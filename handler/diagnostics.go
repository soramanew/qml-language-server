package handler

import (
	"github.com/odvcencio/gotreesitter"
	"github.com/owenrumney/go-lsp/lsp"
)

// maxSyntaxDiagnostics caps how many tree-sitter ERROR/MISSING nodes we
// surface per document. A single real typo routinely cascades through
// tree-sitter's recovery into dozens of sibling ERROR wrappers covering
// every line that follows, which then shows up in the editor as a red
// underline down the whole file. Once the user has three syntax errors
// to look at they've got enough — the rest is almost always noise that
// disappears as soon as the first one is fixed.
const maxSyntaxDiagnostics = 3

// collectDiagnostics walks the tree and reports the innermost ERROR or
// MISSING node in each error subtree, skipping the outer wrappers and the
// single-identifier recovery artifacts tree-sitter injects around the
// broken subtree's leading token. The total count is capped so a single
// typo can't light up the whole document.
func collectDiagnostics(node *gotreesitter.Node, lang *gotreesitter.Language, content []byte, diagnostics *[]lsp.Diagnostic) {
	var real []*gotreesitter.Node
	walkTree(node, func(n *gotreesitter.Node) bool {
		if !n.IsError() && !n.IsMissing() {
			return true
		}
		if hasErrorDescendant(n) {
			return true
		}
		if isRecoveryArtifact(n, lang) {
			return true
		}
		real = append(real, n)
		return true
	})

	if len(real) == 0 {
		// Tree-sitter couldn't produce a precise inner error but the parse is
		// still broken (outer ERROR / unterminated construct). Emit a single
		// point diagnostic at the end of the outermost ERROR so the editor
		// marks the trouble spot — typically right after the trailing `.` or
		// where a brace should have closed — without painting anything red
		// further up the file.
		if outer := firstErrorNode(node); outer != nil {
			*diagnostics = append(*diagnostics, pointDiagnostic(content, outer))
		}
		return
	}

	if len(real) > maxSyntaxDiagnostics {
		real = real[:maxSyntaxDiagnostics]
	}
	severity := lsp.SeverityError
	for _, n := range real {
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
	}
}

// firstErrorNode returns the first ERROR/MISSING node encountered in a
// depth-first walk, or nil if none.
func firstErrorNode(n *gotreesitter.Node) *gotreesitter.Node {
	if n == nil {
		return nil
	}
	if n.IsError() || n.IsMissing() {
		return n
	}
	for i := 0; i < n.ChildCount(); i++ {
		if found := firstErrorNode(n.Child(i)); found != nil {
			return found
		}
	}
	return nil
}

// pointDiagnostic produces a 1-column-wide diagnostic at the END position
// of the given node — close to where the parser gave up, rather than the
// whole broken span.
func pointDiagnostic(content []byte, n *gotreesitter.Node) lsp.Diagnostic {
	end := byteOffsetToPosition(content, n.EndByte())
	start := end
	if start.Character > 0 {
		start.Character--
	}
	severity := lsp.SeverityError
	return lsp.Diagnostic{
		Range:    lsp.Range{Start: start, End: end},
		Severity: &severity,
		Message:  "Syntax error",
		Source:   "qml-language-server",
	}
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

// isRecoveryArtifact filters out ERROR nodes whose only content is a single
// clean identifier. Tree-sitter wraps the leading token of a broken subtree
// in ERROR as part of its recovery — e.g. `Rectangle { broken }` marks
// `Rectangle` itself as an ERROR even though the identifier is fine. The
// real problem is somewhere else in the tree; surfacing this wrapper just
// underlines the wrong word in the editor.
func isRecoveryArtifact(n *gotreesitter.Node, lang *gotreesitter.Language) bool {
	if !n.IsError() || n.ChildCount() != 1 {
		return false
	}
	child := n.Child(0)
	if child == nil {
		return false
	}
	switch child.Type(lang) {
	case "identifier", "nested_identifier", "property_identifier", "type_identifier":
		return true
	}
	return false
}
