package handler

import (
	"context"
	"strings"

	"github.com/owenrumney/go-lsp/lsp"
)

// Formatting takes the current document text, re-indents it based on brace
// depth, trims trailing whitespace from every line, collapses runs of blank
// lines to at most one, and ensures the file ends with a single newline.
// The whole pass is whitespace-only — token content is never modified — so
// even a syntactically broken document gets back something valid for the
// non-broken parts. Emits one TextEdit per differing line instead of a
// whole-file replacement: some LSP clients jump the cursor back to
// position (0, 0) or lose selection state on a full-document edit, and
// per-line edits keep the user's cursor anchored.
func (h *Handler) Formatting(_ context.Context, params *lsp.DocumentFormattingParams) ([]lsp.TextEdit, error) {
	doc, ok := h.getDocument(params.TextDocument.URI)
	if !ok {
		return nil, nil
	}

	formatted := formatQML(doc, params.Options)
	if formatted == doc {
		return []lsp.TextEdit{}, nil
	}
	return perLineEdits(doc, formatted), nil
}

// perLineEdits produces an LSP TextEdit for each line where `original`
// and `formatted` differ. When the line counts disagree (blank-run
// collapse, trailing-newline insertion) we fall back to one
// whole-document replace for the tail so everything is still covered.
func perLineEdits(original, formatted string) []lsp.TextEdit {
	origLines := splitLinesKeepEmpty(original)
	newLines := splitLinesKeepEmpty(formatted)

	var edits []lsp.TextEdit
	common := len(origLines)
	if len(newLines) < common {
		common = len(newLines)
	}
	for i := 0; i < common; i++ {
		if origLines[i] == newLines[i] {
			continue
		}
		edits = append(edits, lsp.TextEdit{
			Range: lsp.Range{
				Start: lsp.Position{Line: i, Character: 0},
				End:   lsp.Position{Line: i, Character: len(origLines[i])},
			},
			NewText: newLines[i],
		})
	}

	// Handle shape changes (added/removed lines) as one tail edit so we
	// don't have to fight the LSP line-delta math.
	if len(origLines) != len(newLines) {
		tailStart := common
		end := lsp.Position{
			Line:      len(origLines) - 1,
			Character: len(origLines[len(origLines)-1]),
		}
		if tailStart >= len(origLines) {
			// Pure append.
			edits = append(edits, lsp.TextEdit{
				Range:   lsp.Range{Start: end, End: end},
				NewText: strings.Join(newLines[tailStart:], "\n"),
			})
		} else {
			edits = append(edits, lsp.TextEdit{
				Range: lsp.Range{
					Start: lsp.Position{Line: tailStart, Character: 0},
					End:   end,
				},
				NewText: strings.Join(newLines[tailStart:], "\n"),
			})
		}
	}
	return edits
}

// splitLinesKeepEmpty is strings.Split(s, "\n") but guarantees at least
// one entry for the empty string (so `perLineEdits` can always index
// `[len-1]`).
func splitLinesKeepEmpty(s string) []string {
	if s == "" {
		return []string{""}
	}
	return strings.Split(s, "\n")
}

func (h *Handler) RangeFormatting(_ context.Context, params *lsp.DocumentRangeFormattingParams) ([]lsp.TextEdit, error) {
	doc, ok := h.getDocument(params.TextDocument.URI)
	if !ok {
		return nil, nil
	}

	lines := getLines(doc)
	startLine := int(params.Range.Start.Line)
	endLine := int(params.Range.End.Line)
	if startLine >= len(lines) {
		return []lsp.TextEdit{}, nil
	}
	if endLine >= len(lines) {
		endLine = len(lines) - 1
	}

	// Extract the selected range, format it, and replace just that span.
	var selected strings.Builder
	for i := startLine; i <= endLine; i++ {
		selected.WriteString(lines[i])
		selected.WriteByte('\n')
	}
	original := selected.String()
	formatted := formatQML(original, params.Options)
	if formatted == original {
		return []lsp.TextEdit{}, nil
	}

	return []lsp.TextEdit{{
		Range: lsp.Range{
			Start: lsp.Position{Line: params.Range.Start.Line, Character: 0},
			End:   lsp.Position{Line: endLine + 1, Character: 0},
		},
		NewText: formatted,
	}}, nil
}

