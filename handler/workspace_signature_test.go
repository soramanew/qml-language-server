package handler

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/owenrumney/go-lsp/lsp"
)

// TestWorkspaceComponentExposesDeclaredProperties verifies that a
// `MyWidget.qml` file in the workspace gets its `property Type name`
// declarations lifted into the type registry so completion inside
// `MyWidget { ... }` offers them — and chain walking through a
// `property MyWidget foo` resolves to MyWidget's declared properties.
func TestWorkspaceComponentExposesDeclaredProperties(t *testing.T) {
	dir := t.TempDir()
	widgetPath := filepath.Join(dir, "WorkWidgetProbe.qml")
	src := `import QtQuick

Rectangle {
    property int customCount: 0
    property string caption: "hi"
}
`
	if err := os.WriteFile(widgetPath, []byte(src), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	// Save and restore registry state for isolation.
	typePropertiesMu.Lock()
	savedProps := typeProperties["WorkWidgetProbe"]
	savedBase := baseTypes["WorkWidgetProbe"]
	delete(typeProperties, "WorkWidgetProbe")
	delete(baseTypes, "WorkWidgetProbe")
	typePropertiesMu.Unlock()
	defer func() {
		typePropertiesMu.Lock()
		if savedProps != nil {
			typeProperties["WorkWidgetProbe"] = savedProps
		} else {
			delete(typeProperties, "WorkWidgetProbe")
		}
		if savedBase != nil {
			baseTypes["WorkWidgetProbe"] = savedBase
		} else {
			delete(baseTypes, "WorkWidgetProbe")
		}
		typePropertiesMu.Unlock()
	}()

	indexWorkspaceComponentSignature(workspaceComponent{
		Name: "WorkWidgetProbe",
		Path: widgetPath,
		URI:  pathToURI(widgetPath),
	})

	labels := map[string]bool{}
	for _, s := range lookupTypeProperties("WorkWidgetProbe") {
		labels[s.Label] = true
	}
	for _, want := range []string{"customCount", "caption"} {
		if !labels[want] {
			t.Errorf("expected %q in WorkWidgetProbe props", want)
		}
	}
	if chain := lookupBaseTypes("WorkWidgetProbe"); len(chain) == 0 || chain[0] != "Rectangle" {
		t.Errorf("expected base chain starting at Rectangle, got %v", chain)
	}
}

// TestCompletionOffersIdsAsReferences verifies that ids declared in the
// current document show up as completion items in value contexts so the
// user can type `width: roo` and pick `root`.
func TestCompletionOffersIdsAsReferences(t *testing.T) {
	doc := `import QtQuick

Rectangle {
    id: root
    Text {
        id: label
        text:
    }
}
`
	uri := lsp.DocumentURI("test://ids.qml")
	h := newTestHandler(t, uri, doc)

	// Cursor right after `text: ` — ContextAfterColon.
	list, err := h.Completion(context.Background(), &lsp.CompletionParams{
		TextDocumentPositionParams: lsp.TextDocumentPositionParams{
			TextDocument: lsp.TextDocumentIdentifier{URI: uri},
			Position:     lsp.Position{Line: 6, Character: 14},
		},
	})
	if err != nil {
		t.Fatalf("Completion: %v", err)
	}
	seen := map[string]bool{}
	for _, item := range list.Items {
		seen[item.Label] = true
	}
	for _, id := range []string{"root", "label"} {
		if !seen[id] {
			t.Errorf("expected id %q in completions", id)
		}
	}
}
