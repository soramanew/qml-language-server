package handler

import (
	"github.com/odvcencio/gotreesitter"
	"github.com/owenrumney/go-lsp/lsp"
)

// collectDiagnostics is intentionally a no-op. Tree-sitter's error recovery
// cascades a single typo into dozens of sibling ERROR nodes that cover every
// trailing construct, and no amount of filtering reliably collapses those
// back into "one error at the typo" without occasionally erasing the only
// signal the user has. qmllint (when installed) produces far better
// diagnostics and runs asynchronously on save; we rely on it for syntax
// feedback and stay silent ourselves so the editor doesn't fill with red
// underlines while the user is mid-keystroke.
func collectDiagnostics(_ *gotreesitter.Node, _ *gotreesitter.Language, _ []byte, _ *[]lsp.Diagnostic) {
}
