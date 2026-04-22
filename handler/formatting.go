package handler

import (
	"context"

	"github.com/owenrumney/go-lsp/lsp"
)

// Formatting proxies to Qt's qmlformat. We never format in-process —
// qmlformat is the single source of truth so the output matches
// `qmlformat` on the command line exactly. When qmlformat isn't installed
// the capability is omitted from the Initialize response and the handler
// returns no edits.
func (h *Handler) Formatting(ctx context.Context, params *lsp.DocumentFormattingParams) ([]lsp.TextEdit, error) {
	doc, ok := h.getDocument(params.TextDocument.URI)
	if !ok {
		return nil, nil
	}
	if h.qmlformat == nil {
		return []lsp.TextEdit{}, nil
	}
	formatted, err := h.qmlformat.Format(ctx, doc)
	if err != nil || formatted == "" || formatted == doc {
		return []lsp.TextEdit{}, nil
	}
	return []lsp.TextEdit{{
		Range:   wholeDocumentRange(doc),
		NewText: formatted,
	}}, nil
}

// RangeFormatting isn't something qmlformat supports directly. We format
// the whole document and hand back the single replacement — Qt's
// qmlformat doesn't know how to operate on a partial range, and trying
// to stitch one together from qmlformat'd fragments would almost
// certainly desync the indent level.
func (h *Handler) RangeFormatting(ctx context.Context, params *lsp.DocumentRangeFormattingParams) ([]lsp.TextEdit, error) {
	return h.Formatting(ctx, &lsp.DocumentFormattingParams{
		TextDocument: params.TextDocument,
		Options:      params.Options,
	})
}

// wholeDocumentRange returns a range covering every character in doc.
// The end position is at the end of the last line (not beyond it), which
// every LSP client we've seen accepts without issue.
func wholeDocumentRange(doc string) lsp.Range {
	lastLine := 0
	lastLineLen := 0
	for i := 0; i < len(doc); i++ {
		if doc[i] == '\n' {
			lastLine++
			lastLineLen = 0
		} else {
			lastLineLen++
		}
	}
	return lsp.Range{
		Start: lsp.Position{Line: 0, Character: 0},
		End:   lsp.Position{Line: lastLine, Character: lastLineLen},
	}
}
