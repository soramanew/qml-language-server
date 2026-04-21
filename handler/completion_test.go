package handler

import (
	"testing"
)

func TestScopedPropertyCompletionsEmptyTypeKeepsAll(t *testing.T) {
	before := len(qmlPropertyCompletions())
	after := len(scopedPropertyCompletions(""))
	if before != after {
		t.Errorf("empty enclosingType should not filter: before=%d after=%d", before, after)
	}
}

func TestScopedPropertyCompletionsDropsWrongTypeProperties(t *testing.T) {
	// Inside a Text { ... } body, properties that only make sense on views
	// or mouse handlers must not appear.
	items := scopedPropertyCompletions("Text")
	forbidden := map[string]bool{
		"radius":       true,
		"source":       true,
		"model":        true,
		"delegate":     true,
		"currentIndex": true,
		"onClicked":    true,
		"onEntered":    true,
		"onTriggered":  true,
	}
	for _, item := range items {
		if forbidden[item.Label] {
			t.Errorf("property %q should not appear inside Text body", item.Label)
		}
	}
}

func TestScopedPropertyCompletionsHidesItemPropsFromNonItemTypes(t *testing.T) {
	// Timer is not an Item. Inside a Timer body the user should not see
	// x/y/width/height/anchors/parent/etc. — those are Item-specific.
	items := scopedPropertyCompletions("Timer")
	forbidden := map[string]bool{
		"x":               true,
		"y":               true,
		"width":           true,
		"height":          true,
		"anchors":         true,
		"parent":          true,
		"children":        true,
		"rotation":        true,
		"transformOrigin": true,
		"focus":           true,
		"activeFocus":     true,
		"color":           true,
	}
	for _, item := range items {
		if forbidden[item.Label] {
			t.Errorf("Item-level property %q should not appear inside Timer body", item.Label)
		}
	}
}

func TestScopedPropertyCompletionsExcludesAnchorSubproperties(t *testing.T) {
	// Anchor sub-properties like `fill`/`centerIn`/`top` are only valid
	// under `anchors.` — they must never appear at object-body top level.
	items := scopedPropertyCompletions("Rectangle")
	for _, item := range items {
		switch item.Label {
		case "fill", "centerIn", "top", "bottom", "left", "right",
			"horizontalCenter", "verticalCenter", "margins",
			"topMargin", "bottomMargin", "leftMargin", "rightMargin":
			t.Errorf("anchor sub-property %q leaked into Rectangle body", item.Label)
		}
	}
}

func TestScopedPropertyCompletionsKeepsMatchingProperties(t *testing.T) {
	// Text-specific restricted properties should appear for Text.
	items := scopedPropertyCompletions("Text")
	required := []string{"text", "font", "color"}
	labels := map[string]bool{}
	for _, item := range items {
		labels[item.Label] = true
	}
	for _, r := range required {
		if !labels[r] {
			t.Errorf("property %q missing from Text body completions", r)
		}
	}
}

func TestScopedPropertyCompletionsWalksInheritance(t *testing.T) {
	// Save and restore baseTypes so we don't leak between tests.
	saved := baseTypes["TestChild"]
	baseTypes["TestChild"] = []string{"Rectangle"}
	defer func() {
		if saved == nil {
			delete(baseTypes, "TestChild")
		} else {
			baseTypes["TestChild"] = saved
		}
	}()

	items := scopedPropertyCompletions("TestChild")
	labels := map[string]bool{}
	for _, item := range items {
		labels[item.Label] = true
	}
	// `radius` is Rectangle-only; should survive for a TestChild whose base
	// chain includes Rectangle.
	if !labels["radius"] {
		t.Error("inherited property `radius` missing via Rectangle base chain")
	}
	// `model` is restricted to views; should not be in the chain.
	if labels["model"] {
		t.Error("unrelated property `model` leaked through inheritance")
	}
}

func TestTypeChainSetTransitive(t *testing.T) {
	// Rectangle -> Item (nonexistent today in hand-coded baseTypes; simulate).
	saved := baseTypes["Rectangle"]
	baseTypes["Rectangle"] = []string{"Item"}
	defer func() {
		if saved == nil {
			delete(baseTypes, "Rectangle")
		} else {
			baseTypes["Rectangle"] = saved
		}
	}()

	chain := typeChainSet("Rectangle")
	if _, ok := chain["Rectangle"]; !ok {
		t.Error("chain missing self")
	}
	if _, ok := chain["Item"]; !ok {
		t.Error("chain missing transitive base Item")
	}
}

func TestTypePropertyCompletionsDeepChain(t *testing.T) {
	// Register three levels, ensure all three contribute props and the
	// closest one wins on label collision.
	typeProperties["DeepA"] = []QMLSymbol{
		{Label: "aProp", Category: "property"},
		{Label: "shared", Category: "property", Detail: "from A"},
	}
	typeProperties["DeepB"] = []QMLSymbol{
		{Label: "bProp", Category: "property"},
		{Label: "shared", Category: "property", Detail: "from B"},
	}
	typeProperties["DeepC"] = []QMLSymbol{
		{Label: "cProp", Category: "property"},
	}
	baseTypes["DeepA"] = []string{"DeepB"}
	baseTypes["DeepB"] = []string{"DeepC"}
	defer func() {
		delete(typeProperties, "DeepA")
		delete(typeProperties, "DeepB")
		delete(typeProperties, "DeepC")
		delete(baseTypes, "DeepA")
		delete(baseTypes, "DeepB")
	}()

	items := typePropertyCompletions("DeepA")
	seen := map[string]string{}
	for _, item := range items {
		seen[item.Label] = item.Detail
	}
	for _, want := range []string{"aProp", "bProp", "cProp"} {
		if _, ok := seen[want]; !ok {
			t.Errorf("missing inherited property %q", want)
		}
	}
	if seen["shared"] != "from A" {
		t.Errorf("closest-wins violated: shared detail = %q, want `from A`", seen["shared"])
	}
}
