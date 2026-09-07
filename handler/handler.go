package handler

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/odvcencio/gotreesitter"
	"github.com/owenrumney/go-lsp/lsp"
	"github.com/owenrumney/go-lsp/server"
)

// qmllintTimeout bounds a single qmllint invocation. A cold run that has to
// load plugins and walk QML_IMPORT_PATH typically finishes well under a
// second; ten seconds is a generous cap to cover very large projects before
// we give up and show tree-sitter diagnostics only.
const qmllintTimeout = 10 * time.Second

// initializedFallback is how long we wait for the client's `initialized`
// notification before letting diagnostics flow anyway.
const initializedFallback = 2 * time.Second

// qmllintDebounce is how long we wait after the last keystroke before firing
// a lint. Short enough that diagnostics feel live; long enough that a bursty
// typist doesn't spawn a subprocess per keystroke.
const qmllintDebounce = 400 * time.Millisecond

type Handler struct {
	logger *slog.Logger

	docMu     sync.RWMutex
	documents map[lsp.DocumentURI]string

	parser    *QMLParser
	server    *server.Server
	workspace *workspaceIndex

	qmllint     *qmllintRunner
	qmlls       *qmllsClient
	qmlformat   *qmlformatRunner
	conventions *conventionsRunner

	// lintMu guards the in-flight/pending bookkeeping for diagnostic runs.
	lintMu      sync.Mutex
	lintCancels map[lsp.DocumentURI]*diagnosticRun
	lintTimers  map[lsp.DocumentURI]*time.Timer

	// initMu guards initialized, which gates diagnostic delivery — see
	// publishDiagnostics.
	initMu      sync.Mutex
	initialized bool
	// initFallback is a per-handler copy so tests can push it out of reach.
	initFallback time.Duration

	// diagMu guards diagBySource: the last diagnostics each producer
	// reported per URI. See setDiagnostics.
	diagMu       sync.Mutex
	diagBySource map[lsp.DocumentURI]map[string][]lsp.Diagnostic
}

// diagnosticRun is one round of diagnostic subprocesses for a URI;
// cancelling it cancels every tool in the round. A pointer type so a
// finishing round can check by identity that it's still the current one.
type diagnosticRun struct {
	cancel context.CancelFunc
}

// diagSources is the publish order of the producers. A fixed slice rather
// than map order, so editors don't reshuffle their problem lists.
var diagSources = []string{diagSourceQmllint, diagSourceConventions}

func New(logger *slog.Logger) *Handler {
	parser := NewQMLParser()
	if parser == nil && logger != nil {
		logger.Error("failed to load QML grammar; all language features will be disabled")
	}
	return &Handler{
		logger:       logger,
		initFallback: initializedFallback,
		documents:    make(map[lsp.DocumentURI]string),
		parser:       parser,
		workspace:    newWorkspaceIndex(),
		qmllint:      newQmllintRunner(logger),
		qmlformat:    newQmlformatRunner(logger),
		conventions:  newConventionsRunner(logger),
		lintCancels:  make(map[lsp.DocumentURI]*diagnosticRun),
		lintTimers:   make(map[lsp.DocumentURI]*time.Timer),
		diagBySource: make(map[lsp.DocumentURI]map[string][]lsp.Diagnostic),
	}
}

// Serve runs the server over stdio. The wiring lives in serve so tests can
// drive the same registration path over a pipe.
func (h *Handler) Serve(ctx context.Context) error {
	return h.serve(ctx, server.RunStdio())
}

func (h *Handler) serve(ctx context.Context, rw io.ReadWriteCloser) error {
	h.server = server.NewServer(h)
	// Override go-lsp's built-in shutdown handler: its default returns untyped
	// nil, which makes the response omit `result` entirely, and Neovim rejects
	// that as INVALID_SERVER_MESSAGE. LSP requires `result: null` explicitly.
	h.server.HandleMethod("shutdown", func(ctx context.Context, _ json.RawMessage) (any, error) {
		_ = h.Shutdown(ctx)
		return json.RawMessage("null"), nil
	})
	// go-lsp's dispatcher ignores errors from notification handlers, so the
	// library's built-in `exit` handler never actually terminates. Without
	// this override the process only exits on stdin EOF, causing the editor
	// to block on its shutdown timeout.
	h.server.HandleNotification("exit", func(context.Context, json.RawMessage) error {
		os.Exit(0)
		return nil
	})
	// go-lsp registers its own no-op for `initialized` and never dispatches
	// to Handler.Initialized, so claim it explicitly — without this the
	// handshake never completes and every diagnostic is dropped.
	h.server.HandleNotification("initialized", func(ctx context.Context, _ json.RawMessage) error {
		return h.Initialized(ctx, nil)
	})
	return h.server.Run(ctx, rw)
}

