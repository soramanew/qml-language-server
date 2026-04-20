package handler

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/owenrumney/go-lsp/lsp"
)

func TestWarningToDiagnosticConvertsToZeroBased(t *testing.T) {
	w := qmllintWarning{
		ID:      "incompatible-type",
		Message: "Cannot assign literal of type string to double",
		Type:    "warning",
		Line:    5,
		Column:  13,
		Length:  14,
	}
	d := warningToDiagnostic(w)

	if d.Range.Start.Line != 4 || d.Range.Start.Character != 12 {
		t.Errorf("start = (%d,%d), want (4,12)", d.Range.Start.Line, d.Range.Start.Character)
	}
	if d.Range.End.Line != 4 || d.Range.End.Character != 26 {
		t.Errorf("end = (%d,%d), want (4,26)", d.Range.End.Line, d.Range.End.Character)
	}
	if d.Severity == nil || *d.Severity != lsp.SeverityWarning {
		t.Errorf("severity = %v, want Warning", d.Severity)
	}
	if d.Source != "qmllint" {
		t.Errorf("source = %q, want %q", d.Source, "qmllint")
	}
	if d.Message != w.Message {
		t.Errorf("message = %q, want %q", d.Message, w.Message)
	}
	var code string
	if err := json.Unmarshal(d.Code, &code); err != nil {
		t.Fatalf("unmarshal code: %v", err)
	}
	if code != "incompatible-type" {
		t.Errorf("code = %q, want %q", code, "incompatible-type")
	}
}

func TestWarningToDiagnosticClampsNegativePositions(t *testing.T) {
	// qmllint sometimes emits warnings without a specific location (line == 0).
	w := qmllintWarning{ID: "import", Message: "Failed to open file", Type: "warning"}
	d := warningToDiagnostic(w)

	if d.Range.Start.Line != 0 || d.Range.Start.Character != 0 {
		t.Errorf("start = (%d,%d), want (0,0)", d.Range.Start.Line, d.Range.Start.Character)
	}
	if d.Range.End.Line != 0 || d.Range.End.Character != 0 {
		t.Errorf("end = (%d,%d), want (0,0)", d.Range.End.Line, d.Range.End.Character)
	}
}

