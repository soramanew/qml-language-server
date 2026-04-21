package handler

import "github.com/owenrumney/go-lsp/lsp"

// typeProperties stores the properties that belong to each QML type.
// Lookup is by type name. Contents are populated at runtime from qmltypes
// discovery (see qmltypes_discovery.go); no entries are hard-coded, so
// without qmltypes files on disk this map is empty and property
// completion for a given type returns nothing.
var typeProperties = map[string][]QMLSymbol{}

// baseTypes maps a QML type to its direct base(s). typePropertyCompletions
// walks this transitively to resolve inherited properties. Populated at
// runtime by buildInheritanceChains from qmltypes prototype metadata; no
// entries are hard-coded.
var baseTypes = map[string][]string{}

// typePropertyCompletions returns the properties of `typeName`, walking
// its base-type chain transitively. Duplicates by Label are dropped
// (closest wins). Returns nil when nothing is registered for the type —
// which is the normal case when qmltypes discovery hasn't run or the
// user's QML runtime isn't installed.
func typePropertyCompletions(typeName string) []lsp.CompletionItem {
	if typeName == "" {
		return nil
	}
	seen := map[string]bool{}
	visited := map[string]bool{}
	var items []lsp.CompletionItem
	queue := []string{typeName}
	for len(queue) > 0 && len(visited) < 32 {
		t := queue[0]
		queue = queue[1:]
		if visited[t] {
			continue
		}
		visited[t] = true
		for _, s := range typeProperties[t] {
			if seen[s.Label] {
				continue
			}
			seen[s.Label] = true
			items = append(items, s.CompletionItem())
		}
		queue = append(queue, baseTypes[t]...)
	}
	return items
}