// getDocument returns the current text for uri. Returns "", false when the
// document isn't open.
func (h *Handler) getDocument(uri lsp.DocumentURI) (string, bool) {
	h.docMu.RLock()
	defer h.docMu.RUnlock()
	doc, ok := h.documents[uri]
	return doc, ok
}

func (h *Handler) setDocument(uri lsp.DocumentURI, text string) {
	h.docMu.Lock()
	h.documents[uri] = text
	h.docMu.Unlock()
}

func (h *Handler) deleteDocument(uri lsp.DocumentURI) {
	h.docMu.Lock()
	delete(h.documents, uri)
	h.docMu.Unlock()
}

func (h *Handler) Initialize(_ context.Context, params *lsp.InitializeParams) (*lsp.InitializeResult, error) {
	roots := workspaceRootsFromInitialize(params)
	if h.workspace != nil {
		h.workspace.setRoots(roots)
		go h.workspace.scan()
	}
	h.conventions.SetRoots(roots)
	// Safety net for a client that never sends `initialized`: late
	// diagnostics beat none.
	time.AfterFunc(h.initFallback, h.markInitialized)
	go DiscoverAndRegisterQMLTypes(h.logger, roots)
	h.qmlls = startQmllsClient(h.logger, roots)
	completionProvider := (*lsp.CompletionOptions)(nil)
	if h.qmlls != nil {
		completionProvider = &lsp.CompletionOptions{
			TriggerCharacters: []string{".", ":", "<", "\"", "/"},
		}
	}
	return &lsp.InitializeResult{
		Capabilities: lsp.ServerCapabilities{
			TextDocumentSync: &lsp.TextDocumentSyncOptions{
				OpenClose: boolPtr(true),
				Change:    lsp.SyncFull,
				Save:      &lsp.SaveOptions{IncludeText: boolPtr(true)},
			},
			HoverProvider:             boolPtr(true),
			CompletionProvider:        completionProvider,
			DefinitionProvider:        boolPtr(true),
			ReferencesProvider:        boolPtr(true),
			DocumentSymbolProvider:    boolPtr(true),
			DocumentHighlightProvider: boolPtr(true),
			SignatureHelpProvider: &lsp.SignatureHelpOptions{
				TriggerCharacters: []string{"(", ","},
			},
			CodeActionProvider: &lsp.CodeActionOptions{},
			RenameProvider: &lsp.RenameOptions{
				PrepareProvider: boolPtr(true),
			},
			// DiagnosticProvider is intentionally omitted: qmllint is async and
			// can take >1s, so we publish diagnostics via the push model
			// (textDocument/publishDiagnostics). Advertising pull would make
			// clients call textDocument/diagnostic on every keystroke and
			// expect an immediate, authoritative response — they'd overwrite
			// our debounced push results with whatever the synchronous pull
			// returned, which in our case is nothing.
			InlayHintProvider: &lsp.InlayHintOptions{},
			SemanticTokensProvider: &lsp.SemanticTokensOptions{
				Legend: SemanticTokensLegend(),
				Full:   &lsp.SemanticTokensFull{},
			},
			FoldingRangeProvider:            boolPtr(true),
			WorkspaceSymbolProvider:         boolPtr(true),
			DocumentFormattingProvider:      boolPtrIf(h.qmlformat != nil),
			DocumentRangeFormattingProvider: boolPtrIf(h.qmlformat != nil),
			DocumentLinkProvider:            &lsp.DocumentLinkOptions{},
		},
		ServerInfo: &lsp.ServerInfo{
			Name:    "qml-language-server",
			Version: "0.1.0",
		},
	}, nil
}

// Initialized marks the handshake complete — the client sends it only after
// processing our initialize response, so it's the first moment we may push.
func (h *Handler) Initialized(_ context.Context, _ *lsp.InitializedParams) error {
	h.markInitialized()
	return nil
}

// markInitialized opens the gate and flushes what was produced meanwhile.
// Idempotent: the notification and the fallback timer race by design.
func (h *Handler) markInitialized() {
	h.initMu.Lock()
	already := h.initialized
	h.initialized = true
	h.initMu.Unlock()
	if already {
		return
	}
	for uri, diags := range h.snapshotDiagnostics() {
		h.publishDiagnostics(uri, diags)
	}
}