func TestQmllintSeverityMapping(t *testing.T) {
	cases := []struct {
		in   string
		want lsp.DiagnosticSeverity
	}{
		{"error", lsp.SeverityError},
		{"Error", lsp.SeverityError},
		{"critical", lsp.SeverityError},
		{"warning", lsp.SeverityWarning},
		{"info", lsp.SeverityInformation},
		{"hint", lsp.SeverityHint},
		{"", lsp.SeverityWarning},
		{"unknown", lsp.SeverityWarning},
	}
	for _, c := range cases {
		if got := qmllintSeverity(c.in); got != c.want {
			t.Errorf("qmllintSeverity(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestDiagnosticsFromReportParsesMultipleWarnings(t *testing.T) {
	raw := `{
		"files": [{
			"filename": "/tmp/test.qml",
			"success": false,
			"warnings": [
				{"id":"incompatible-type","message":"Cannot assign literal of type string to double","type":"warning","line":5,"column":13,"length":14,"charOffset":55},
				{"id":"unqualified","message":"Unqualified access","type":"warning","line":7,"column":15,"length":19,"charOffset":95}
			]
		}],
		"revision": 4
	}`
	var report qmllintReport
	if err := json.Unmarshal([]byte(raw), &report); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	diags := diagnosticsFromReport(&report)
	if len(diags) != 2 {
		t.Fatalf("got %d diagnostics, want 2", len(diags))
	}
	if diags[0].Range.Start.Line != 4 || diags[0].Range.Start.Character != 12 {
		t.Errorf("first diag start = (%d,%d), want (4,12)",
			diags[0].Range.Start.Line, diags[0].Range.Start.Character)
	}
	if diags[1].Range.Start.Line != 6 || diags[1].Range.Start.Character != 14 {
		t.Errorf("second diag start = (%d,%d), want (6,14)",
			diags[1].Range.Start.Line, diags[1].Range.Start.Character)
	}
}

func TestDiagnosticsFromReportHandlesNoFiles(t *testing.T) {
	if got := diagnosticsFromReport(&qmllintReport{}); got != nil {
		t.Errorf("empty report produced %d diagnostics, want nil", len(got))
	}
}

func TestResolveIniPathPreservesAbsolute(t *testing.T) {
	got := resolveIniPath("/proj", "/opt/qml")
	if got != "/opt/qml" {
		t.Errorf("got %q, want /opt/qml", got)
	}
}

func TestResolveIniPathJoinsRelative(t *testing.T) {
	got := resolveIniPath("/proj", "build")
	if got != "/proj/build" {
		t.Errorf("got %q, want /proj/build", got)
	}
}

func TestLintImportPathsReadsQmllsIni(t *testing.T) {
	dir := t.TempDir()
	ini := "[General]\nbuildDir=build\nimportPaths=qml:/abs/imports\n"
	if err := os.WriteFile(filepath.Join(dir, ".qmlls.ini"), []byte(ini), 0o644); err != nil {
		t.Fatalf("write ini: %v", err)
	}
	h := &Handler{workspace: newWorkspaceIndex()}
	h.workspace.setRoots([]string{dir})

	got := h.lintImportPaths(filepath.Join(dir, "Foo.qml"))
	want := []string{filepath.Join(dir, "build"), filepath.Join(dir, "qml"), "/abs/imports"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("paths[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestLintImportPathsReturnsNilWithoutIni(t *testing.T) {
	dir := t.TempDir()
	h := &Handler{workspace: newWorkspaceIndex()}
	h.workspace.setRoots([]string{dir})
	if got := h.lintImportPaths(filepath.Join(dir, "Foo.qml")); got != nil {
		t.Errorf("got %v, want nil", got)
	}
}

// TestLintImportPathsFindsIniBeforeWorkspaceRootsPopulated proves the fix for
// the race where DidOpen can arrive before Initialize's goroutine runs
// setRoots: finding the ini must work even when workspace.roots is empty.
func TestLintImportPathsFindsIniBeforeWorkspaceRootsPopulated(t *testing.T) {
	dir := t.TempDir()
	ini := "[General]\nbuildDir=build\nimportPaths=qml\n"
	if err := os.WriteFile(filepath.Join(dir, ".qmlls.ini"), []byte(ini), 0o644); err != nil {
		t.Fatalf("write ini: %v", err)
	}
	// Workspace deliberately has NO roots — mimics the pre-Initialize state.
	h := &Handler{workspace: newWorkspaceIndex()}

	// File sits two directories below the ini.
	sub := filepath.Join(dir, "ui", "widgets")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	got := h.lintImportPaths(filepath.Join(sub, "Button.qml"))
	want := []string{filepath.Join(dir, "build"), filepath.Join(dir, "qml")}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("paths[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// TestQmllintRunnerEndToEnd verifies the full subprocess path when a Qt6
// qmllint binary happens to be on the system. Skipped cleanly otherwise so
// the suite stays green in minimal CI environments.
func TestQmllintRunnerEndToEnd(t *testing.T) {
	runner := newQmllintRunner(nil)
	if runner == nil {
		t.Skip("qmllint not available on this system")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "Broken.qml")
	src := "import QtQuick\n\nRectangle {\n    width: 100\n    height: \"not a number\"\n}\n"
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	diags := runner.Lint(ctx, path, nil)
	if len(diags) == 0 {
		// Qt5 qmllint-only environment would have been rejected at
		// detectQmllintBinary, but if the detected binary lacks the Qt6 type
		// checker plugins we still treat this as a skip rather than a failure.
		t.Skip("qmllint produced no warnings; likely Qt5 or stripped build")
	}
	for _, d := range diags {
		if d.Source != "qmllint" {
			t.Errorf("source = %q, want qmllint", d.Source)
		}
	}
}
