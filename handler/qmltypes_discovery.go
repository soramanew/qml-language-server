package handler

import (
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/owenrumney/go-lsp/lsp"
)

// DiscoverAndRegisterQMLTypes walks known Qt installation paths, parses
// every qmldir/qmltypes pair it finds, and registers the types into the
// global symbol registry. Hard-coded entries are never removed — qmltypes
// data augments them so users never lose what they had.
//
// If workspaceRoots is non-empty, the function also looks for a .qmlls.ini
// file (the same format Qt's qmlls reads) and adds its buildDir and
// importPaths to the search.
func DiscoverAndRegisterQMLTypes(logger *slog.Logger, workspaceRoots []string) {
	paths := qmlImportPaths()

	// Merge paths from .qmlls.ini if present.
	if cfg := FindAndParseQMLLSIni(workspaceRoots); cfg != nil {
		if logger != nil {
			logger.Info("loaded .qmlls.ini", "buildDir", cfg.BuildDir, "importPaths", cfg.ImportPaths)
		}
		if cfg.BuildDir != "" {
			paths = appendUnique(paths, cfg.BuildDir)
		}
		for _, p := range cfg.ImportPaths {
			paths = appendUnique(paths, p)
		}
	}

	if len(paths) == 0 {
		if logger != nil {
			logger.Info("no QML import paths found; skipping qmltypes discovery")
		}
		return
	}

	var allModules []discoveredModule
	for _, root := range paths {
		allModules = append(allModules, discoverModules(root)...)
	}

	if logger != nil {
		logger.Info("discovered QML modules", "count", len(allModules))
	}

	for _, dm := range allModules {
		mod, err := ParseQMLTypesFile(dm.qmltypesPath)
		if err != nil {
			if logger != nil {
				logger.Warn("failed to parse qmltypes", "path", dm.qmltypesPath, "err", err)
			}
			continue
		}
		registerQMLTypesModule(mod, dm.moduleName)
	}
}

func appendUnique(paths []string, p string) []string {
	p = filepath.Clean(p)
	for _, existing := range paths {
		if existing == p {
			return paths
		}
	}
	if info, err := os.Stat(p); err == nil && info.IsDir() {
		paths = append(paths, p)
	}
	return paths
}

type discoveredModule struct {
	moduleName    string
	qmltypesPath  string
	qmldirPath    string
}

// moduleQMLDirs maps module name to the absolute path of the qmldir file that
// declared it. Populated by DiscoverAndRegisterQMLTypes and used by document
// links to resolve `import Foo` targets. First writer wins so Qt6 takes
// precedence over Qt5 when both are installed.
var (
	moduleDirsMu sync.RWMutex
	moduleQMLDirs = map[string]string{}
)

func recordModuleQMLDir(name, path string) {
	if name == "" || path == "" {
		return
	}
	moduleDirsMu.Lock()
	if _, ok := moduleQMLDirs[name]; !ok {
		moduleQMLDirs[name] = path
	}
	moduleDirsMu.Unlock()
}

// LookupModuleQMLDir returns the qmldir path registered for a module, or "".
func LookupModuleQMLDir(name string) string {
	moduleDirsMu.RLock()
	defer moduleDirsMu.RUnlock()
	return moduleQMLDirs[name]
}

// qmlImportPaths returns directories to scan for QML modules.
func qmlImportPaths() []string {
	var paths []string
	seen := map[string]bool{}
	add := func(p string) {
		p = filepath.Clean(p)
		if !seen[p] {
			if info, err := os.Stat(p); err == nil && info.IsDir() {
				seen[p] = true
				paths = append(paths, p)
			}
		}
	}

	// Qt6, then Qt5 — first writer wins for duplicate exports.
	add("/usr/lib/qt6/qml")
	add("/usr/lib/qt/qml")
	add("/usr/lib64/qt6/qml")
	add("/usr/lib64/qt/qml")
	add("/usr/local/lib/qt6/qml")

	if p := os.Getenv("QML_IMPORT_PATH"); p != "" {
		for _, dir := range filepath.SplitList(p) {
			add(dir)
		}
	}
	if p := os.Getenv("QML2_IMPORT_PATH"); p != "" {
		for _, dir := range filepath.SplitList(p) {
			add(dir)
		}
	}
	return paths
}

// discoverModules walks a root directory for qmldir files and returns each
// module that has a typeinfo reference.
func discoverModules(root string) []discoveredModule {
	var modules []discoveredModule
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			return nil
		}
		if d.Name() != "qmldir" {
			return nil
		}
		qmld, err := ParseQMLDirFile(path)
		if err != nil || qmld.TypeInfo == "" {
			return nil
		}
		typesPath := filepath.Join(qmld.Dir, qmld.TypeInfo)
		if _, err := os.Stat(typesPath); err != nil {
			return nil
		}
		modules = append(modules, discoveredModule{
			moduleName:   qmld.Name,
			qmltypesPath: typesPath,
			qmldirPath:   path,
		})
		recordModuleQMLDir(qmld.Name, path)
		return nil
	})
	return modules
}

