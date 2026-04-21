package handler

import (
	"strings"

	"github.com/odvcencio/gotreesitter"
	"github.com/owenrumney/go-lsp/lsp"
)

// importedModules walks the document's parse tree for ui_import nodes and
// returns the set of qualified module names it imports (e.g. `QtQuick`,
// `QtQuick.Controls`). Quoted-path imports like `import "./components"` are
// excluded — those resolve to files, not registry modules, and workspace
// completions stay unfiltered.
func (h *Handler) importedModules(uri lsp.DocumentURI) map[string]struct{} {
	set := map[string]struct{}{}
	if h.parser == nil {
		return set
	}
	tree := h.parser.GetTree(uri)
	if tree == nil {
		return set
	}
	root := tree.RootNode()
	if root == nil {
		return set
	}
	doc, ok := h.getDocument(uri)
	if !ok {
		return set
	}
	lang := h.parser.Language()
	content := []byte(doc)
	walkTree(root, func(n *gotreesitter.Node) bool {
		if n.Type(lang) != "ui_import" {
			return true
		}
		source := n.ChildByFieldName("source", lang)
		if source == nil {
			return false
		}
		if source.Type(lang) == "string" {
			return false
		}
		name := strings.TrimSpace(string(content[source.StartByte():source.EndByte()]))
		if name != "" {
			set[name] = struct{}{}
		}
		return false
	})
	return set
}

// importedTypeCompletions returns type-category symbols filtered to the
// provided imported-module set. Types with an empty Module are treated as
// implicit (built-in primitives, workspace stand-ins) and always kept. A
// nil set disables filtering entirely, preserving callers that run before
// imports are known.
func importedTypeCompletions(imported map[string]struct{}) []lsp.CompletionItem {
	syms := symbolsByCategory("type")
	items := make([]lsp.CompletionItem, 0, len(syms))
	for _, s := range syms {
		if imported == nil || s.Module == "" {
			items = append(items, s.CompletionItem())
			continue
		}
		if _, ok := imported[s.Module]; ok {
			items = append(items, s.CompletionItem())
		}
	}
	return items
}
