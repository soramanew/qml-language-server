# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Overview

A Go-based Language Server Protocol implementation for QML (Qt Meta-Object Language). The binary speaks LSP over stdio and is intended to be launched by editors (VS Code, Neovim, etc.).

## Commands

- `make build` — compile to `./qml-language-server` with version ldflags
- `make test` / `go test -v -race ./...` — full test suite
- `go test -v -run TestName ./handler` — run a single test
- `make lint` — `golangci-lint run ./...`
- `make coverage` — writes `coverage.out` / `coverage.html`
- `make install` — builds and installs to `~/.local/bin`

Requires Go 1.26.1+ (see `go.mod`).

## Architecture

### Entry point & server lifecycle
`main.go` constructs a `handler.Handler` and calls `Serve`, which wires the `owenrumney/go-lsp` server to stdio. `Handler.Initialize` (handler/handler.go) is the source of truth for advertised LSP capabilities — add a new capability here when implementing a new feature.

### Handler package (`handler/`)
One file per LSP feature (`hover.go`, `completion.go`, `definition.go`, `references.go`, `diagnostics.go`, `symbols.go`, `codeactions.go`, `rename.go`, `signature.go`, `inlayhints.go`). All share:

- `Handler.documents` — `map[DocumentURI]string` kept in sync by `DidOpen`/`DidChange`/`DidSave`/`DidClose`.
- `Handler.parser` (`*QMLParser`, `parser.go`) — wraps gotreesitter. Maintains a per-URI `*gotreesitter.Tree`, reparses incrementally via `ParseIncremental` when a previous tree exists.
- `Handler.workspace` (`*workspaceIndex`, `workspace.go`) — scans workspace roots for `*.qml` files at startup so user-defined components show up in the symbol registry (used for hover and go-to-definition).
- `Handler.qmlls` (`*qmllsClient`, `qmlls_client.go`) — a thin LSP client that speaks stdio to Qt's own `qmlls` subprocess. Completion is the only request we forward; document lifecycle notifications (`didOpen`/`didChange`/`didClose`/`didSave`) are mirrored to keep qmlls' view of every open file in sync with ours. When the `qmlls` binary isn't installed the client is nil and `Completion` returns an empty list — the server stays functional for every other feature.
- `positions.go` — `positionToByte` / `byteOffsetToPosition` convert between LSP `Position` (line/char) and tree-sitter byte offsets, and `findSmallestNodeAt` resolves a cursor position to an AST node. Use these helpers from feature code.
- `types.go` — thin accessors around the flat symbol registry (`registryTypeInfo` / `registryPropertyInfo`). No per-type property catalog and no inheritance graph are tracked in-process anymore; qmlls owns all of that.
- `registry.go` — process-wide `QMLSymbol` registry. `registerKeywords` and `registerJSBuiltins` seed keywords and JS/Qt globals so hover has something to show for them. Type/module entries are added at runtime by `qmltypes_discovery.go`.
- `qmltypes_parser.go` + `qmltypes_types.go` — recursive descent parser for Qt's `.qmltypes` DSL format, producing structured Go types for components, properties, signals, methods, and enums.
- `qmldir_parser.go` — parses `qmldir` files to discover module name and path to `.qmltypes`.
- `qmltypes_discovery.go` — at startup, walks Qt install dirs and `QML_IMPORT_PATH` to find and parse every module's type info, then registers each exported type and its module in the flat symbol registry (for hover) and each public method in `functionSignatures` (for signature help). Property catalogs and inheritance chains are intentionally not built — completion is qmlls' responsibility.
- `completion.go` — a two-line handler that proxies `textDocument/completion` to `Handler.qmlls`. There is no in-process completion logic.
- `diagnostics.go` — `collectDiagnostics` is a no-op. Tree-sitter's error recovery cascades one typo into a screen full of ERROR nodes, so we rely on qmllint (when installed) for syntax feedback instead of relaying tree-sitter errors.

### Grammar package (`grammars/`)
Loads the QML tree-sitter grammar into `gotreesitter`. Two embedded assets are combined at startup by `QmljsLanguage()` (grammars/loader.go):

1. `qmljs.grammar.json` — run through `grammargen.ImportGrammarJSON` + `GenerateLanguage` to build the parse tables in pure Go.
2. `grammar_blobs/qmljs.bin` — a gzipped gob-encoded reference `Language`; used only to copy the external-scanner ordering via `AdaptExternalScannerByExternalOrder`.
3. `qmljs_scanner.go` — hand-written Go port of the tree-sitter-qmljs external scanner (automatic semicolon insertion, template literals, regex). Registered via `grammars.RegisterExternalScanner` before the language is detected.

Query files in `grammars/queries/` (`highlights.scm`, `locals.scm`) are embedded and used for highlighting; highlights inherit from the `javascript` query set.

