package handler

import (
	"sync"

	"github.com/owenrumney/go-lsp/lsp"
)

// typeProperties stores the properties that belong to each QML type.
// Lookup is by type name. Contents are populated at runtime from qmltypes
// discovery (see qmltypes_discovery.go) and from the workspace scanner
// (see workspace.go) which extracts declared properties out of the user's
// own `.qml` files. Both run in goroutines, so access is serialized by
// typePropertiesMu.
var (
	typePropertiesMu sync.RWMutex
	typeProperties   = map[string][]QMLSymbol{}
	baseTypes        = map[string][]string{}
)

// typePropertyCompletions returns the properties of `typeName`, walking
// its base-type chain transitively. Duplicates by Label are dropped
// (closest wins). Returns nil when nothing is registered for the type —
// which is the normal case when qmltypes discovery hasn't run or the
// user's QML runtime isn't installed.
func typePropertyCompletions(typeName string) []lsp.CompletionItem {
	if typeName == "" {
		return nil
	}
	typePropertiesMu.RLock()
	defer typePropertiesMu.RUnlock()
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

// lookupTypeProperties returns a snapshot of the registered properties for
// a type. Callers that need to iterate without holding the registry lock
// should use this rather than touching typeProperties directly.
func lookupTypeProperties(typeName string) []QMLSymbol {
	typePropertiesMu.RLock()
	defer typePropertiesMu.RUnlock()
	if props, ok := typeProperties[typeName]; ok {
		out := make([]QMLSymbol, len(props))
		copy(out, props)
		return out
	}
	return nil
}

// lookupBaseTypes returns a snapshot of the base-type chain for a type.
func lookupBaseTypes(typeName string) []string {
	typePropertiesMu.RLock()
	defer typePropertiesMu.RUnlock()
	if bt, ok := baseTypes[typeName]; ok {
		out := make([]string, len(bt))
		copy(out, bt)
		return out
	}
	return nil
}
