package handler

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/owenrumney/go-lsp/lsp"
)

// writeTrsCheckScript drops an executable stand-in at
// <proj>/scripts/trs-check.py. /bin/sh via a shebang, so these tests don't
// depend on a Python interpreter being installed.
func writeTrsCheckScript(t *testing.T, proj, body string) {
	t.Helper()
	dir := filepath.Join(proj, "scripts")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "trs-check.py"), []byte(body), 0o755); err != nil {
		t.Fatalf("write script: %v", err)
	}
}

// TestTrsCheckRunnerParsesStderrReport: JSON on stderr becomes diagnostics
// anchored at the reported line and column, and the script's two levels map
// onto error and warning severity.
func TestTrsCheckRunnerParsesStderrReport(t *testing.T) {
	proj := t.TempDir()
	argsFile := filepath.Join(proj, "args")
	report := `{"issues": [` +
		`{"file": "Foo.qml", "line": 2, "column": 14, "endLine": 2, "endColumn": 18, "level": "error", "rule": "foreign-helper", "message": "qsTr() is not extracted"},` +
		`{"file": "Foo.qml", "line": 4, "column": 5, "endLine": 4, "endColumn": 13, "level": "warning", "rule": "empty-context", "message": "empty context"}` +
		`]}`
	writeTrsCheckScript(t, proj, checkScript(argsFile, report, 1))

	r := newTrsCheckRunner(nil)
	r.SetRoots([]string{proj})
	path := filepath.Join(proj, "Foo.qml")
	source := "import QtQuick\nText { text: qsTr(\"hi\") }\nItem {\n    id: root\n}\n"
	diags := r.Check(context.Background(), path, source)

	if len(diags) != 2 {
		t.Fatalf("got %d diagnostics, want 2: %+v", len(diags), diags)
	}
	// Exit 1 is normal when the script finds an error; it must not suppress
	// results.
	first := diags[0]
	if first.Message != "qsTr() is not extracted" {
		t.Errorf("Message = %q", first.Message)
	}
	if first.Source != diagSourceTrsCheck {
		t.Errorf("Source = %q, want %q", first.Source, diagSourceTrsCheck)
	}
	if first.Severity == nil || *first.Severity != lsp.SeverityError {
		t.Errorf("Severity = %v, want error", first.Severity)
	}
	if string(first.Code) != `"foreign-helper"` {
		t.Errorf("Code = %s, want \"foreign-helper\"", first.Code)
	}
	// Columns 14..18 on line 2 (1-based, end exclusive) is the `qsTr` the
	// issue is about, i.e. characters 13..17 to LSP.
	want := lsp.Range{
		Start: lsp.Position{Line: 1, Character: 13},
		End:   lsp.Position{Line: 1, Character: 17},
	}
	if first.Range != want {
		t.Errorf("Range = %+v, want %+v", first.Range, want)
	}
	if second := diags[1]; second.Severity == nil || *second.Severity != lsp.SeverityWarning {
		t.Errorf("second Severity = %v, want warning", second.Severity)
	}

	// The buffer goes in on stdin, but the script still needs the real path
	// to tell QML from C++ and to skip the helpers' own sources.
	gotArgs, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatalf("read args: %v", err)
	}
	wantArgs := "--file - --stdin-name " + path + " --json"
	if got := strings.TrimSpace(string(gotArgs)); got != wantArgs {
		t.Errorf("args = %q, want %q", got, wantArgs)
	}
}

// TestTrsCheckRunnerNoScript locks in the best-effort contract: no script in
// the project means no diagnostics and no error.
func TestTrsCheckRunnerNoScript(t *testing.T) {
	proj := t.TempDir()
	r := newTrsCheckRunner(nil)
	r.SetRoots([]string{proj})
	if got := r.Check(context.Background(), filepath.Join(proj, "Foo.qml"), "import QtQuick\n"); got != nil {
		t.Errorf("Check = %+v, want nil", got)
	}
	if got := r.findScript(); got != "" {
		t.Errorf("findScript = %q, want empty", got)
	}
}

// TestTrsCheckRunnerIgnoresNonJSON: a broken script (or one whose report is
// the human-readable form) yields no diagnostics rather than garbage.
func TestTrsCheckRunnerIgnoresNonJSON(t *testing.T) {
	proj := t.TempDir()
	writeTrsCheckScript(t, proj, "#!/bin/sh\ncat > /dev/null\necho 'Checking 1 source file(s)...' >&2\nexit 1\n")
	r := newTrsCheckRunner(nil)
	r.SetRoots([]string{proj})
	if got := r.Check(context.Background(), filepath.Join(proj, "Foo.qml"), "import QtQuick\n"); got != nil {
		t.Errorf("Check = %+v, want nil", got)
	}
}

// TestStartDiagnosticsPublishesTrsCheck: the translation checker is wired
// into the diagnostic round like any other producer, under its own key.
func TestStartDiagnosticsPublishesTrsCheck(t *testing.T) {
	proj := t.TempDir()
	writeTrsCheckScript(t, proj, "#!/bin/sh\ncat > /dev/null\n"+
		`printf '%s' '{"issues": [{"file": "Foo.qml", "line": 1, "level": "error", "rule": "missing-import", "message": "missing import"}]}' >&2`+"\n")

	h := New(nil)
	h.qmllint = nil // isolate the trs-check producer
	h.conventions = nil
	h.trscheck.SetRoots([]string{proj})
	uri := lsp.DocumentURI("file://" + filepath.Join(proj, "Foo.qml"))
	h.setDocument(uri, "Text { text: Tr.tr(\"hi\") }\n")

	h.startDiagnostics(uri)

	deadline := time.Now().Add(5 * time.Second)
	for {
		h.diagMu.Lock()
		got := h.diagBySource[uri][diagSourceTrsCheck]
		h.diagMu.Unlock()
		if len(got) == 1 {
			if got[0].Message != "missing import" {
				t.Fatalf("Message = %q, want %q", got[0].Message, "missing import")
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("trs-check diagnostics never arrived, got %+v", got)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
