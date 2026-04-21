package handler

import "github.com/owenrumney/go-lsp/lsp"

// registerQuickshellBuiltins adds Quickshell-specific boilerplate snippets.
// Types, imports, and singleton members are NOT hard-coded here — they come
// from the user's Quickshell qmltypes files via qmltypes discovery so they
// stay in sync with whatever version is actually installed.
func registerQuickshellBuiltins() {
	registerQuickshellSnippets()
}

func registerQuickshellSnippets() {
	kind := lsp.CompletionItemKindSnippet
	snippets := []struct {
		label, body, detail, desc string
	}{
		{
			"qs-scope",
			"import Quickshell\n\nScope {\n\t${0}\n}",
			"Quickshell shell scope",
			"Top-level `Scope` with the Quickshell import.",
		},
		{
			"qs-panel",
			"PanelWindow {\n\tanchors {\n\t\ttop: true\n\t\tleft: true\n\t\tright: true\n\t}\n\timplicitHeight: ${1:30}\n\n\t${0}\n}",
			"PanelWindow (bar / dock)",
			"Top-anchored `PanelWindow` with default height.",
		},
		{
			"qs-float",
			"FloatingWindow {\n\tvisible: ${1:true}\n\twidth: ${2:400}\n\theight: ${3:300}\n\n\t${0}\n}",
			"FloatingWindow",
			"Basic `FloatingWindow` with size.",
		},
		{
			"qs-variants",
			"Variants {\n\tmodel: ${1:Quickshell.screens}\n\n\t${2:ComponentName} {\n\t\trequired property var modelData\n\t\t${0}\n\t}\n}",
			"Variants (one instance per screen)",
			"`Variants` with model and a delegate declaring `required property var modelData`.",
		},
		{
			"qs-process",
			"Process {\n\tid: ${1:proc}\n\tcommand: [${2:\"command\"}]\n\trunning: ${3:false}\n\n\tstdout: StdioCollector {\n\t\tonStreamFinished: {\n\t\t\t${0}\n\t\t}\n\t}\n\n\tonExited: (code) => {\n\t\tif (code !== 0) return\n\t}\n}",
			"Process with stdout collector",
			"`Process` pattern with a `StdioCollector` on stdout and an `onExited` guard.",
		},
		{
			"qs-fileview",
			"FileView {\n\tid: ${1:fileView}\n\tpath: ${2:\"~/.config/example\"}\n\n\tonTextChanged: {\n\t\t${0}\n\t}\n}",
			"FileView watcher",
			"`FileView` watching a path with an `onTextChanged` handler.",
		},
		{
			"qs-layershell",
			"WlrLayershell.layer: WlrLayer.${1:Top}\nWlrLayershell.keyboardFocus: WlrKeyboardFocus.${2:None}\nWlrLayershell.exclusionMode: ExclusionMode.${3:Normal}",
			"WlrLayershell attached properties",
			"Standard `WlrLayershell` configuration block for a `PanelWindow`.",
		},
		{
			"qs-timer",
			"Timer {\n\tid: ${1:timer}\n\tinterval: ${2:1000}\n\trunning: ${3:false}\n\trepeat: ${4:false}\n\n\tonTriggered: {\n\t\t${0}\n\t}\n}",
			"Timer",
			"`Timer` with interval and handler.",
		},
		{
			"qs-prop",
			"property ${1:var} ${2:name}: ${3:null}",
			"Property declaration",
			"QML property declaration with type, name, and default value.",
		},
		{
			"qs-signal",
			"signal ${1:signalName}(${2:type} ${3:param})",
			"Signal declaration",
			"QML signal declaration with typed parameters.",
		},
	}
	for _, s := range snippets {
		registerSymbols(QMLSymbol{
			Label:         s.label,
			Kind:          kind,
			Detail:        s.detail,
			Description:   s.desc,
			Category:      "quickshell-snippet",
			InsertText:    s.body,
			InsertSnippet: true,
		})
	}
}
