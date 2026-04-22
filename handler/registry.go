package handler

import (
	"fmt"
	"strings"
	"sync"

	"github.com/owenrumney/go-lsp/lsp"
)

// QMLSymbol is the unified knowledge entry used for both hover and completion.
// One source of truth, so completion items ship with rich Documentation instead
// of relying on ResolveCompletionItem to find docs that may never be asked for.
type QMLSymbol struct {
	Label         string
	Kind          lsp.CompletionItemKind
	Detail        string // short one-liner shown next to the label
	Signature     string // optional; rendered as a fenced code block above the description
	Description   string // prose shown below the signature
	Module        string // e.g. "QtQuick", "builtin", empty for keywords
	Category      string // "type", "property", "keyword", "import", "anchor", "js", "workspace"
	InsertText    string // optional; implies snippet format when set and containing $
	InsertSnippet bool   // forces Snippet insert text format
}

// Render produces the markdown body used for both hover and CompletionItem.Documentation.
func (s QMLSymbol) Render() string {
	var b strings.Builder

	header := s.Label
	switch s.Category {
	case "type":
		fmt.Fprintf(&b, "**%s** — type", header)
	case "property":
		fmt.Fprintf(&b, "**%s** — property", header)
	case "keyword":
		fmt.Fprintf(&b, "**%s** — keyword", header)
	case "import":
		fmt.Fprintf(&b, "**%s** — module", header)
	case "anchor":
		fmt.Fprintf(&b, "**%s** — anchor", header)
	case "js":
		fmt.Fprintf(&b, "**%s** — JavaScript", header)
	case "workspace":
		fmt.Fprintf(&b, "**%s** — workspace component", header)
	case "quickshell-snippet":
		fmt.Fprintf(&b, "**%s** — snippet", header)
	default:
		fmt.Fprintf(&b, "**%s**", header)
	}
	if s.Module != "" {
		fmt.Fprintf(&b, "  \n_%s_", s.Module)
	}
	b.WriteString("\n\n")

	if s.Signature != "" {
		b.WriteString("```qml\n")
		b.WriteString(s.Signature)
		b.WriteString("\n```\n\n")
	}

	if s.Description != "" {
		b.WriteString(s.Description)
	}

	return b.String()
}

// CompletionItem builds a fully populated LSP completion item including docs.
func (s QMLSymbol) CompletionItem() lsp.CompletionItem {
	kind := s.Kind
	item := lsp.CompletionItem{
		Label:  s.Label,
		Kind:   &kind,
		Detail: s.Detail,
		Documentation: &lsp.MarkupContent{
			Kind:  lsp.Markdown,
			Value: s.Render(),
		},
	}
	if s.InsertText != "" {
		item.InsertText = s.InsertText
	}
	if s.InsertSnippet || strings.Contains(s.InsertText, "${") {
		fmtSnippet := lsp.InsertTextFormatSnippet
		item.InsertTextFormat = &fmtSnippet
	}
	return item
}

// symbolRegistry is looked up by Label. ResolveCompletionItem uses this as a
// fallback for clients that send us back items missing Documentation. The
// workspace scanner writes to it concurrently, so access is synchronized.
var (
	registryMu     sync.RWMutex
	symbolRegistry = map[string]QMLSymbol{}
)

func registerSymbols(entries ...QMLSymbol) {
	registryMu.Lock()
	defer registryMu.Unlock()
	for _, e := range entries {
		symbolRegistry[e.Label] = e
	}
}

func lookupSymbol(label string) (QMLSymbol, bool) {
	registryMu.RLock()
	defer registryMu.RUnlock()
	s, ok := symbolRegistry[label]
	return s, ok
}

func symbolsByCategory(cats ...string) []QMLSymbol {
	want := make(map[string]struct{}, len(cats))
	for _, c := range cats {
		want[c] = struct{}{}
	}
	registryMu.RLock()
	defer registryMu.RUnlock()
	out := make([]QMLSymbol, 0, len(symbolRegistry))
	for _, s := range symbolRegistry {
		if _, ok := want[s.Category]; ok {
			out = append(out, s)
		}
	}
	return out
}

