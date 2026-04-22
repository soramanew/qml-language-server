package handler

import (
	"github.com/odvcencio/gotreesitter"
)

// idScope tracks everything we know about the object referenced by a file-
// scoped id: its declared type (for Qt-registered property lookup) and the
// user-declared properties on that specific object (`property Item target`,
// `property string greeting`, ...), so chain completion can resolve through
// them even though they live outside the Qt type registry.
type idScope struct {
	TypeName  string
	UserProps map[string]string // prop name → declared type
}

// buildIDTypeIndex walks the tree and returns a map from `id` name to the
// enclosing object's type name. QML ids are file-scoped, so a single map per
// document is sufficient for property resolution on member expressions like
// `root.width`.
//
// Duplicate ids (which QML forbids) resolve first-writer-wins — whichever the
// walk sees first. This is consistent with how the existing id → location
// lookup in definition.go behaves.
func buildIDTypeIndex(root *gotreesitter.Node, lang *gotreesitter.Language, content []byte) map[string]string {
	scopes := buildIDScopeIndex(root, lang, content)
	index := make(map[string]string, len(scopes))
	for id, sc := range scopes {
		index[id] = sc.TypeName
	}
	return index
}

// buildIDScopeIndex is the richer version: for every id binding it resolves
// the enclosing object's type AND collects its ui_property declarations.
// Used by chain completion so `root.myTarget.width` resolves through a
// user-declared `property Item myTarget` even though myTarget isn't in any
// qmltypes file.
func buildIDScopeIndex(root *gotreesitter.Node, lang *gotreesitter.Language, content []byte) map[string]idScope {
	scopes := map[string]idScope{}
	walkTree(root, func(n *gotreesitter.Node) bool {
		if n.Type(lang) != "ui_binding" {
			return true
		}
		if bindingName(n, lang, content) != "id" {
			return true
		}
		idName := bindingValueIdentifier(n, lang, content)
		if idName == "" {
			return true
		}
		obj := enclosingObjectDefinition(n, lang)
		var typeName string
		if obj != nil {
			typeName = objectDefinitionTypeName(obj, lang, content)
		}
		// Tree-sitter often unwinds to ERROR while the user is mid-edit (a
		// trailing `.` in a chain completion is the canonical trigger), so
		// keep the text-based fallback the plain id → type index relied on.
		if typeName == "" {
			typeName = enclosingTypeFromText(content, n.StartByte())
		}
		if typeName == "" {
			return true
		}
		// Collect `ui_property` siblings of the id binding. When the parse
		// is clean the parent is `ui_object_initializer`; when it's broken
		// (trailing `.` mid-edit) tree-sitter substitutes
		// `ui_object_initializer_repeat*` but still groups the bindings and
		// property declarations under a single parent, so walking siblings
		// works in both cases.
		userProps := collectSiblingUserProperties(n, lang, content)
		if _, exists := scopes[idName]; exists {
			return true
		}
		scopes[idName] = idScope{
			TypeName:  typeName,
			UserProps: userProps,
		}
		return true
	})
	return scopes
}

// enclosingObjectDefinition walks up from a node until it finds the
// `ui_object_definition` that encloses it, or returns nil.
func enclosingObjectDefinition(n *gotreesitter.Node, lang *gotreesitter.Language) *gotreesitter.Node {
	for anc := n.Parent(); anc != nil; anc = anc.Parent() {
		if anc.Type(lang) == "ui_object_definition" {
			return anc
		}
	}
	return nil
}

// collectSiblingUserProperties scans the children of the binding node's
// parent for `ui_property` declarations. Using the parent-of-binding rather
// than the `ui_object_definition` ancestor keeps this working when
// tree-sitter has degenerated to `ui_object_initializer_repeat*` during a
// partial parse.
func collectSiblingUserProperties(binding *gotreesitter.Node, lang *gotreesitter.Language, content []byte) map[string]string {
	parent := binding.Parent()
	if parent == nil {
		return nil
	}
	props := map[string]string{}
	for i := 0; i < parent.ChildCount(); i++ {
		child := parent.Child(i)
		if child == nil || child.Type(lang) != "ui_property" {
			continue
		}
		name, declType := parseUserProperty(child, lang, content)
		if name == "" || declType == "" {
			continue
		}
		if _, exists := props[name]; exists {
			continue
		}
		props[name] = declType
	}
	if len(props) == 0 {
		return nil
	}
	return props
}

// parseUserProperty pulls `(name, declaredType)` out of a `ui_property` node.
// The grammar places the type (a `type_identifier` or `ui_list_property_type`)
// as the first non-keyword child and the `identifier` name right after it.
func parseUserProperty(node *gotreesitter.Node, lang *gotreesitter.Language, content []byte) (string, string) {
	var declType, name string
	sawType := false
	for i := 0; i < node.ChildCount(); i++ {
		c := node.Child(i)
		if c == nil {
			continue
		}
		switch c.Type(lang) {
		case "type_identifier":
			if !sawType {
				declType = string(content[c.StartByte():c.EndByte()])
				sawType = true
			}
		case "ui_list_property_type":
			if !sawType {
				declType = "list"
				sawType = true
			}
		case "identifier":
			if sawType && name == "" {
				name = string(content[c.StartByte():c.EndByte()])
			}
		}
	}
	return name, declType
}

// objectDefinitionTypeName returns the type name of a ui_object_definition
// node (e.g. "Rectangle" for `Rectangle { ... }`). Handles the dotted form
// `QtQuick.Window` by returning the last segment.
func objectDefinitionTypeName(obj *gotreesitter.Node, lang *gotreesitter.Language, content []byte) string {
	for i := 0; i < obj.ChildCount(); i++ {
		c := obj.Child(i)
		if c == nil {
			continue
		}
		t := c.Type(lang)
		if t == "identifier" || t == "nested_identifier" {
			return lastDottedSegment(string(content[c.StartByte():c.EndByte()]))
		}
	}
	return ""
}

func bindingName(b *gotreesitter.Node, lang *gotreesitter.Language, content []byte) string {
	for i := 0; i < b.ChildCount(); i++ {
		c := b.Child(i)
		if c == nil {
			continue
		}
		if c.Type(lang) == "identifier" {
			return string(content[c.StartByte():c.EndByte()])
		}
	}
	return ""
}

func bindingValueIdentifier(b *gotreesitter.Node, lang *gotreesitter.Language, content []byte) string {
	for i := 0; i < b.ChildCount(); i++ {
		c := b.Child(i)
		if c == nil || c.Type(lang) != "expression_statement" {
			continue
		}
		for j := 0; j < c.ChildCount(); j++ {
			cc := c.Child(j)
			if cc == nil {
				continue
			}
			if cc.Type(lang) == "identifier" {
				return string(content[cc.StartByte():cc.EndByte()])
			}
		}
	}
	return ""
}
