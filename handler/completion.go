package handler

import (
	"context"

	"github.com/owenrumney/go-lsp/lsp"
)

// Completion delegates to Qt's own qmlls if it's running. We don't offer
// any in-process completions — qmlls is the single source of truth for
// autocomplete so users get industrial-grade results that track Qt's own
// type system instead of our best-effort reconstruction.
func (h *Handler) Completion(ctx context.Context, params *lsp.CompletionParams) (*lsp.CompletionList, error) {
	if h.qmlls == nil {
		return &lsp.CompletionList{Items: []lsp.CompletionItem{}}, nil
	}
	return h.qmlls.Completion(ctx, params)
}