func completionItemsByCategory(cats ...string) []lsp.CompletionItem {
	syms := symbolsByCategory(cats...)
	items := make([]lsp.CompletionItem, 0, len(syms))
	for _, s := range syms {
		items = append(items, s.CompletionItem())
	}
	return items
}

// init registers the registry entries that are *not* QML types or
// type-specific properties: QML syntax keywords, JavaScript/Qt global
// objects, and Quickshell boilerplate snippets. Everything else — types,
// properties, imports, inheritance chains — comes from qmltypes discovery
// at runtime (see qmltypes_discovery.go). If no qmltypes files can be
// found, the LSP has no knowledge of types/properties; that's the
// intentional tradeoff for keeping the server's type data honest.
func init() {
	registerKeywords()
	registerJSBuiltins()
}

func registerKeywords() {
	k := lsp.CompletionItemKindKeyword
	keywords := []QMLSymbol{
		{
			Label: "import", Kind: k, Detail: "Import a QML module",
			Signature:   "import <module> [<version>] [as <alias>]",
			Description: "Brings a module's types into scope. Must appear at the top of the file, before any object declarations.\n\n```qml\nimport QtQuick\nimport QtQuick.Controls\nimport QtQuick.Layouts as L\nimport \"./components\"\n```",
			Category:    "keyword",
			InsertText:  "import ",
		},
		{
			Label: "property", Kind: k, Detail: "Declare a property",
			Signature:   "property <type> <name>: <value>",
			Description: "Adds a new property to the surrounding object. Valid types include: `int`, `real`, `string`, `bool`, `color`, `url`, `var`, `list<T>`, any QML type, or `alias` (a pass-through reference).\n\n```qml\nproperty int count: 0\nproperty alias label: myText.text\nproperty list<Item> panels\n```",
			Category:    "keyword",
			InsertText:  "property ",
		},
		{
			Label: "readonly property", Kind: k, Detail: "Declare a read-only property",
			Signature:   "readonly property <type> <name>: <value>",
			Description: "A property that cannot be reassigned after its initial value is set.",
			Category:    "keyword",
			InsertText:  "readonly property ",
		},
		{
			Label: "required property", Kind: k, Detail: "Declare a required property",
			Signature:   "required property <type> <name>",
			Description: "A property that callers **must** supply. The QML engine throws if it's missing when the component is instantiated. Common in delegates that consume model roles.",
			Category:    "keyword",
			InsertText:  "required property ",
		},
		{
			Label: "default property", Kind: k, Detail: "Declare the default property",
			Signature:   "default property <type> <name>",
			Description: "The property that receives child objects when none is explicitly named. For `Item`, the default property is `data` (the list of children).",
			Category:    "keyword",
			InsertText:  "default property ",
		},
		{
			Label: "signal", Kind: k, Detail: "Declare a signal",
			Signature:   "signal <name>(<type> <param>, ...)",
			Description: "Declares a signal that can be emitted with `emitName()`. Listeners attach via `on<Name>` handlers.\n\n```qml\nsignal accepted(string value)\n```",
			Category:    "keyword",
			InsertText:  "signal ",
		},
		{
			Label: "function", Kind: k, Detail: "Declare a method",
			Signature:   "function name(param1, param2): returnType { ... }",
			Description: "Declares a JavaScript method on the surrounding object. Typed parameters and return type are optional but recommended.\n\n```qml\nfunction add(a: int, b: int): int {\n    return a + b\n}\n```",
			Category:    "keyword",
			InsertText:  "function ${1:name}(${2:params}) {\n\t$0\n}",
		},
		{
			Label: "component", Kind: k, Detail: "Declare an inline Component type",
			Signature:   "component <Name>: <BaseType> { ... }",
			Description: "Qt 6+ syntax for declaring a named inline component inside a QML file.\n\n```qml\ncomponent Badge: Rectangle {\n    color: \"red\"\n    width: 20; height: 20\n}\n```",
			Category:    "keyword",
			InsertText:  "component ",
		},
		{
			Label: "pragma", Kind: k, Detail: "File-level pragma",
			Signature:   "pragma <Name>",
			Description: "File-level directive. Common pragmas: `Singleton`, `NativeMethodBehavior: AcceptThisObject`, `ComponentBehavior: Bound`, `FunctionSignatureBehavior: Enforced`.",
			Category:    "keyword",
			InsertText:  "pragma ",
		},
		{
			Label: "as", Kind: k, Detail: "Import alias",
			Description: "Used inside an `import` statement to rename a module.",
			Category:    "keyword",
			InsertText:  "as ",
		},
		{
			Label: "on", Kind: k, Detail: "Behavior target specifier",
			Description: "Used in `Behavior on <property>` and `PropertyAnimation on <property>` to target a specific property.",
			Category:    "keyword",
		},
	}
	registerSymbols(keywords...)
}

