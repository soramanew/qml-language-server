package handler

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/owenrumney/go-lsp/lsp"
)

// qmllsClient is a thin LSP client that proxies completion to Qt's own
// qmlls. We keep qmlls' view of every open document in sync with ours via
// didOpen/didChange/didClose/didSave and forward textDocument/completion
// requests to it, returning whatever qmlls hands back.
//
// Everything other than completion stays inside this process — qmlls is a
// pure autocomplete backend here, not a full delegated server.
type qmllsClient struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.ReadCloser
	logger *slog.Logger

	writeMu sync.Mutex
	nextID  atomic.Int64

	pendMu   sync.Mutex
	pending  map[int64]chan jsonrpcResponse
	closed   bool
	shutdown chan struct{}
}

type jsonrpcMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      *int64          `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *jsonrpcError   `json:"error,omitempty"`
}

type jsonrpcError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

type jsonrpcResponse struct {
	Result json.RawMessage
	Err    *jsonrpcError
}

// startQmllsClient looks for a qmlls binary on the system and starts it.
// Returns nil when qmlls isn't installed — the handler treats that as
// "completion disabled" and returns empty completion lists.
func startQmllsClient(logger *slog.Logger, roots []string) *qmllsClient {
	bin := detectQmllsBinary()
	if bin == "" {
		if logger != nil {
			logger.Info("qmlls not found; completion disabled")
		}
		return nil
	}
	if logger != nil {
		logger.Info("qmlls detected", "path", bin)
	}
	cmd := exec.Command(bin)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil
	}
	// Drop stderr to prevent deadlock on a slow stderr pipe. qmlls logs go
	// there but we never read them; best effort only.
	cmd.Stderr = nil
	if err := cmd.Start(); err != nil {
		if logger != nil {
			logger.Warn("failed to start qmlls", "err", err)
		}
		return nil
	}
	c := &qmllsClient{
		cmd:      cmd,
		stdin:    stdin,
		stdout:   stdout,
		logger:   logger,
		pending:  map[int64]chan jsonrpcResponse{},
		shutdown: make(chan struct{}),
	}
	go c.readLoop()

	if err := c.initialize(roots); err != nil {
		if logger != nil {
			logger.Warn("qmlls initialize failed", "err", err)
		}
		c.Stop()
		return nil
	}
	return c
}

// detectQmllsBinary searches $PATH and the common Qt install locations for
// a qmlls binary. Qt 6.4+ ships it under the same prefix as qmllint.
func detectQmllsBinary() string {
	for _, name := range []string{"qmlls-qt6", "qmlls6"} {
		if p, err := exec.LookPath(name); err == nil {
			return p
		}
	}
	for _, p := range []string{
		"/usr/lib/qt6/bin/qmlls",
		"/usr/lib64/qt6/bin/qmlls",
		"/opt/Qt/6/gcc_64/bin/qmlls",
	} {
		if _, err := exec.LookPath(p); err == nil {
			return p
		}
	}
	if p, err := exec.LookPath("qmlls"); err == nil {
		return p
	}
	return ""
}

// Stop terminates the qmlls subprocess and cancels any in-flight requests.
func (c *qmllsClient) Stop() {
	if c == nil {
		return
	}
	c.pendMu.Lock()
	if c.closed {
		c.pendMu.Unlock()
		return
	}
	c.closed = true
	close(c.shutdown)
	pending := c.pending
	c.pending = nil
	c.pendMu.Unlock()

	// Fail any outstanding requests.
	for _, ch := range pending {
		select {
		case ch <- jsonrpcResponse{Err: &jsonrpcError{Code: -32099, Message: "qmlls stopped"}}:
		default:
		}
	}
	_ = c.stdin.Close()
	_ = c.stdout.Close()
	if c.cmd.Process != nil {
		_ = c.cmd.Process.Kill()
	}
	_ = c.cmd.Wait()
}

// readLoop reads framed JSON-RPC messages from qmlls' stdout and dispatches
// responses to the waiting goroutine. Notifications from the server are
// discarded — we only care about request/response round trips.
func (c *qmllsClient) readLoop() {
	reader := bufio.NewReader(c.stdout)
	for {
		msg, err := readLSPMessage(reader)
		if err != nil {
			if !errors.Is(err, io.EOF) && c.logger != nil {
				c.logger.Debug("qmlls read error", "err", err)
			}
			c.Stop()
			return
		}
		if msg.ID == nil {
			// Notification or server-initiated request — ignore.
			continue
		}
		c.pendMu.Lock()
		ch, ok := c.pending[*msg.ID]
		if ok {
			delete(c.pending, *msg.ID)
		}
		c.pendMu.Unlock()
		if !ok {
			continue
		}
		ch <- jsonrpcResponse{Result: msg.Result, Err: msg.Error}
	}
}

func readLSPMessage(r *bufio.Reader) (*jsonrpcMessage, error) {
	var contentLength int
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return nil, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		if strings.HasPrefix(line, "Content-Length:") {
			v := strings.TrimSpace(strings.TrimPrefix(line, "Content-Length:"))
			n, err := strconv.Atoi(v)
			if err != nil {
				return nil, fmt.Errorf("bad Content-Length: %w", err)
			}
			contentLength = n
		}
	}
	if contentLength <= 0 {
		return nil, fmt.Errorf("missing Content-Length header")
	}
	body := make([]byte, contentLength)
	if _, err := io.ReadFull(r, body); err != nil {
		return nil, err
	}
	var msg jsonrpcMessage
	if err := json.Unmarshal(body, &msg); err != nil {
		return nil, err
	}
	return &msg, nil
}

// writeMessage frames a JSON-RPC message and writes it to qmlls' stdin.
func (c *qmllsClient) writeMessage(msg jsonrpcMessage) error {
	body, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	header := fmt.Sprintf("Content-Length: %d\r\n\r\n", len(body))
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if _, err := c.stdin.Write([]byte(header)); err != nil {
		return err
	}
	if _, err := c.stdin.Write(body); err != nil {
		return err
	}
	return nil
}

// Notify sends a one-way JSON-RPC notification.
func (c *qmllsClient) Notify(method string, params any) error {
	raw, err := json.Marshal(params)
	if err != nil {
		return err
	}
	return c.writeMessage(jsonrpcMessage{JSONRPC: "2.0", Method: method, Params: raw})
}

// Request sends a JSON-RPC request and waits for its response, respecting
// ctx cancellation and the deadline.
func (c *qmllsClient) Request(ctx context.Context, method string, params any) (json.RawMessage, error) {
	raw, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}
	id := c.nextID.Add(1)
	ch := make(chan jsonrpcResponse, 1)

	c.pendMu.Lock()
	if c.closed {
		c.pendMu.Unlock()
		return nil, errors.New("qmlls client closed")
	}
	c.pending[id] = ch
	c.pendMu.Unlock()

	if err := c.writeMessage(jsonrpcMessage{
		JSONRPC: "2.0",
		ID:      &id,
		Method:  method,
		Params:  raw,
	}); err != nil {
		c.pendMu.Lock()
		delete(c.pending, id)
		c.pendMu.Unlock()
		return nil, err
	}
	select {
	case resp := <-ch:
		if resp.Err != nil {
			return nil, fmt.Errorf("qmlls %s: %s", method, resp.Err.Message)
		}
		return resp.Result, nil
	case <-ctx.Done():
		c.pendMu.Lock()
		delete(c.pending, id)
		c.pendMu.Unlock()
		return nil, ctx.Err()
	}
}

func (c *qmllsClient) initialize(roots []string) error {
	var workspaceFolders []map[string]string
	var rootURI string
	for _, r := range roots {
		if r == "" {
			continue
		}
		u := &url.URL{Scheme: "file", Path: r}
		uri := u.String()
		if rootURI == "" {
			rootURI = uri
		}
		workspaceFolders = append(workspaceFolders, map[string]string{
			"uri":  uri,
			"name": r,
		})
	}
	params := map[string]any{
		"processId":    nil,
		"rootUri":      rootURI,
		"capabilities": map[string]any{},
	}
	if len(workspaceFolders) > 0 {
		params["workspaceFolders"] = workspaceFolders
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := c.Request(ctx, "initialize", params); err != nil {
		return err
	}
	return c.Notify("initialized", map[string]any{})
}

// DidOpen forwards a textDocument/didOpen notification.
func (c *qmllsClient) DidOpen(uri lsp.DocumentURI, languageID, text string, version int) {
	if c == nil {
		return
	}
	_ = c.Notify("textDocument/didOpen", map[string]any{
		"textDocument": map[string]any{
			"uri":        uri,
			"languageId": languageID,
			"version":    version,
			"text":       text,
		},
	})
}

// DidChange forwards a full-document sync. We always send the whole text,
// matching our internal SyncFull mode.
func (c *qmllsClient) DidChange(uri lsp.DocumentURI, text string, version int) {
	if c == nil {
		return
	}
	_ = c.Notify("textDocument/didChange", map[string]any{
		"textDocument": map[string]any{
			"uri":     uri,
			"version": version,
		},
		"contentChanges": []map[string]any{{"text": text}},
	})
}

// DidClose forwards a textDocument/didClose notification.
func (c *qmllsClient) DidClose(uri lsp.DocumentURI) {
	if c == nil {
		return
	}
	_ = c.Notify("textDocument/didClose", map[string]any{
		"textDocument": map[string]any{"uri": uri},
	})
}

// DidSave forwards a textDocument/didSave notification. Text is optional
// per the spec; we include it when we have it.
func (c *qmllsClient) DidSave(uri lsp.DocumentURI, text string) {
	if c == nil {
		return
	}
	params := map[string]any{
		"textDocument": map[string]any{"uri": uri},
	}
	if text != "" {
		params["text"] = text
	}
	_ = c.Notify("textDocument/didSave", params)
}

// Completion proxies a textDocument/completion request to qmlls and
// returns the raw JSON response, which handler code then decodes as
// either a CompletionList or a []CompletionItem (qmlls picks one).
func (c *qmllsClient) Completion(ctx context.Context, params *lsp.CompletionParams) (*lsp.CompletionList, error) {
	if c == nil {
		return &lsp.CompletionList{Items: []lsp.CompletionItem{}}, nil
	}
	raw, err := c.Request(ctx, "textDocument/completion", params)
	if err != nil {
		return nil, err
	}
	if len(raw) == 0 || string(raw) == "null" {
		return &lsp.CompletionList{Items: []lsp.CompletionItem{}}, nil
	}
	// qmlls may return either a CompletionItem[] or a CompletionList.
	var list lsp.CompletionList
	if err := json.Unmarshal(raw, &list); err == nil && list.Items != nil {
		return &list, nil
	}
	var items []lsp.CompletionItem
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, err
	}
	return &lsp.CompletionList{Items: items}, nil
}
