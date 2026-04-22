package handler

import (
	"context"
	"testing"

	"github.com/owenrumney/go-lsp/lsp"
)

func TestDiagnosticsCleanDocumentReturnsEmpty(t *testing.T) {
	doc := "import QtQuick\n\nRectangle {\n    width: 100\n}\n"
	h := newTestHandler(t, "test://clean.qml", doc)

	report, err := h.DocumentDiagnostic(context.Background(), &lsp.DocumentDiagnosticParams{
		TextDocument: lsp.TextDocumentIdentifier{URI: "test://clean.qml"},
	})
	if err != nil {
		t.Fatalf("DocumentDiagnostic: %v", err)
	}
	full, ok := report.(lsp.FullDocumentDiagnosticReport)
	if !ok {
		t.Fatalf("expected FullDocumentDiagnosticReport, got %T", report)
	}
	if full.Items == nil {
		t.Error("Items must be normalized to [], not nil")
	}
	if len(full.Items) != 0 {
		t.Errorf("expected no diagnostics on clean doc, got %d", len(full.Items))
	}
}

// TestDiagnosticsSuppressesTreeSitterErrors locks in that we no longer emit
// tree-sitter ERROR/MISSING diagnostics. One typo used to cascade into dozens
// of sibling errors that filled the whole editor with red underlines, and
// every heuristic that tried to collapse them back into a single useful
// diagnostic leaked edge cases. qmllint (when installed) provides far better
// syntax feedback; tree-sitter stays silent.
func TestDiagnosticsSuppressesTreeSitterErrors(t *testing.T) {
	cases := []string{
		"import QtQuick\n\nRectangle {\n    width: 100\n",                          // missing close
		"import QtQuick\n\nRectangle {\n    color:\n    width: 100\n}\n",            // half binding
		"import QtQuick\n\nRectangle {\n    id: r\n    @broken@\n    width: 1\n}\n", // junk mid-file
		"import QtQuick\n\nRectangle {\n    width: 100\n    border.\n}\n",           // trailing dot cascade
	}
	for i, doc := range cases {
		uri := lsp.DocumentURI("test://suppressed.qml")
		h := newTestHandler(t, uri, doc)
		rep, err := h.DocumentDiagnostic(context.Background(), &lsp.DocumentDiagnosticParams{
			TextDocument: lsp.TextDocumentIdentifier{URI: uri},
		})
		if err != nil {
			t.Fatalf("case %d: DocumentDiagnostic: %v", i, err)
		}
		full := rep.(lsp.FullDocumentDiagnosticReport)
		if len(full.Items) != 0 {
			t.Errorf("case %d: expected zero diagnostics, got %d: %+v", i, len(full.Items), full.Items)
		}
	}
}
