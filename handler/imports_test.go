package handler

import (
	"context"
	"testing"

	"github.com/owenrumney/go-lsp/lsp"
)

func TestImportedTypeCompletionsFiltersUnimportedModules(t *testing.T) {
	// Seed a type that lives in an uncommon module to avoid coincidental
	// presence in the base registry.
	registerSymbols(QMLSymbol{
		Label:    "FakeVideoType",
		Kind:     lsp.CompletionItemKindClass,
		Module:   "QtMultimedia",
		Category: "type",
	})

	withImports := importedTypeCompletions(map[string]struct{}{"QtQuick": {}})
	for _, it := range withImports {
		if it.Label == "FakeVideoType" {
			t.Error("FakeVideoType leaked when QtMultimedia was not imported")
		}
	}

	withMulti := importedTypeCompletions(map[string]struct{}{"QtMultimedia": {}})
	found := false
	for _, it := range withMulti {
		if it.Label == "FakeVideoType" {
			found = true
			break
		}
	}
	if !found {
		t.Error("FakeVideoType missing when QtMultimedia was imported")
	}
}

func TestImportedTypeCompletionsKeepsEmptyModuleTypes(t *testing.T) {
	// Symbols with Module == "" (e.g. workspace stand-ins, implicit
	// primitives) must pass through regardless of the import set.
	registerSymbols(QMLSymbol{
		Label:    "ImplicitLocalType",
		Kind:     lsp.CompletionItemKindClass,
		Module:   "",
		Category: "type",
	})

	items := importedTypeCompletions(map[string]struct{}{"QtQuick": {}})
	found := false
	for _, it := range items {
		if it.Label == "ImplicitLocalType" {
			found = true
			break
		}
	}
	if !found {
		t.Error("empty-module types should always pass the import filter")
	}
}

func TestImportedTypeCompletionsNilDisablesFilter(t *testing.T) {
	before := len(symbolsByCategory("type"))
	items := importedTypeCompletions(nil)
	if len(items) != before {
		t.Errorf("nil imported set should return all types: got %d want %d", len(items), before)
	}
}

func TestImportedModulesCollectsQualifiedImports(t *testing.T) {
	doc := "import QtQuick\nimport QtQuick.Controls\nimport \"./components\"\n\nItem {}\n"
	h := newTestHandler(t, "test://imports.qml", doc)

	got := h.importedModules("test://imports.qml")
	for _, name := range []string{"QtQuick", "QtQuick.Controls"} {
		if _, ok := got[name]; !ok {
			t.Errorf("missing %q from imported set: %+v", name, got)
		}
	}
	// Quoted-path imports should not enter the module set.
	if _, ok := got["./components"]; ok {
		t.Error("relative path import should not be tracked as a module")
	}
}

func TestCompletionHidesUnimportedModuleTypes(t *testing.T) {
	registerSymbols(QMLSymbol{
		Label:    "GatedOnlyInMultimedia",
		Kind:     lsp.CompletionItemKindClass,
		Module:   "QtMultimedia",
		Category: "type",
	})

	doc := "import QtQuick\n\nItem {\n    \n}\n"
	uri := lsp.DocumentURI("test://gated.qml")
	h := newTestHandler(t, uri, doc)

	list, err := h.Completion(context.Background(), &lsp.CompletionParams{
		TextDocumentPositionParams: lsp.TextDocumentPositionParams{
			TextDocument: lsp.TextDocumentIdentifier{URI: uri},
			Position:     lsp.Position{Line: 2, Character: 4},
		},
	})
	if err != nil {
		t.Fatalf("Completion: %v", err)
	}
	for _, item := range list.Items {
		if item.Label == "GatedOnlyInMultimedia" {
			t.Error("type from non-imported QtMultimedia module should not appear")
		}
	}
}