// prototypeIndex maps C++ class names to their parsed component so we can
// walk inheritance chains to resolve properties.
var (
	protoMu    sync.RWMutex
	protoIndex = map[string]*QMLTypesComponent{}
)

func indexPrototype(comp *QMLTypesComponent) {
	protoMu.Lock()
	protoIndex[comp.Name] = comp
	protoMu.Unlock()
}


// registerQMLTypesModule converts parsed qmltypes components into QMLSymbol
// entries and writes them into the global symbol registry. Existing entries
// (hard-coded or from earlier modules) are not overwritten — only new
// symbols or richer property sets are added.
func registerQMLTypesModule(mod *QMLTypesModule, fallbackModule string) {
	// First pass: index all components by C++ name for prototype resolution.
	for i := range mod.Components {
		indexPrototype(&mod.Components[i])
	}

	// Second pass: register types that have QML exports.
	for i := range mod.Components {
		comp := &mod.Components[i]
		qmlName := comp.ExportedName()
		if qmlName == "" {
			continue
		}
		// Only register types that are creatable or singletons — skip internal
		// value types and sequence wrappers.
		if comp.AccessSemantics == "sequence" {
			continue
		}

		module := comp.ExportedModule()
		if module == "" {
			module = fallbackModule
		}

		// Register the type itself if not already known.
		if _, exists := lookupSymbol(qmlName); !exists {
			sym := QMLSymbol{
				Label:    qmlName,
				Kind:     lsp.CompletionItemKindClass,
				Detail:   module + " — " + qmlName,
				Module:   module,
				Category: "type",
			}
			if comp.IsSingleton {
				sym.Kind = lsp.CompletionItemKindModule
				sym.Category = "js"
			}
			registerSymbols(sym)
		}

		// Register the import module if not already known.
		if module != "" {
			if _, exists := lookupSymbol(module); !exists {
				registerSymbols(QMLSymbol{
					Label:     module,
					Kind:      lsp.CompletionItemKindModule,
					Detail:    module + " module",
					Signature: "import " + module,
					Module:    module,
					Category:  "import",
				})
			}
		}

		// Register enums in the flat symbol registry so hover can find them.
		for _, e := range comp.Enums {
			if e.Name == "" {
				continue
			}
			label := qmlName + "." + e.Name
			if _, exists := lookupSymbol(label); exists {
				continue
			}
			desc := strings.Join(e.Values, ", ")
			if len(desc) > 120 {
				desc = desc[:120] + "…"
			}
			registerSymbols(QMLSymbol{
				Label:       label,
				Kind:        lsp.CompletionItemKindEnum,
				Detail:      "enum — " + e.Name + " (" + qmlName + ")",
				Description: desc,
				Module:      module,
				Category:    "type",
			})
		}

		// Register method signatures for signature help.
		registerQMLTypesSignatures(comp, qmlName)
	}
}

// registerQMLTypesSignatures registers method signatures from a parsed
// component into the functionSignatures map used by signature help. Methods
// are registered under both "method" (bare) and "TypeName.method" (dotted)
// keys. Existing hand-coded entries are never overwritten.
func registerQMLTypesSignatures(comp *QMLTypesComponent, qmlName string) {
	for _, m := range comp.Methods {
		if m.Name == "" || m.IsConstructor || m.IsCloned || isInternalName(m.Name) {
			continue
		}
		sig := buildSignatureInfo(m)

		// Register as "TypeName.method" for dotted calls (e.g. Qt.binding).
		dotted := qmlName + "." + m.Name
		if _, exists := functionSignatures[dotted]; !exists {
			functionSignatures[dotted] = sig
		}

		// Register as bare "method" for unqualified calls. Skip if already
		// registered (hand-coded entries or an earlier type's method wins).
		if _, exists := functionSignatures[m.Name]; !exists {
			functionSignatures[m.Name] = sig
		}
	}
}

func buildSignatureInfo(m QMLTypesMethod) lsp.SignatureInformation {
	var params []lsp.ParameterInformation
	var paramLabels []string
	for _, p := range m.Parameters {
		qmlType := cppTypeToQML(p.Type)
		label := p.Name + ": " + qmlType
		paramLabels = append(paramLabels, label)
		params = append(params, lsp.ParameterInformation{
			Label:         label,
			Documentation: plainText(qmlType),
		})
	}
	ret := cppTypeToQML(m.ReturnType)
	sigLabel := m.Name + "(" + strings.Join(paramLabels, ", ") + ")"
	if ret != "void" {
		sigLabel += ": " + ret
	}
	return lsp.SignatureInformation{
		Label:      sigLabel,
		Parameters: params,
	}
}

// isInternalName reports whether a property/signal/method name is a Qt
// implementation detail that should never surface in user-facing completions.
// Qt's qmltypes files expose private C++ glue (`_q_createJSWrapper`,
// `_q_resourceObjectDeleted`, `_q_reregisterTimers`, ...) and the `_` prefix
// is the universal Qt/C++ convention for "not API".
func isInternalName(name string) bool {
	return strings.HasPrefix(name, "_")
}

