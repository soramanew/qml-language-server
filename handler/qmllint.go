package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/owenrumney/go-lsp/lsp"
)

// qmllintWarning mirrors the JSON shape emitted by Qt's `qmllint --json -`.
// Line/column are 1-based; length is the byte length of the offending token.
type qmllintWarning struct {
	ID         string `json:"id"`
	Message    string `json:"message"`
	Type       string `json:"type"`
	Line       int    `json:"line"`
	Column     int    `json:"column"`
	Length     int    `json:"length"`
	CharOffset int    `json:"charOffset"`
}

type qmllintFileReport struct {
	Filename string           `json:"filename"`
	Success  bool             `json:"success"`
	Warnings []qmllintWarning `json:"warnings"`
}

type qmllintReport struct {
	Files []qmllintFileReport `json:"files"`
}

// qmllintRunner invokes qmllint as a subprocess and converts its JSON output
// into LSP diagnostics. Requires Qt6-era qmllint (the `--json` flag was added
// in Qt 6.2); Qt5's qmllint only does syntax checking and has no JSON mode, so
// we deliberately skip it and fall back to tree-sitter-only diagnostics.
type qmllintRunner struct {
	binary string
	logger *slog.Logger
}

// newQmllintRunner searches $PATH plus well-known Qt install locations for a
// qmllint binary that supports `--json`. Returns nil when none is found; the
// handler treats that as "lint disabled" without an error.
func newQmllintRunner(logger *slog.Logger) *qmllintRunner {
	bin := detectQmllintBinary()
	if bin == "" {
		if logger != nil {
			logger.Info("qmllint not found; lint diagnostics disabled")
		}
		return nil
	}
	if logger != nil {
		logger.Info("qmllint detected", "path", bin)
	}
	return &qmllintRunner{binary: bin, logger: logger}
}

// detectQmllintBinary picks the best qmllint on this system. Distro packaging
// varies: Fedora ships `qmllint-qt6`, Arch puts it under `/usr/lib/qt6/bin`,
// and a plain `qmllint` on $PATH could be either Qt5 or Qt6 — we still accept
// it, but Qt5's qmllint will be rejected later when --json fails to parse.
func detectQmllintBinary() string {
	pathCandidates := []string{"qmllint-qt6", "qmllint6"}
	for _, name := range pathCandidates {
		if p, err := exec.LookPath(name); err == nil {
			return p
		}
	}
	absoluteCandidates := []string{
		"/usr/lib/qt6/bin/qmllint",
		"/usr/lib64/qt6/bin/qmllint",
		"/opt/Qt/6/gcc_64/bin/qmllint",
	}
	for _, p := range absoluteCandidates {
		if _, err := exec.LookPath(p); err == nil {
			return p
		}
	}
	if p, err := exec.LookPath("qmllint"); err == nil {
		return p
	}
	return ""
}

// Lint runs qmllint against the given source text and returns diagnostics for
// every warning in its output. qmllint only accepts file paths (the `--json -`
// flag writes the report to stdout — it does not mean "read source from
// stdin"), so to lint unsaved editor buffers we write `source` to a hidden
// temp file beside the real file and point qmllint at that. Writing in the
// same directory (rather than /tmp) preserves qmllint's implicit-module
// lookup, which resolves sibling QML components like `MyButton.qml` by
// directory. If the temp write fails (read-only mount, etc.) we lint the
// on-disk file instead — diagnostics may be stale but it beats returning
// nothing. Each entry in importPaths is forwarded as `-I <path>` so qmllint
// can locate project-local QML modules the same way qmlls does for
// completion. Non-zero exit codes are expected (qmllint exits 1 on warnings),
// so we ignore exit status and key off successfully-parsed JSON instead.
// diagSourceQmllint is the `source` field stamped on every qmllint
// diagnostic, and its key in the handler's per-source diagnostic cache.
const diagSourceQmllint = "qmllint"

func (r *qmllintRunner) Lint(ctx context.Context, path, source string, importPaths []string) []lsp.Diagnostic {
	if r == nil || r.binary == "" || path == "" {
		return nil
	}
	lintPath, cleanup := stageLintBuffer(path, source)
	defer cleanup()
	args := make([]string, 0, 3+2*len(importPaths))
	args = append(args, "--json", "-")
	for _, p := range importPaths {
		if p == "" {
			continue
		}
		args = append(args, "-I", p)
	}
	args = append(args, lintPath)
	cmd := exec.CommandContext(ctx, r.binary, args...)
	cmd.Dir = filepath.Dir(path)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	_ = cmd.Run()
	if ctx.Err() != nil {
		return nil
	}
	out := bytes.TrimSpace(stdout.Bytes())
	if len(out) == 0 {
		if r.logger != nil {
			r.logger.Debug("qmllint produced no output", "stderr", stderr.String())
		}
		return nil
	}
	var report qmllintReport
	if err := json.Unmarshal(out, &report); err != nil {
		if r.logger != nil {
			r.logger.Debug("qmllint output not JSON (Qt5 binary?)", "err", err)
		}
		return nil
	}
	return diagnosticsFromReport(&report)
}