// formatQML applies the indentation/whitespace rules. It walks the document
// character-by-character to track string/comment state so braces inside those
// don't move the indentation depth.
func formatQML(text string, opts lsp.FormattingOptions) string {
	indentUnit := indentUnitFrom(opts)
	lines := strings.Split(text, "\n")

	type lineInfo struct {
		text       string
		indent     int  // depth this line is rendered at
		blank      bool // true after trim-and-strip
	}
	infos := make([]lineInfo, 0, len(lines))

	depth := 0
	state := scanState{}
	for _, raw := range lines {
		stripped := strings.TrimSpace(raw)
		// Compute the leading-brace adjustment first so a line that *starts*
		// with `}` is rendered one level shallower than its content depth.
		leadingClose := countLeadingClose(stripped)
		renderDepth := max(depth-leadingClose, 0)
		infos = append(infos, lineInfo{text: stripped, indent: renderDepth, blank: stripped == ""})

		// Then advance the state for whatever this line contributes to depth.
		depth = max(scanLineForBraces(raw, &state, depth), 0)
	}

	var b strings.Builder
	prevBlank := false
	for i, info := range infos {
		if info.blank {
			// Collapse runs of blanks to a single blank line; never start the
			// file with a blank line.
			if prevBlank || i == 0 {
				continue
			}
			b.WriteByte('\n')
			prevBlank = true
			continue
		}
		for j := 0; j < info.indent; j++ {
			b.WriteString(indentUnit)
		}
		b.WriteString(info.text)
		b.WriteByte('\n')
		prevBlank = false
	}

	out := b.String()
	// Trim any trailing extra blank lines (we may have written a blank from a
	// mid-document blank that turned out to be the last meaningful line).
	out = strings.TrimRight(out, "\n") + "\n"
	if strings.TrimSpace(out) == "" {
		return ""
	}
	return out
}

func indentUnitFrom(opts lsp.FormattingOptions) string {
	if !opts.InsertSpaces {
		return "\t"
	}
	size := opts.TabSize
	if size <= 0 {
		size = 4
	}
	return strings.Repeat(" ", size)
}

// countLeadingClose returns how many `}`, `)`, or `]` characters lead
// this trimmed line. Used so that closing brackets render at the parent's
// indentation level.
func countLeadingClose(s string) int {
	count := 0
	for _, r := range s {
		switch r {
		case '}', ')', ']':
			count++
		default:
			return count
		}
	}
	return count
}

// scanState tracks whether the cursor is inside a string or block comment so
// that braces in those contexts don't change indentation depth.
type scanState struct {
	inLineComment  bool
	inBlockComment bool
	inString       byte // 0 if not in string; otherwise the opening quote byte
	escape         bool
}

func scanLineForBraces(line string, st *scanState, depth int) int {
	st.inLineComment = false // line comments end at end of line
	for i := 0; i < len(line); i++ {
		c := line[i]
		if st.escape {
			st.escape = false
			continue
		}
		if st.inBlockComment {
			if c == '*' && i+1 < len(line) && line[i+1] == '/' {
				st.inBlockComment = false
				i++
			}
			continue
		}
		if st.inLineComment {
			continue
		}
		if st.inString != 0 {
			switch c {
			case '\\':
				st.escape = true
			case st.inString:
				st.inString = 0
			}
			continue
		}
		switch c {
		case '/':
			if i+1 < len(line) {
				switch line[i+1] {
				case '/':
					st.inLineComment = true
					i++
					continue
				case '*':
					st.inBlockComment = true
					i++
					continue
				}
			}
		case '"', '\'', '`':
			st.inString = c
		case '{', '(', '[':
			depth++
		case '}', ')', ']':
			depth--
		}
	}
	return depth
}