### Diagnostics
Tree-sitter-derived diagnostics are disabled (`collectDiagnostics` is a no-op); diagnostics come from external tools only — qmllint for syntax and semantics, and the project's conventions script for style — delivered via the **push** model only (`textDocument/publishDiagnostics`). We deliberately do **not** implement `textDocument/diagnostic` (pull) — qmllint is async and can take >1s, and a synchronous pull would either block or return nothing and then overwrite whatever we had just pushed. Because go-lsp auto-advertises `DiagnosticProvider` whenever the handler implements `DocumentDiagnostic`, removing that method is the on/off switch; don't add it back without also serving cached lint results synchronously. qmllint itself has no stdin mode — `--json -` directs its JSON report to stdout, not source input — so `stageLintBuffer` writes the in-memory buffer to a hidden `.qmllsbuf-*.qml` beside the real file and hands that path to qmllint, then removes it after. Writing in the same directory (rather than /tmp) preserves qmllint's implicit-module resolution of sibling components; the dot-prefix guarantees the scratch file can never be interpreted as a QML component (component filenames must start with uppercase). `handler.startDiagnostics` runs immediately on `DidOpen`/`DidSave`; on `DidChange`, `handler.scheduleDiagnostics` defers the round behind a 400ms debounce (`qmllintDebounce`). `DidChange` only calls `reparse` — it does not publish diagnostics — so the editor keeps showing the last qmllint results until the scheduled run replaces them, avoiding a per-keystroke "blank-then-refill" flash. `publishDiagnostics` always normalizes `nil` to `[]lsp.Diagnostic{}` before sending — do not remove that, empty-vs-nil matters for some LSP clients.

**Nothing before `initialized`.** `publishDiagnostics` drops everything until the handshake completes: a notification that reaches the client before it has processed our initialize response is a protocol violation, and Node-based clients respond by destroying the connection (the next request then fails with "Cannot call write after a stream was destroyed"). go-lsp registers its own no-op for the `initialized` notification and never dispatches to `Handler.Initialized`, so `Serve` claims the method explicitly — **don't remove that override**, without it the handshake is never marked complete and every diagnostic is dropped. `markInitialized` flushes whatever was cached in the meantime, and a `initFallback` timer (2s) opens the gate anyway for a client that never sends `initialized`. `TestDiagnosticsReachClientAfterHandshake` drives a real handshake over a pipe (via `Handler.serve`, the injectable-transport form of `Serve`) and asserts both halves.

**Multiple producers, one list.** There are two external diagnostic producers (qmllint and the conventions script) and `publishDiagnostics` replaces the client's *entire* list for a URI, so neither may publish on its own — whichever finished last would erase the other's findings. Every producer instead calls `h.setDiagnostics(uri, source, diags)`, which caches the result under that source key in `h.diagBySource` and publishes the union in `diagSources` order. Add a new producer by adding its source constant to `diagSources` and having it call `setDiagnostics`; never call `publishDiagnostics` directly from a producer. `clearDiagnostics` (DidClose) drops the whole cache entry so a reopened file doesn't inherit stale findings.

`startDiagnostics` runs every available producer concurrently under one cancellable `diagnosticRun` per URI, so the slow tool never delays the fast one's findings; `scheduleDiagnostics` is the debounced form and `cancelDiagnostics` kills a pending or in-flight round. A cancelled round leaves the cache alone on purpose — stale-but-present beats blanking the editor until the next round lands.

### Completion
Completion is delegated wholesale to Qt's `qmlls`. We never generate completion items in-process — no keyword lists, no property catalogs, no id-scope walking. Adding anything to `completion.go` beyond "ask qmlls" is a regression.

### Formatting
Formatting is delegated to Qt's `qmlformat` via `handler/qmlformat.go`. Each request writes the current document to a temp file, runs qmlformat, and returns the stdout as a single whole-document TextEdit. When the binary isn't on the system the capability is omitted from Initialize and the handler returns no edits. Do not add an in-process formatter — qmlformat is the source of truth.

### Conventions script
If the project ships `scripts/qml-lint-conventions.py` under any workspace root, `handler/qmlconventions.go` drives it two different ways.

**Post-save fix (`conventionsRunner.Run`).** `DidSave` fires `<script> --fix` asynchronously after the lint run, with cwd=project root and no file argument — the real script walks every `*.qml` under its own root and rewrites each in place. We don't read its output: disk changes flow back to the editor through its own file watcher.

**Buffer check (`conventionsRunner.Check`).** Every diagnostic round runs `<script> --file - --json`, piping the unsaved buffer in on stdin, and turns the report into warning-severity diagnostics under the `qml-conventions` source. The script's contract is that **the report goes to stderr** (stdout is reserved for fixed source, which only `--fix` emits), so `Check` parses stderr and ignores exit status — the script exits 1 whenever it finds anything. We deliberately don't pass `--fix` here: with `--fix` the script reports against the source it rewrote, whose line numbers no longer match the buffer the editor is showing. The unfixed report therefore lists violations the post-save `--fix` will clear anyway; correct positions are worth that. The script reports a line but no column, so each diagnostic spans its whole line, and lines outside the buffer (a `0` line, or a report that raced an edit) are pinned to line 0 rather than dropped.

Both paths are strictly best-effort — missing script is a silent no-op, and a failing, silent, or non-JSON subprocess yields no diagnostics rather than an error. A broken convention tool must never block a save or lose the other producer's diagnostics.

## Notes for editing

- When adding a new LSP method, update both `Handler.Initialize`'s capabilities and add the method on `Handler`.
- The parser can return `nil` if grammar loading fails; all handler features guard against `h.parser == nil` and `tree == nil`.
- Incremental parse relies on calling `Parse` with the full new document text on every `DidChange`; the server does not apply individual content change ranges.
