package handler

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/owenrumney/go-lsp/lsp"
)

// Pull-diagnostic tests used to live here. We stopped advertising
// textDocument/diagnostic (the pull model) because qmllint is async and
// clients that pull expect a synchronous, authoritative answer we can't
// provide — they'd overwrite our debounced push results with whatever the
// pull returned (empty). Diagnostics are published via
// textDocument/publishDiagnostics and are covered by TestQmllintRunnerEndToEnd
// in qmllint_test.go.

func diagWithMessage(msg string) lsp.Diagnostic {
	return lsp.Diagnostic{Message: msg}
}

func messages(diags []lsp.Diagnostic) []string {
	out := make([]string, 0, len(diags))
	for _, d := range diags {
		out = append(out, d.Message)
	}
	return out
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestCacheDiagnosticsMergesSources guards what the cache exists for: every
// write yields the union in diagSources order, whoever reports first.
func TestCacheDiagnosticsMergesSources(t *testing.T) {
	h := New(nil)
	uri := lsp.DocumentURI("file:///tmp/Foo.qml")

	got := h.cacheDiagnostics(uri, diagSourceConventions, []lsp.Diagnostic{diagWithMessage("style")})
	if want := []string{"style"}; !equalStrings(messages(got), want) {
		t.Fatalf("after conventions: %v, want %v", messages(got), want)
	}

	got = h.cacheDiagnostics(uri, diagSourceQmllint, []lsp.Diagnostic{diagWithMessage("syntax")})
	// qmllint leads in diagSources even though it reported second.
	if want := []string{"syntax", "style"}; !equalStrings(messages(got), want) {
		t.Fatalf("after qmllint: %v, want %v", messages(got), want)
	}

	// A producer reporting a clean run clears only its own findings.
	got = h.cacheDiagnostics(uri, diagSourceQmllint, nil)
	if want := []string{"style"}; !equalStrings(messages(got), want) {
		t.Fatalf("after clean qmllint: %v, want %v", messages(got), want)
	}
}

// TestCacheDiagnosticsIsolatesURIs: no leaking between files.
func TestCacheDiagnosticsIsolatesURIs(t *testing.T) {
	h := New(nil)
	h.cacheDiagnostics("file:///tmp/Foo.qml", diagSourceQmllint, []lsp.Diagnostic{diagWithMessage("foo")})
	got := h.cacheDiagnostics("file:///tmp/Bar.qml", diagSourceQmllint, []lsp.Diagnostic{diagWithMessage("bar")})
	if want := []string{"bar"}; !equalStrings(messages(got), want) {
		t.Errorf("Bar.qml diagnostics = %v, want %v", messages(got), want)
	}
}

// TestClearDiagnosticsDropsCache: DidClose, then a reopened file must not
// inherit the last session's diagnostics.
func TestClearDiagnosticsDropsCache(t *testing.T) {
	h := New(nil)
	uri := lsp.DocumentURI("file:///tmp/Foo.qml")
	h.cacheDiagnostics(uri, diagSourceQmllint, []lsp.Diagnostic{diagWithMessage("stale")})
	h.clearDiagnostics(uri)
	if got := h.cacheDiagnostics(uri, diagSourceConventions, nil); len(got) != 0 {
		t.Errorf("after clear: %v, want empty", messages(got))
	}
}

// TestStartDiagnosticsPublishesConventions: an open buffer plus a project
// script fills the cache, with qmllint out of the picture.
func TestStartDiagnosticsPublishesConventions(t *testing.T) {
	proj := t.TempDir()
	writeConventionsScript(t, proj, "#!/bin/sh\ncat > /dev/null\n"+
		`printf '%s' '{"violations": [{"file": "<stdin>", "line": 1, "rule": "import-order", "message": "wrong order"}]}' >&2`+"\n")

	h := New(nil)
	h.qmllint = nil // isolate the conventions producer
	h.conventions.SetRoots([]string{proj})
	uri := lsp.DocumentURI("file://" + filepath.Join(proj, "Foo.qml"))
	h.setDocument(uri, "import qs.config\nimport QtQuick\n")

	h.startDiagnostics(uri)

	deadline := time.Now().Add(5 * time.Second)
	for {
		h.diagMu.Lock()
		got := h.diagBySource[uri][diagSourceConventions]
		h.diagMu.Unlock()
		if len(got) == 1 {
			if got[0].Message != "wrong order" {
				t.Fatalf("Message = %q, want %q", got[0].Message, "wrong order")
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("conventions diagnostics never arrived, got %+v", got)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// lspPipe drives a real handshake against Serve's registration path. The
// servertest harness can't: it builds its own server.Server, so Handler.server
// — what diagnostics publish through — is never set.
type lspPipe struct {
	t    *testing.T
	conn net.Conn
	msgs chan map[string]any
}

func newLSPPipe(t *testing.T, h *Handler) *lspPipe {
	t.Helper()
	clientConn, serverConn := net.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = h.serve(ctx, serverConn) }()
	p := &lspPipe{t: t, conn: clientConn, msgs: make(chan map[string]any, 64)}
	go p.readLoop()
	t.Cleanup(func() {
		cancel()
		_ = clientConn.Close()
	})
	return p
}

func (p *lspPipe) send(msg map[string]any) {
	p.t.Helper()
	body, err := json.Marshal(msg)
	if err != nil {
		p.t.Fatalf("marshal: %v", err)
	}
	if _, err := fmt.Fprintf(p.conn, "Content-Length: %d\r\n\r\n%s", len(body), body); err != nil {
		p.t.Fatalf("write: %v", err)
	}
}

// readLoop parses Content-Length framing until the pipe closes.
func (p *lspPipe) readLoop() {
	r := bufio.NewReader(p.conn)
	for {
		length := 0
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			line = strings.TrimRight(line, "\r\n")
			if line == "" {
				break
			}
			if v, ok := strings.CutPrefix(line, "Content-Length: "); ok {
				length, _ = strconv.Atoi(v)
			}
		}
		if length == 0 {
			continue
		}
		body := make([]byte, length)
		if _, err := io.ReadFull(r, body); err != nil {
			return
		}
		var msg map[string]any
		if err := json.Unmarshal(body, &msg); err != nil {
			return
		}
		p.msgs <- msg
	}
}

// next returns the next message from the server, or fails the test.
func (p *lspPipe) next(timeout time.Duration) map[string]any {
	p.t.Helper()
	select {
	case msg := <-p.msgs:
		return msg
	case <-time.After(timeout):
		p.t.Fatal("timed out waiting for a message from the server")
		return nil
	}
}

// TestDiagnosticsReachClientAfterHandshake is the regression test for a
// server that went silent: publishDiagnostics drops anything produced before
// the initialize response, and go-lsp claims `initialized` itself, so without
// Serve's override the gate never opened. Asserts both halves — the response
// comes first, and diagnostics still arrive.
func TestDiagnosticsReachClientAfterHandshake(t *testing.T) {
	proj := t.TempDir()
	writeConventionsScript(t, proj, "#!/bin/sh\ncat > /dev/null\n"+
		`printf '%s' '{"violations": [{"file": "<stdin>", "line": 1, "rule": "import-order", "message": "wrong order"}]}' >&2`+"\n")

	h := New(nil)
	h.qmllint = nil // isolate the conventions producer
	// Out of reach, so an arriving diagnostic proves the notification was
	// dispatched rather than the fallback papering over it.
	h.initFallback = time.Hour
	p := newLSPPipe(t, h)

	root := "file://" + proj
	p.send(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{
		"rootUri":          root,
		"workspaceFolders": []map[string]any{{"uri": root, "name": "proj"}},
		"capabilities":     map[string]any{},
	}})

	// Nothing may precede the initialize response on the wire.
	first := p.next(10 * time.Second)
	if _, ok := first["result"]; !ok {
		t.Fatalf("first message was %v, want the initialize response", first["method"])
	}

	p.send(map[string]any{"jsonrpc": "2.0", "method": "initialized", "params": map[string]any{}})
	uri := "file://" + filepath.Join(proj, "Foo.qml")
	p.send(map[string]any{"jsonrpc": "2.0", "method": "textDocument/didOpen", "params": map[string]any{
		"textDocument": map[string]any{
			"uri": uri, "languageId": "qml", "version": 1,
			"text": "import qs.config\nimport QtQuick\n",
		},
	}})

	deadline := time.Now().Add(20 * time.Second)
	for {
		msg := p.next(time.Until(deadline))
		if msg["method"] != "textDocument/publishDiagnostics" {
			continue
		}
		params, _ := msg["params"].(map[string]any)
		diags, _ := params["diagnostics"].([]any)
		if len(diags) == 0 {
			continue // a clean producer reporting in; keep waiting for ours
		}
		first, _ := diags[0].(map[string]any)
		if first["source"] != diagSourceConventions || first["message"] != "wrong order" {
			t.Fatalf("diagnostic = %v, want the conventions violation", first)
		}
		return
	}
}