// snapshotDiagnostics returns the merged cache for every URI, for replay.
func (h *Handler) snapshotDiagnostics() map[lsp.DocumentURI][]lsp.Diagnostic {
	h.diagMu.Lock()
	defer h.diagMu.Unlock()
	out := make(map[lsp.DocumentURI][]lsp.Diagnostic, len(h.diagBySource))
	for uri, bySource := range h.diagBySource {
		var merged []lsp.Diagnostic
		for _, s := range diagSources {
			merged = append(merged, bySource[s]...)
		}
		out[uri] = merged
	}
	return out
}
func (h *Handler) Shutdown(_ context.Context) error {
	if h.qmlls != nil {
		h.qmlls.Stop()
	}
	return nil
}
func (h *Handler) Exit(_ context.Context) error { return nil }

func (h *Handler) publishDiagnostics(uri lsp.DocumentURI, diagnostics []lsp.Diagnostic) {
	if h.server == nil || h.server.Client == nil {
		return
	}
	// Publishing before the client has processed our initialize response is
	// a protocol violation that makes Node clients destroy the connection,
	// and go-lsp dispatches every message on its own goroutine so a fast
	// producer can beat a slow Initialize onto the wire. Dropping is safe:
	// the results stay cached and Initialized flushes them.
	h.initMu.Lock()
	ready := h.initialized
	h.initMu.Unlock()
	if !ready {
		return
	}
	if diagnostics == nil {
		diagnostics = []lsp.Diagnostic{}
	}
	_ = h.server.Client.PublishDiagnostics(context.Background(), &lsp.PublishDiagnosticsParams{
		URI:         uri,
		Diagnostics: diagnostics,
	})
}

// setDiagnostics records one producer's latest result and republishes the
// union. publishDiagnostics replaces the client's whole list per URI, so a
// producer publishing alone would erase the other's findings.
func (h *Handler) setDiagnostics(uri lsp.DocumentURI, source string, diags []lsp.Diagnostic) {
	h.publishDiagnostics(uri, h.cacheDiagnostics(uri, source, diags))
}

// cacheDiagnostics records diags under source for uri and returns the union
// of every producer, in diagSources order.
func (h *Handler) cacheDiagnostics(uri lsp.DocumentURI, source string, diags []lsp.Diagnostic) []lsp.Diagnostic {
	h.diagMu.Lock()
	bySource, ok := h.diagBySource[uri]
	if !ok {
		bySource = make(map[string][]lsp.Diagnostic, len(diagSources))
		h.diagBySource[uri] = bySource
	}
	bySource[source] = diags
	total := 0
	for _, s := range diagSources {
		total += len(bySource[s])
	}
	merged := make([]lsp.Diagnostic, 0, total)
	for _, s := range diagSources {
		merged = append(merged, bySource[s]...)
	}
	h.diagMu.Unlock()
	return merged
}

// clearDiagnostics drops the cache for uri and clears the client's display,
// so a reopened file doesn't inherit the last session's diagnostics.
func (h *Handler) clearDiagnostics(uri lsp.DocumentURI) {
	h.diagMu.Lock()
	delete(h.diagBySource, uri)
	h.diagMu.Unlock()
	h.publishDiagnostics(uri, nil)
}

// reparse updates the tree-sitter tree for uri. It used to also publish
// diagnostics, but tree-sitter diagnostics are intentionally a no-op (qmllint
// is our only source) and publishing an empty list on every keystroke blanks
// the editor's lint display until the debounced qmllint run catches up.
// Diagnostics are now published only by startDiagnostics' producers.
func (h *Handler) reparse(uri lsp.DocumentURI, text string) {
	if h.parser == nil {
		return
	}
	h.parser.Parse(uri, text)
}

// diagProducer is one external checker's contribution to a diagnostic
// round: what to run, how long to give it, and which cache key its result
// lands under.
type diagProducer struct {
	source  string
	timeout time.Duration
	check   func(ctx context.Context) []lsp.Diagnostic
}

