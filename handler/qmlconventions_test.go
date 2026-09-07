package handler

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/owenrumney/go-lsp/lsp"
)

// writeConventionsScript drops an executable shell script at
// <proj>/scripts/qml-lint-conventions.py. We use /bin/sh via a shebang
// rather than real Python so these tests don't depend on a Python
// interpreter being installed.
func writeConventionsScript(t *testing.T, proj, body string) {
	t.Helper()
	dir := filepath.Join(proj, "scripts")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "qml-lint-conventions.py"), []byte(body), 0o755); err != nil {
		t.Fatalf("write script: %v", err)
	}
}

// TestConventionsRunnerRewritesProjectQmlFiles verifies the post-save flow:
// the script runs with cwd=project root and is free to edit every *.qml in
// place. We confirm that by dropping two QML files in the project and
// checking both end up modified.
func TestConventionsRunnerRewritesProjectQmlFiles(t *testing.T) {
	proj := t.TempDir()
	writeConventionsScript(t, proj,
		"#!/bin/sh\nif [ \"$1\" = \"--fix\" ]; then for f in *.qml; do [ -e \"$f\" ] || continue; printf '// fixed\\n' >> \"$f\"; done; fi\n")

	files := []string{"Foo.qml", "Bar.qml"}
	for _, name := range files {
		if err := os.WriteFile(filepath.Join(proj, name), []byte("// before\n"), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	c := newConventionsRunner(nil)
	c.SetRoots([]string{proj})
	c.Run(context.Background())

	for _, name := range files {
		got, err := os.ReadFile(filepath.Join(proj, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if string(got) != "// before\n// fixed\n" {
			t.Errorf("%s = %q, want script-applied edits", name, string(got))
		}
	}
}

// TestConventionsRunnerNoOpWithoutScript locks in the "best effort"
// contract: no script in the project, Run is a silent no-op.
func TestConventionsRunnerNoOpWithoutScript(t *testing.T) {
	proj := t.TempDir()
	c := newConventionsRunner(nil)
	c.SetRoots([]string{proj})
	c.Run(context.Background()) // must not panic or error
}

// TestConventionsRunnerSwallowsFailures proves a broken conventions script
// does not bubble up — Run returns normally so the save response isn't
// held up.
func TestConventionsRunnerSwallowsFailures(t *testing.T) {
	proj := t.TempDir()
	writeConventionsScript(t, proj, "#!/bin/sh\nexit 1\n")
	c := newConventionsRunner(nil)
	c.SetRoots([]string{proj})
	c.Run(context.Background())
}

// TestConventionsRunnerFindScriptReturnsEmptyWhenMissing is a direct unit
// test on the lookup helper — makes sure we don't false-positive on, say,
// a scripts/ directory that lacks the expected file.
func TestConventionsRunnerFindScriptReturnsEmptyWhenMissing(t *testing.T) {
	proj := t.TempDir()
	if err := os.MkdirAll(filepath.Join(proj, "scripts"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	c := newConventionsRunner(nil)
	c.SetRoots([]string{proj})
	if got := c.findScript(); got != "" {
		t.Errorf("findScript = %q, want empty", got)
	}
}

// checkScript is a fake conventions script: it drains stdin (so our stdin
// copy never sees EPIPE), records its arguments, and writes `report` to
// stderr, where the real script puts it.
func checkScript(argsFile, report string, exit int) string {
	return "#!/bin/sh\n" +
		"printf '%s\\n' \"$*\" > " + argsFile + "\n" +
		"cat > /dev/null\n" +
		"printf '%s' '" + report + "' >&2\n" +
		"exit " + strconv.Itoa(exit) + "\n"
}

// TestConventionsRunnerCheckParsesStderrReport: JSON on stderr becomes
// diagnostics, spanning the whole line since the script reports no column.
func TestConventionsRunnerCheckParsesStderrReport(t *testing.T) {
	proj := t.TempDir()
	argsFile := filepath.Join(proj, "args")
	report := `{"violations": [` +
		`{"file": "<stdin>", "line": 2, "column": 1, "endLine": 2, "endColumn": 17, "rule": "import-order", "message": "wrong order"},` +
		`{"file": "<stdin>", "line": 4, "column": 5, "endLine": 4, "endColumn": 13, "rule": "section-order", "message": "wrong section"}` +
		`]}`
	writeConventionsScript(t, proj, checkScript(argsFile, report, 1))

	c := newConventionsRunner(nil)
	c.SetRoots([]string{proj})
	source := "import QtQuick\nimport qs.config\nItem {\n    id: root\n}\n"
	diags := c.Check(context.Background(), source)

	if len(diags) != 2 {
		t.Fatalf("got %d diagnostics, want 2: %+v", len(diags), diags)
	}
	// Exit 1 is normal on any finding; it must not suppress results.
	first := diags[0]
	if first.Message != "wrong order" {
		t.Errorf("Message = %q, want %q", first.Message, "wrong order")
	}
	if first.Source != diagSourceConventions {
		t.Errorf("Source = %q, want %q", first.Source, diagSourceConventions)
	}
	if first.Severity == nil || *first.Severity != lsp.SeverityWarning {
		t.Errorf("Severity = %v, want warning", first.Severity)
	}
	if string(first.Code) != `"import-order"` {
		t.Errorf("Code = %s, want \"import-order\"", first.Code)
	}
	// Line 2 (1-based) is `import qs.config`, 16 characters wide. The
	// script's end column is exclusive, so 1..17 is the whole line.
	want := lsp.Range{
		Start: lsp.Position{Line: 1, Character: 0},
		End:   lsp.Position{Line: 1, Character: 16},
	}
	if first.Range != want {
		t.Errorf("Range = %+v, want %+v", first.Range, want)
	}
	// The script spans the line's content, so the range skips the indent.
	wantSecond := lsp.Range{
		Start: lsp.Position{Line: 3, Character: 4},
		End:   lsp.Position{Line: 3, Character: 12},
	}
	if diags[1].Range != wantSecond {
		t.Errorf("second Range = %+v, want %+v", diags[1].Range, wantSecond)
	}

	// Unfixed: --fix reports against the source it rewrote, whose lines
	// don't match the editor's buffer.
	gotArgs, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatalf("read args: %v", err)
	}
	if strings.TrimSpace(string(gotArgs)) != "--file - --json" {
		t.Errorf("args = %q, want %q", strings.TrimSpace(string(gotArgs)), "--file - --json")
	}
}

// TestConventionsRunnerCheckPipesBufferOnStdin: the unsaved buffer reaches
// the script, rather than it re-reading the file on disk.
func TestConventionsRunnerCheckPipesBufferOnStdin(t *testing.T) {
	proj := t.TempDir()
	stdinFile := filepath.Join(proj, "stdin")
	writeConventionsScript(t, proj,
		"#!/bin/sh\ncat > "+stdinFile+"\nprintf '%s' '{\"violations\": []}' >&2\n")

	c := newConventionsRunner(nil)
	c.SetRoots([]string{proj})
	source := "// unsaved edit\nItem {}\n"
	if diags := c.Check(context.Background(), source); diags != nil {
		t.Errorf("Check = %+v, want nil for an empty report", diags)
	}

	got, err := os.ReadFile(stdinFile)
	if err != nil {
		t.Fatalf("read stdin capture: %v", err)
	}
	if string(got) != source {
		t.Errorf("script stdin = %q, want %q", string(got), source)
	}
}

// TestConventionsRunnerCheckToleratesJunk: an unexpected script yields no
// diagnostics rather than an error.
func TestConventionsRunnerCheckToleratesJunk(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{"not json", "#!/bin/sh\ncat > /dev/null\necho 'boom: not json' >&2\nexit 2\n"},
		{"silent", "#!/bin/sh\ncat > /dev/null\n"},
		{"crashes", "#!/bin/sh\nexit 127\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			proj := t.TempDir()
			writeConventionsScript(t, proj, tt.body)
			c := newConventionsRunner(nil)
			c.SetRoots([]string{proj})
			if diags := c.Check(context.Background(), "Item {}\n"); diags != nil {
				t.Errorf("Check = %+v, want nil", diags)
			}
		})
	}
}

// TestConventionsRunnerCheckNoScript: no script, no work — the common case.
func TestConventionsRunnerCheckNoScript(t *testing.T) {
	c := newConventionsRunner(nil)
	c.SetRoots([]string{t.TempDir()})
	if diags := c.Check(context.Background(), "Item {}\n"); diags != nil {
		t.Errorf("Check = %+v, want nil", diags)
	}
}

// TestConventionsDiagnosticsClampsLines: lines the buffer doesn't have — 0,
// or past the end — pin to line 0 rather than being dropped.
func TestConventionsDiagnosticsClampsLines(t *testing.T) {
	source := "Item {\n}\n"
	violations := []conventionsViolation{
		{scriptRange: scriptRange{Line: 0, Column: 1, EndLine: 0, EndColumn: 7},
			Rule: "file-structure", Message: "whole file"},
		{scriptRange: scriptRange{Line: 99, Column: 1, EndLine: 99, EndColumn: 7},
			Rule: "section-order", Message: "past the end"},
	}
	diags := conventionsDiagnostics(violations, source)
	if len(diags) != 2 {
		t.Fatalf("got %d diagnostics, want 2", len(diags))
	}
	for i, d := range diags {
		if d.Range.Start.Line != 0 {
			t.Errorf("diags[%d] line = %d, want 0", i, d.Range.Start.Line)
		}
		if d.Range.End.Character != len("Item {") {
			t.Errorf("diags[%d] end char = %d, want %d", i, d.Range.End.Character, len("Item {"))
		}
	}
}
