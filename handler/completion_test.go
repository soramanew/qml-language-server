package handler

import "testing"

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