// diagProducers returns the checkers available for path/source. The
// per-project scripts are probed here, once per round, rather than inside a
// goroutine — findScript hits the filesystem.
func (h *Handler) diagProducers(uri lsp.DocumentURI, path, source string) []diagProducer {
	var producers []diagProducer
	if h.qmllint != nil {
		importPaths := h.lintImportPaths(path)
		producers = append(producers, diagProducer{
			source:  diagSourceQmllint,
			timeout: qmllintTimeout,
			check: func(ctx context.Context) []lsp.Diagnostic {
				// Tree-sitter rides along rather than publishing on its own:
				// collectDiagnostics is a no-op, so an eager publish would
				// put an empty list on the wire for nothing.
				return append(h.getDiagnostics(uri), h.qmllint.Lint(ctx, path, source, importPaths)...)
			},
		})
	}
	if h.conventions.findScript() != "" {
		producers = append(producers, diagProducer{
			source:  diagSourceConventions,
			timeout: conventionsCheckTimeout,
			check: func(ctx context.Context) []lsp.Diagnostic {
				return h.conventions.Check(ctx, source)
			},
		})
	}
	return producers
}

// startDiagnostics runs every external checker — qmllint and the conventions
// script — over the in-memory buffer,
// concurrently under one cancellable round, each publishing as it finishes.
// Any in-flight round for the URI is cancelled first so stale results can't
// land. No-op when no tool is available, the URI isn't a local file, or the
// document isn't open.
func (h *Handler) startDiagnostics(uri lsp.DocumentURI) {
	path := uriToPath(uri)
	if path == "" {
		return
	}
	source, ok := h.getDocument(uri)
	if !ok {
		return
	}
	producers := h.diagProducers(uri, path, source)
	if len(producers) == 0 {
		return
	}

	h.cancelDiagnostics(uri)
	ctx, cancel := context.WithCancel(context.Background())
	run := &diagnosticRun{cancel: cancel}
	h.lintMu.Lock()
	h.lintCancels[uri] = run
	h.lintMu.Unlock()

	var wg sync.WaitGroup
	for _, p := range producers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			checkCtx, checkCancel := context.WithTimeout(ctx, p.timeout)
			defer checkCancel()
			diags := p.check(checkCtx)
			// If the user edited meanwhile, DidChange cancelled this round
			// and a newer one owns the display — don't overwrite it with
			// results for the previous text.
			if ctx.Err() != nil {
				return
			}
			h.setDiagnostics(uri, p.source, diags)
		}()
	}

	go func() {
		wg.Wait()
		cancel()
		// By identity: if a newer round was installed after we were
		// cancelled, deleting blindly would leak its subprocesses.
		h.lintMu.Lock()
		if h.lintCancels[uri] == run {
			delete(h.lintCancels, uri)
		}
		h.lintMu.Unlock()
	}()
}

// scheduleDiagnostics defers startDiagnostics until the user is idle for
// `delay`. Every call resets the timer, so a bursty typist spawns one round.
func (h *Handler) scheduleDiagnostics(uri lsp.DocumentURI, delay time.Duration) {
	h.lintMu.Lock()
	if t, ok := h.lintTimers[uri]; ok {
		t.Stop()
	}
	h.lintTimers[uri] = time.AfterFunc(delay, func() {
		h.lintMu.Lock()
		delete(h.lintTimers, uri)
		h.lintMu.Unlock()
		h.startDiagnostics(uri)
	})
	h.lintMu.Unlock()
}

// cancelDiagnostics drops a pending round for uri and cancels an in-flight
// one. Cached results are left alone — keeping stale diagnostics beats
// blanking the display until a fresh round lands.
func (h *Handler) cancelDiagnostics(uri lsp.DocumentURI) {
	h.lintMu.Lock()
	defer h.lintMu.Unlock()
	if t, ok := h.lintTimers[uri]; ok {
		t.Stop()
		delete(h.lintTimers, uri)
	}
	if run, ok := h.lintCancels[uri]; ok {
		run.cancel()
		delete(h.lintCancels, uri)
	}
}

func (h *Handler) DidOpen(_ context.Context, params *lsp.DidOpenTextDocumentParams) error {
	h.setDocument(params.TextDocument.URI, params.TextDocument.Text)
	h.reparse(params.TextDocument.URI, params.TextDocument.Text)
	h.startDiagnostics(params.TextDocument.URI)
	if h.workspace != nil {
		h.workspace.registerURI(params.TextDocument.URI)
	}
	if h.qmlls != nil {
		h.qmlls.DidOpen(params.TextDocument.URI, params.TextDocument.LanguageID,
			params.TextDocument.Text, int(params.TextDocument.Version))
	}
	return nil
}

