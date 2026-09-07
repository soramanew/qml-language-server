package handler

import (
	"strings"
	"testing"

	"github.com/owenrumney/go-lsp/lsp"
)

// TestReportedRangeClamps: both scripts now report a real range, so the
// conversion is a straight 1-based-to-0-based shift. What's left to pin down
// is the defence against a report that raced an edit.
func TestReportedRangeClamps(t *testing.T) {
	// Lines: 0 "import QtQuick" (14), 1 "Text { text: qsTr(\"hi\") }" (25),
	// 2 "" (0).
	lines := strings.Split("import QtQuick\nText { text: qsTr(\"hi\") }\n", "\n")
	tests := []struct {
		name string
		in   scriptRange
		want lsp.Range
	}{{
		name: "a range the buffer has is shifted, not clamped",
		in:   scriptRange{Line: 2, Column: 14, EndLine: 2, EndColumn: 18},
		want: lsp.Range{Start: lsp.Position{Line: 1, Character: 13}, End: lsp.Position{Line: 1, Character: 17}},
	}, {
		name: "spanning lines is preserved",
		in:   scriptRange{Line: 1, Column: 1, EndLine: 2, EndColumn: 5},
		want: lsp.Range{Start: lsp.Position{Line: 0, Character: 0}, End: lsp.Position{Line: 1, Character: 4}},
	}, {
		name: "line 0 pins to the first line",
		in:   scriptRange{Line: 0, Column: 1, EndLine: 0, EndColumn: 15},
		want: lsp.Range{Start: lsp.Position{Line: 0, Character: 0}, End: lsp.Position{Line: 0, Character: 14}},
	}, {
		name: "line past the end pins to the first line",
		in:   scriptRange{Line: 99, Column: 1, EndLine: 99, EndColumn: 15},
		want: lsp.Range{Start: lsp.Position{Line: 0, Character: 0}, End: lsp.Position{Line: 0, Character: 14}},
	}, {
		name: "columns past the end of a shrunken line clamp to it",
		in:   scriptRange{Line: 1, Column: 99, EndLine: 1, EndColumn: 120},
		want: lsp.Range{Start: lsp.Position{Line: 0, Character: 14}, End: lsp.Position{Line: 0, Character: 14}},
	}, {
		name: "an inverted range widens to the end of the line",
		in:   scriptRange{Line: 1, Column: 8, EndLine: 1, EndColumn: 2},
		want: lsp.Range{Start: lsp.Position{Line: 0, Character: 7}, End: lsp.Position{Line: 0, Character: 14}},
	}, {
		name: "a missing range is the whole first line",
		in:   scriptRange{},
		want: lsp.Range{Start: lsp.Position{Line: 0, Character: 0}, End: lsp.Position{Line: 0, Character: 14}},
	}, {
		name: "an empty line stays empty",
		in:   scriptRange{Line: 3, Column: 1, EndLine: 3, EndColumn: 1},
		want: lsp.Range{Start: lsp.Position{Line: 2, Character: 0}, End: lsp.Position{Line: 2, Character: 0}},
	}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := reportedRange(lines, tt.in); got != tt.want {
				t.Errorf("reportedRange(%+v) = %+v, want %+v", tt.in, got, tt.want)
			}
		})
	}
}
