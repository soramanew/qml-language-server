package handler

import (
	"context"
	"testing"

	"github.com/owenrumney/go-lsp/lsp"
)

func TestResolvePropertyTypeWalksChain(t *testing.T) {
	typeProperties["ChainOuter"] = []QMLSymbol{
		{Label: "child", Signature: "ChainOuter.child: ChainInner", Category: "property"},
	}
	typeProperties["ChainInner"] = []QMLSymbol{
		{Label: "leaf", Signature: "ChainInner.leaf: real", Category: "property"},
	}
	defer delete(typeProperties, "ChainOuter")
	defer delete(typeProperties, "ChainInner")

	if got := resolvePropertyType("ChainOuter", "child"); got != "ChainInner" {
		t.Errorf("resolvePropertyType(ChainOuter, child) = %q, want ChainInner", got)
	}
	if got := resolvePropertyType("ChainInner", "leaf"); got != "real" {
		t.Errorf("resolvePropertyType(ChainInner, leaf) = %q, want real", got)
	}
}

func TestResolvePropertyTypeRejectsUnchainable(t *testing.T) {
	typeProperties["GroupHolder"] = []QMLSymbol{
		{Label: "anchors", Signature: "GroupHolder.anchors: group", Category: "property"},
		{Label: "parts", Signature: "GroupHolder.parts: list<Item>", Category: "property"},
		{Label: "variant", Signature: "GroupHolder.variant: var", Category: "property"},
	}
	defer delete(typeProperties, "GroupHolder")

	for _, name := range []string{"anchors", "parts", "variant"} {
		if got := resolvePropertyType("GroupHolder", name); got != "" {
			t.Errorf("resolvePropertyType(GroupHolder, %s) = %q, want empty", name, got)
		}
	}
}

func TestExtractPropertyType(t *testing.T) {
	cases := map[string]string{
		"width: real":                     "real",
		"Text.text: string":               "string",
		"parent: Item (read-only)":        "Item",
		"anchors: group":                  "group",
		"TestView.model: list<QtObject>":  "list<QtObject>",
		"no separator here":               "",
	}
	for sig, want := range cases {
		if got := extractPropertyType(sig); got != want {
			t.Errorf("extractPropertyType(%q) = %q, want %q", sig, got, want)
		}
	}
}

func TestChainCompletionWalksMultiLevel(t *testing.T) {
	typeProperties["ChainRoot"] = []QMLSymbol{
		{Label: "middle", Signature: "ChainRoot.middle: ChainMid", Category: "property"},
	}
	typeProperties["ChainMid"] = []QMLSymbol{
		{Label: "leafItem", Signature: "ChainMid.leafItem: ChainLeaf", Category: "property"},
	}
	typeProperties["ChainLeaf"] = []QMLSymbol{
		{Label: "finalProp", Signature: "ChainLeaf.finalProp: real", Category: "property"},
	}
	defer delete(typeProperties, "ChainRoot")
	defer delete(typeProperties, "ChainMid")
	defer delete(typeProperties, "ChainLeaf")

	doc := "import QtQuick\n\nChainRoot {\n    id: r\n    width: r.middle.leafItem.\n}\n"
	uri := lsp.DocumentURI("test://chain.qml")
	h := newTestHandler(t, uri, doc)

	list, err := h.Completion(context.Background(), &lsp.CompletionParams{
		TextDocumentPositionParams: lsp.TextDocumentPositionParams{
			TextDocument: lsp.TextDocumentIdentifier{URI: uri},
			Position:     lsp.Position{Line: 4, Character: 29},
		},
	})
	if err != nil {
		t.Fatalf("Completion: %v", err)
	}
	found := false
	for _, item := range list.Items {
		if item.Label == "finalProp" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected finalProp from ChainLeaf in chain completions, got %d items", len(list.Items))
	}
}

func TestChainCompletionTerminalAnchorsOffersAnchors(t *testing.T) {
	// The chain walker special-cases `anchors` as a terminal segment and
	// calls getAnchorCompletions() unconditionally. Seed two anchor entries
	// so the lookup returns something observable.
	registerSymbols(
		QMLSymbol{Label: "syntheticAnchorFill", Kind: lsp.CompletionItemKindProperty, Category: "anchor"},
		QMLSymbol{Label: "syntheticAnchorCenterIn", Kind: lsp.CompletionItemKindProperty, Category: "anchor"},
	)

	typeProperties["AnchorHost"] = []QMLSymbol{}
	defer delete(typeProperties, "AnchorHost")

	doc := "import FakeModule\n\nAnchorHost {\n    id: rect\n    width: rect.anchors.\n}\n"
	uri := lsp.DocumentURI("test://anchors-chain.qml")
	h := newTestHandler(t, uri, doc)

	list, err := h.Completion(context.Background(), &lsp.CompletionParams{
		TextDocumentPositionParams: lsp.TextDocumentPositionParams{
			TextDocument: lsp.TextDocumentIdentifier{URI: uri},
			Position:     lsp.Position{Line: 4, Character: 24},
		},
	})
	if err != nil {
		t.Fatalf("Completion: %v", err)
	}
	needed := map[string]bool{"syntheticAnchorFill": false, "syntheticAnchorCenterIn": false}
	for _, item := range list.Items {
		if _, want := needed[item.Label]; want {
			needed[item.Label] = true
		}
	}
	for name, seen := range needed {
		if !seen {
			t.Errorf("expected anchor %q in completions for rect.anchors.", name)
		}
	}
}

func TestChainCompletionBrokenChainFallsBack(t *testing.T) {
	// When the chain can't be resolved (group-typed intermediate), the
	// completion path falls back to qmlPropertyCompletions. Seed a
	// distinctive entry so we can detect that the fallback kicked in.
	registerSymbols(QMLSymbol{
		Label:    "fallbackMarker",
		Kind:     lsp.CompletionItemKindProperty,
		Category: "property",
	})

	typeProperties["ChainA"] = []QMLSymbol{
		{Label: "mystery", Signature: "ChainA.mystery: group", Category: "property"},
	}
	defer delete(typeProperties, "ChainA")

	doc := "import FakeModule\n\nChainA {\n    id: a\n    width: a.mystery.broken.\n}\n"
	uri := lsp.DocumentURI("test://broken.qml")
	h := newTestHandler(t, uri, doc)

	list, err := h.Completion(context.Background(), &lsp.CompletionParams{
		TextDocumentPositionParams: lsp.TextDocumentPositionParams{
			TextDocument: lsp.TextDocumentIdentifier{URI: uri},
			Position:     lsp.Position{Line: 4, Character: 27},
		},
	})
	if err != nil {
		t.Fatalf("Completion: %v", err)
	}
	for _, item := range list.Items {
		if item.Label == "fallbackMarker" {
			return
		}
	}
	t.Errorf("expected fallbackMarker in generic fallback; got %d items", len(list.Items))
}
