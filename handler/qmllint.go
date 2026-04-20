package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
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

// Lint runs qmllint on path and returns diagnostics for every warning in its
// output. Each entry in importPaths is forwarded as `-I <path>` so qmllint
// can resolve project-local QML modules the same way we do for completion.
// Non-zero exit codes are expected (qmllint exits 1 on warnings), so we
// ignore exit status and key off successfully-parsed JSON instead.
func (r *qmllintRunner) Lint(ctx context.Context, path string, importPaths []string) []lsp.Diagnostic {
	if r == nil || r.binary == "" || path == "" {
		return nil
	}
	args := make([]string, 0, 3+2*len(importPaths))
	args = append(args, "--json", "-")
	for _, p := range importPaths {
		if p == "" {
			continue
		}
		args = append(args, "-I", p)
	}
	args = append(args, path)
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
		Source:   "qmllint",
		Message:  w.Message,
	}
	if w.ID != "" {
		if code, err := json.Marshal(w.ID); err == nil {
			diag.Code = code
		}
	}
	return diag
}

// lintImportPaths returns the absolute -I paths qmllint should search, pulled
// from the first .qmlls.ini it finds under a workspace root. Relative entries
// in the ini (e.g. `buildDir=build`) are resolved against the ini's directory
// rather than the file being linted, matching Qt qmlls's behavior.
func (h *Handler) lintImportPaths() []string {
	if h == nil || h.workspace == nil {
		return nil
	}
	h.workspace.mu.RLock()
	roots := append([]string{}, h.workspace.roots...)
	h.workspace.mu.RUnlock()
	for _, root := range roots {
		iniPath := filepath.Join(root, ".qmlls.ini")
		cfg, err := ParseQMLLSIni(iniPath)
		if err != nil || cfg == nil {
			continue
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
	return nil
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