// stageLintBuffer writes source to a hidden temp file next to path so qmllint
// can lint an unsaved editor buffer. Returns the path to use for linting and
// a cleanup func the caller must defer. On any I/O failure we fall back to
// the original path with a no-op cleanup — stale-but-real is strictly better
// than no diagnostics at all. The `.qmllsbuf-` prefix is hidden on unix and
// starts with a dot, which guarantees it can never be treated as an implicit
// QML component (component filenames must begin with an uppercase letter).
func stageLintBuffer(path, source string) (string, func()) {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, ".qmllsbuf-*.qml")
	if err != nil {
		return path, func() {}
	}
	tmp := f.Name()
	if _, err := f.WriteString(source); err != nil {
		f.Close()
		os.Remove(tmp)
		return path, func() {}
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return path, func() {}
	}
	return tmp, func() { os.Remove(tmp) }
}

func diagnosticsFromReport(report *qmllintReport) []lsp.Diagnostic {
	var diags []lsp.Diagnostic
	for _, f := range report.Files {
		for _, w := range f.Warnings {
			diags = append(diags, warningToDiagnostic(w))
		}
	}
	return diags
}

// warningToDiagnostic translates a single qmllint warning into its LSP form.
// qmllint occasionally emits warnings with line == 0 (e.g. the "import" class
// of errors that apply to the whole file); we pin those to the start of the
// document so editors still show them.
func warningToDiagnostic(w qmllintWarning) lsp.Diagnostic {
	line := w.Line - 1
	col := w.Column - 1
	length := w.Length
	if line < 0 {
		line = 0
	}
	if col < 0 {
		col = 0
	}
	if length < 0 {
		length = 0
	}
	severity := qmllintSeverity(w.Type)
	diag := lsp.Diagnostic{
		Range: lsp.Range{
			Start: lsp.Position{Line: line, Character: col},
			End:   lsp.Position{Line: line, Character: col + length},
		},
		Severity: &severity,
		Source:   diagSourceQmllint,
		Message:  w.Message,
	}
	if w.ID != "" {
		if code, err := json.Marshal(w.ID); err == nil {
			diag.Code = code
		}
	}
	return diag
}

// lintImportPaths returns the absolute -I paths qmllint should search. It
// looks for a .qmlls.ini first by walking upward from filePath's directory —
// this finds the config regardless of whether workspace roots have been
// populated yet (Initialize runs in a goroutine in go-lsp, so the very first
// DidOpen/DidSave can race ahead of setRoots) — and falls back to scanning
// the known workspace roots if the walk finds nothing. Relative entries in
// the ini are resolved against the ini's directory, matching Qt qmlls.
func (h *Handler) lintImportPaths(filePath string) []string {
	if iniPath := findIniUpward(filePath); iniPath != "" {
		if paths := parseIniPaths(iniPath); paths != nil {
			return paths
		}
	}
	if h == nil || h.workspace == nil {
		return nil
	}
	h.workspace.mu.RLock()
	roots := append([]string{}, h.workspace.roots...)
	h.workspace.mu.RUnlock()
	for _, root := range roots {
		iniPath := filepath.Join(root, ".qmlls.ini")
		if paths := parseIniPaths(iniPath); paths != nil {
			return paths
		}
	}
	return nil
}

// findIniUpward walks from filePath's directory toward the filesystem root,
// returning the path of the first .qmlls.ini found. Returns "" when filePath
// is empty or the walk reaches the root without finding one.
func findIniUpward(filePath string) string {
	if filePath == "" {
		return ""
	}
	dir := filepath.Dir(filePath)
	for {
		candidate := filepath.Join(dir, ".qmlls.ini")
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

// parseIniPaths parses an ini file and returns its resolved import paths, or
// nil if the file is missing/unreadable/empty of import-related keys.
func parseIniPaths(iniPath string) []string {
	cfg, err := ParseQMLLSIni(iniPath)
	if err != nil || cfg == nil {
		return nil
	}
	if cfg.BuildDir == "" && len(cfg.ImportPaths) == 0 {
		return nil
	}
	iniDir := filepath.Dir(iniPath)
	var paths []string
	if cfg.BuildDir != "" {
		paths = append(paths, resolveIniPath(iniDir, cfg.BuildDir))
	}
	for _, p := range cfg.ImportPaths {
		paths = append(paths, resolveIniPath(iniDir, p))
	}
	return paths
}

func resolveIniPath(base, p string) string {
	if filepath.IsAbs(p) {
		return filepath.Clean(p)
	}
	return filepath.Clean(filepath.Join(base, p))
}

func qmllintSeverity(t string) lsp.DiagnosticSeverity {
	switch strings.ToLower(t) {
	case "error", "critical":
		return lsp.SeverityError
	case "info":
		return lsp.SeverityInformation
	case "hint":
		return lsp.SeverityHint
	default:
		return lsp.SeverityWarning
	}
}
