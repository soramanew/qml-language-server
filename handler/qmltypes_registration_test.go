package handler

import "testing"

// TestRegisterQMLTypesPropertiesFiltersInternals checks that C++ constructors,
// cloned-overload duplicates, and underscore-prefixed Qt internals
// (`_q_createJSWrapper`, `_q_resourceObjectDeleted`, ...) never land in
// typeProperties. Those names are present in Qt's real qmltypes files and
// pollute completion/hover if registered verbatim.
func TestRegisterQMLTypesPropertiesFiltersInternals(t *testing.T) {
	const qmlName = "FilterProbe"

	// Save and restore the per-type catalog.
	saved := typeProperties[qmlName]
	defer func() {
		if saved == nil {
			delete(typeProperties, qmlName)
		} else {
			typeProperties[qmlName] = saved
		}
	}()
	delete(typeProperties, qmlName)

	comp := &QMLTypesComponent{
		Name:    "QFilterProbe",
		Exports: []string{"Probe/FilterProbe 1.0"},
		Properties: []QMLTypesProperty{
			{Name: "color", Type: "QColor"},
			{Name: "_q_internal", Type: "int"},
		},
		Signals: []QMLTypesSignal{
			{Name: "clicked"},
			{Name: "_q_privateSignal"},
		},
		Methods: []QMLTypesMethod{
			{Name: "update"},
			{Name: "FilterProbe", IsConstructor: true},
			{Name: "forceActiveFocus", IsCloned: true},
			{Name: "_q_createJSWrapper"},
		},
	}
	registerQMLTypesProperties(comp, qmlName)

	got := map[string]bool{}
	for _, p := range typeProperties[qmlName] {
		got[p.Label] = true
	}

	for _, want := range []string{"color", "onClicked", "update"} {
		if !got[want] {
			t.Errorf("expected %q in typeProperties[%s]", want, qmlName)
		}
	}
	for _, bad := range []string{
		"_q_internal",
		"on_q_privateSignal",
		"FilterProbe",         // constructor
		"forceActiveFocus",    // cloned overload
		"_q_createJSWrapper",  // private method
	} {
		if got[bad] {
			t.Errorf("unexpected %q leaked into typeProperties[%s]", bad, qmlName)
		}
	}
}