func (h *Handler) DidChange(_ context.Context, params *lsp.DidChangeTextDocumentParams) error {
	// With Change: SyncFull the last change carries the full document. Earlier
	// entries are dropped in the same spirit as the previous code.
	if len(params.ContentChanges) == 0 {
		return nil
	}
	text := params.ContentChanges[len(params.ContentChanges)-1].Text
	h.setDocument(params.TextDocument.URI, text)
	// Cancel any in-flight lint: its results reflect a pre-edit snapshot and
	// must not overwrite fresher diagnostics. Reparse the tree so hover,
	// go-to-def, etc. stay accurate, but do NOT publish diagnostics here —
	// publishing an empty list on every keystroke would blank the editor's
	// lint display until the 400ms debounce completes, causing a visible
	// flash. Instead, leave the prior qmllint results on screen (slightly
	// stale, at most for qmllintDebounce), and let the scheduled lint push
	// the fresh set when it finishes.
	h.cancelDiagnostics(params.TextDocument.URI)
	h.reparse(params.TextDocument.URI, text)
	h.scheduleDiagnostics(params.TextDocument.URI, qmllintDebounce)
	if h.qmlls != nil {
		h.qmlls.DidChange(params.TextDocument.URI, text, int(params.TextDocument.Version))
	}
	return nil
}

func (h *Handler) DidClose(_ context.Context, params *lsp.DidCloseTextDocumentParams) error {
	h.deleteDocument(params.TextDocument.URI)
	h.cancelDiagnostics(params.TextDocument.URI)
	if h.parser != nil {
		h.parser.Invalidate(params.TextDocument.URI)
	}
	h.clearDiagnostics(params.TextDocument.URI)
	if h.qmlls != nil {
		h.qmlls.DidClose(params.TextDocument.URI)
	}
	return nil
}

func (h *Handler) DidSave(_ context.Context, params *lsp.DidSaveTextDocumentParams) error {
	if params.Text == nil {
		return nil
	}
	h.setDocument(params.TextDocument.URI, *params.Text)
	h.reparse(params.TextDocument.URI, *params.Text)
	h.startDiagnostics(params.TextDocument.URI)
	if h.qmlls != nil {
		h.qmlls.DidSave(params.TextDocument.URI, *params.Text)
	}
	// Fire-and-forget: the conventions script may walk the whole project and
	// rewrite files in place. The editor picks up the resulting disk changes
	// through its own file watcher, so we don't read its output. Running in
	// a goroutine keeps the save response snappy on large projects.
	if h.conventions != nil {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), conventionsTimeout)
			defer cancel()
			h.conventions.Run(ctx)
		}()
	}
	return nil
}

func (h *Handler) DidChangeWatchedFiles(_ context.Context, _ *lsp.DidChangeWatchedFilesParams) error {
	if h.workspace != nil {
		go h.workspace.scan()
	}
	return nil
}

func (h *Handler) DocumentHighlight(_ context.Context, params *lsp.DocumentHighlightParams) ([]lsp.DocumentHighlight, error) {
	uri := params.TextDocument.URI
	doc, ok := h.getDocument(uri)
	if !ok || h.parser == nil {
		return nil, nil
	}
	tree := h.parser.GetTree(uri)
	if tree == nil {
		return nil, nil
	}
	root := tree.RootNode()
	if root == nil {
		return nil, nil
	}

	lang := h.parser.Language()
	content := []byte(doc)
	node := h.parser.GetNodeAt(uri, params.Position, content)
	if node == nil || node.Type(lang) != "identifier" {
		return nil, nil
	}
	target := string(content[node.StartByte():node.EndByte()])

	var highlights []lsp.DocumentHighlight
	walkTree(root, func(n *gotreesitter.Node) bool {
		if n.Type(lang) == "identifier" && string(content[n.StartByte():n.EndByte()]) == target {
			highlights = append(highlights, lsp.DocumentHighlight{Range: nodeRange(content, n)})
		}
		return true
	})
	return highlights, nil
}

func (h *Handler) getDiagnostics(uri lsp.DocumentURI) []lsp.Diagnostic {
	if h.parser == nil {
		return nil
	}
	tree := h.parser.GetTree(uri)
	if tree == nil {
		return nil
	}
	doc, ok := h.getDocument(uri)
	if !ok {
		return nil
	}
	var diagnostics []lsp.Diagnostic
	collectDiagnostics(tree.RootNode(), h.parser.Language(), []byte(doc), &diagnostics)
	return diagnostics
}

func boolPtr(b bool) *bool { return &b }

// boolPtrIf returns a *bool pointing to true when b is true, and nil
// otherwise. Used for capability fields where omitting the field
// altogether tells the client "feature not available".
func boolPtrIf(b bool) *bool {
	if !b {
		return nil
	}
	return boolPtr(true)
}