func registerJSBuiltins() {
	fnKind := lsp.CompletionItemKindFunction
	objKind := lsp.CompletionItemKindModule
	entries := []QMLSymbol{
		{
			Label: "console", Kind: objKind, Detail: "Console logging object",
			Description: "Debug logging API.\n\n- `console.log(...)` — info-level output\n- `console.info(...)`\n- `console.warn(...)`\n- `console.error(...)`\n- `console.debug(...)`\n- `console.trace()` — current QML stack trace\n- `console.time(label)` / `console.timeEnd(label)`",
			Category:    "js",
		},
		{
			Label: "Math", Kind: objKind, Detail: "JavaScript Math object",
			Description: "Standard JS Math.\n\n**Constants:** `Math.PI`, `Math.E`, `Math.LN2`, `Math.LN10`, `Math.LOG2E`, `Math.LOG10E`, `Math.SQRT2`, `Math.SQRT1_2`.\n\n**Methods:** `abs`, `acos`, `asin`, `atan`, `atan2`, `ceil`, `cos`, `exp`, `floor`, `log`, `log2`, `log10`, `max`, `min`, `pow`, `random`, `round`, `sign`, `sin`, `sqrt`, `tan`, `trunc`, `hypot`, `cbrt`.",
			Category:    "js",
		},
		{
			Label: "JSON", Kind: objKind, Detail: "JSON parse/stringify",
			Description: "`JSON.parse(text)` and `JSON.stringify(value[, replacer, space])`.",
			Category:    "js",
		},
		{
			Label: "Date", Kind: objKind, Detail: "JavaScript Date",
			Description: "`new Date()`, `Date.now()`, `Date.parse(str)`. Instance methods: `getFullYear`, `getMonth`, `getDate`, `getHours`, `getTime`, `toISOString`, `toLocaleDateString`, `toLocaleTimeString`.",
			Category:    "js",
		},
		{
			Label: "Array", Kind: objKind, Detail: "JavaScript Array",
			Description: "`Array.isArray(v)`, `Array.from(iter)`, `Array.of(...items)`.\n\n**Instance methods:** `push`, `pop`, `shift`, `unshift`, `slice`, `splice`, `concat`, `join`, `map`, `filter`, `reduce`, `forEach`, `some`, `every`, `find`, `findIndex`, `includes`, `indexOf`, `sort`, `reverse`, `flat`, `flatMap`.",
			Category:    "js",
		},
		{
			Label: "Object", Kind: objKind, Detail: "JavaScript Object",
			Description: "`Object.keys(o)`, `Object.values(o)`, `Object.entries(o)`, `Object.assign(target, ...src)`, `Object.freeze(o)`, `Object.fromEntries(entries)`.",
			Category:    "js",
		},
		{
			Label: "String", Kind: fnKind, Detail: "String(value) — coerce to string",
			Signature:   "String(value: any): string",
			Description: "Converts any value to a string. Also used as a namespace for `String.fromCharCode(n)`.",
			Category:    "js",
			InsertText:  "String(${1:value})",
		},
		{
			Label: "Number", Kind: fnKind, Detail: "Number(value) — coerce to number",
			Signature:   "Number(value: any): number",
			Description: "Converts any value to a number. Also a namespace for `Number.isFinite`, `Number.isInteger`, `Number.parseFloat`, `Number.parseInt`, `Number.MAX_SAFE_INTEGER`, etc.",
			Category:    "js",
			InsertText:  "Number(${1:value})",
		},
		{
			Label: "Boolean", Kind: fnKind, Detail: "Boolean(value) — coerce to bool",
			Signature:   "Boolean(value: any): bool",
			Description: "Converts any value to a boolean.",
			Category:    "js",
			InsertText:  "Boolean(${1:value})",
		},
		{
			Label: "parseInt", Kind: fnKind, Detail: "Parse an integer from a string",
			Signature:   "parseInt(s: string, radix?: int): int",
			Description: "Parses the leading integer from `s` in the given `radix` (2–36, default 10).",
			Category:    "js",
			InsertText:  "parseInt(${1:s})",
		},
		{
			Label: "parseFloat", Kind: fnKind, Detail: "Parse a float from a string",
			Signature:   "parseFloat(s: string): number",
			Description: "Parses the leading floating-point value from `s`.",
			Category:    "js",
			InsertText:  "parseFloat(${1:s})",
		},
		{
			Label: "isNaN", Kind: fnKind, Detail: "Check for NaN",
			Signature:   "isNaN(value: any): bool",
			Description: "Returns `true` if `value` is `NaN` after numeric coercion. Prefer `Number.isNaN` to avoid coercion surprises.",
			Category:    "js",
			InsertText:  "isNaN(${1:value})",
		},
		{
			Label: "isFinite", Kind: fnKind, Detail: "Check for finite number",
			Signature:   "isFinite(value: any): bool",
			Description: "Returns `true` if `value` is a finite number.",
			Category:    "js",
			InsertText:  "isFinite(${1:value})",
		},
		{
			Label: "Qt", Kind: objKind, Detail: "Qt global object",
			Description: "QML engine globals.\n\n**Value factories:** `Qt.rect(x, y, w, h)`, `Qt.size(w, h)`, `Qt.point(x, y)`, `Qt.vector2d/3d/4d`, `Qt.quaternion(s, x, y, z)`, `Qt.matrix4x4(...)`.\n\n**Color/font:** `Qt.rgba(r,g,b,a)`, `Qt.hsla(h,s,l,a)`, `Qt.hsva(h,s,v,a)`, `Qt.tint(base, tint)`, `Qt.lighter(c[,factor])`, `Qt.darker(c[,factor])`, `Qt.font(obj)`.\n\n**Utilities:** `Qt.createComponent(url)`, `Qt.createQmlObject(qml, parent)`, `Qt.openUrlExternally(url)`, `Qt.resolvedUrl(url)`, `Qt.application`, `Qt.platform`, `Qt.locale()`, `Qt.formatDate/Time/DateTime`, `Qt.binding(fn)`, `Qt.callLater(fn)`.",
			Category:    "js",
		},
	}
	registerSymbols(entries...)
}

// Back-compat shims — keep the old getTypeInfo/getPropertyInfo signatures working
// for hover code that hasn't been migrated to the registry yet.
func registryTypeInfo(name string) (QMLTypeInfo, bool) {
	s, ok := lookupSymbol(name)
	if !ok || (s.Category != "type" && s.Category != "workspace") {
		return QMLTypeInfo{}, false
	}
	return QMLTypeInfo{
		Description: s.Description,
		Type:        "Object",
		Module:      s.Module,
	}, true
}

func registryPropertyInfo(name string) (PropertyInfo, bool) {
	s, ok := lookupSymbol(name)
	if !ok || (s.Category != "property" && s.Category != "anchor") {
		return PropertyInfo{}, false
	}
	typ := ""
	if idx := strings.Index(s.Detail, " — "); idx > 0 {
		typ = s.Detail[:idx]
	}
	return PropertyInfo{
		Description: s.Description,
		Type:        typ,
	}, true
}
