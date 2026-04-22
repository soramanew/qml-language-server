package handler

import (
	"github.com/odvcencio/gotreesitter"
	"github.com/owenrumney/go-lsp/lsp"
)

// collectDiagnostics walks the tree and reports the innermost ERROR or
// MISSING node in each error subtree. Tree-sitter routinely wraps a small
// syntax slip in a giant outer ERROR that spans the entire file (the
// trailing-`.`-in-a-chain case is the canonical example), so emitting every
// ERROR we see would light the whole document up red on a single mistake.
// Skipping outer nodes that already contain a more precise error keeps
// diagnostics tight to the actual problem.
func collectDiagnostics(node *gotreesitter.Node, lang *gotreesitter.Language, content []byte, diagnostics *[]lsp.Diagnostic) {
	walkTree(node, func(n *gotreesitter.Node) bool {
		if !n.IsError() && !n.IsMissing() {
			return true
		}
		if hasErrorDescendant(n) {
			// Keep descending — the inner ERROR/MISSING is what we want to
			// surface. Suppress this outer wrapper.
			return true
		}
		severity := lsp.SeverityError
		msg := "Syntax error"
		if n.IsMissing() {
			msg = "Missing " + n.Type(lang)
		}
		*diagnostics = append(*diagnostics, lsp.Diagnostic{
			Range:    nodeRange(content, n),
			Severity: &severity,
			Message:  msg,
			Source:   "qml-language-server",
		})
		return true
	})
}

// hasErrorDescendant reports whether any proper descendant of n is an
// ERROR or MISSING node. Used to tell a noisy outer wrapper apart from a
// leaf-level syntax error.
func hasErrorDescendant(n *gotreesitter.Node) bool {
	for i := 0; i < n.ChildCount(); i++ {
		child := n.Child(i)
		if child == nil {
			continue
		}
		if child.IsError() || child.IsMissing() {
			return true
		}
		if hasErrorDescendant(child) {
			return true
		}
	}
	return false
}
