package handler

import (
	"context"
	"testing"

	"github.com/owenrumney/go-lsp/lsp"
)

// TestFormattingReturnsEmptyWithoutQmlformat verifies the handler returns
// an empty edit list (not nil, not an error) when qmlformat isn't on the
// system. The test harness doesn't install it on this path, so h.qmlformat
// is whatever newQmlformatRunner detects at construction time; if
// qmlformat *is* present we skip the assertion.
func TestFormattingReturnsEmptyWithoutQmlformat(t *testing.T) {
	h := newTestHandler(t, "test://fmt.qml", "Rectangle {\nwidth: 100\n}\n")
	if h.qmlformat != nil {
		t.Skip("qmlformat is installed; covered by integration")
	}
	edits, err := h.Formatting(context.Background(), &lsp.DocumentFormattingParams{
		TextDocument: lsp.TextDocumentIdentifier{URI: "test://fmt.qml"},
		Options:      lsp.FormattingOptions{TabSize: 4, InsertSpaces: true},
	})
	if err != nil {
		t.Fatalf("Formatting: %v", err)
	}
	if edits == nil || len(edits) != 0 {
		t.Errorf("expected empty edits when qmlformat missing, got %+v", edits)
	}
}

// TestFormattingRunsQmlformatWhenAvailable exercises the full path when
// qmlformat is installed — the handler should return a single
// whole-document TextEdit whose NewText matches qmlformat's output.
func TestFormattingRunsQmlformatWhenAvailable(t *testing.T) {
	h := newTestHandler(t, "test://fmt.qml", "Rectangle {\nwidth: 100\n}\n")
	if h.qmlformat == nil {
		t.Skip("qmlformat not installed")
	}
	edits, err := h.Formatting(context.Background(), &lsp.DocumentFormattingParams{
		TextDocument: lsp.TextDocumentIdentifier{URI: "test://fmt.qml"},
		Options:      lsp.FormattingOptions{TabSize: 4, InsertSpaces: true},
	})
	if err != nil {
		t.Fatalf("Formatting: %v", err)
	}
	if len(edits) != 1 {
		t.Fatalf("expected one whole-document edit, got %d", len(edits))
	}
	if edits[0].NewText == "" {
		t.Error("expected non-empty formatted text")
	}
	if edits[0].Range.Start.Line != 0 || edits[0].Range.Start.Character != 0 {
		t.Errorf("edit must start at (0,0), got %+v", edits[0].Range.Start)
	}
}

func TestWholeDocumentRangeCountsLinesAndLastLine(t *testing.T) {
	cases := []struct {
		doc         string
		wantLine    int
		wantCharEnd int
	}{
		{"", 0, 0},
		{"A", 0, 1},
		{"A\n", 1, 0},
		{"A\nB", 1, 1},
		{"A\nB\n", 2, 0},
	}
	for _, tc := range cases {
		got := wholeDocumentRange(tc.doc)
		if got.End.Line != tc.wantLine || got.End.Character != tc.wantCharEnd {
			t.Errorf("doc=%q end=%+v, want {%d,%d}", tc.doc, got.End, tc.wantLine, tc.wantCharEnd)
		}
	}
}
