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
	"sync"
	"time"

	"github.com/owenrumney/go-lsp/lsp"
)

// conventionsScriptRelPath is the project-relative path checked after every
// save. If a workspace root contains this file, we invoke it with `--fix`
// from that root so it can rewrite every `*.qml` in place per the
// project's conventions. Matched by path only — no contents or shebang
// check — so projects can swap the script for anything accepting `--fix`.
const conventionsScriptRelPath = "scripts/qml-lint-conventions.py"

// conventionsTimeout bounds a single post-save run. Real-world scripts
// that walk a whole project and run a parser per file typically complete
// well under 10s; 30s gives large repos headroom before we kill the run.
const conventionsTimeout = 30 * time.Second

// conventionsCheckTimeout bounds a single buffer check — one file on stdin,
// not a project walk, so this is only a hedge against a wedged interpreter.
const conventionsCheckTimeout = 5 * time.Second

// diagSourceConventions is the diagnostics' `source` field and cache key.
const diagSourceConventions = "qml-conventions"

// conventionsReport is the `--json` payload, written to stderr because the
// script reserves stdout for fixed source.
type conventionsReport struct {
	Violations []conventionsViolation `json:"violations"`
}

type conventionsViolation struct {
	File    string `json:"file"`
	Line    int    `json:"line"`
	Rule    string `json:"rule"`
	Message string `json:"message"`
}

// conventionsRunner invokes the per-project conventions script after a save.
// It's strictly best-effort: missing script → no-op, failing subprocess →
// logged and otherwise ignored. The script itself rewrites files on disk in
// place; we don't capture or transform its output, we just start it and let
// the editor pick up the file changes through its own watcher.
type conventionsRunner struct {
	logger *slog.Logger

	mu    sync.RWMutex
	roots []string
}

func newConventionsRunner(logger *slog.Logger) *conventionsRunner {
	return &conventionsRunner{logger: logger}
}

// SetRoots records the workspace roots to search for the conventions script.
// First match wins, so put the primary root first when there are several.
func (c *conventionsRunner) SetRoots(roots []string) {
	if c == nil {
		return
	}
	abs := make([]string, 0, len(roots))
	for _, r := range roots {
		if r == "" {
			continue
		}
		if a, err := filepath.Abs(r); err == nil {
			abs = append(abs, a)
		} else {
			abs = append(abs, r)
		}
	}
	c.mu.Lock()
	c.roots = abs
	c.mu.Unlock()
}

// findScript returns the absolute path of the conventions script under the
// first workspace root that contains it, or "" when none do.
func (c *conventionsRunner) findScript() string {
	if c == nil {
		return ""
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	for _, r := range c.roots {
		p := filepath.Join(r, conventionsScriptRelPath)
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			return p
		}
	}
	return ""
}

// Check runs the conventions script over an unsaved buffer piped in on stdin
// and returns the violations as diagnostics. No `--fix`: it would report
// against the source it rewrote, whose lines don't match the editor's buffer.
// Non-zero exit is normal (the script exits 1 on any finding), so we key off
// parseable JSON instead. Returns nil on a missing, failing or non-JSON
// script.
func (c *conventionsRunner) Check(ctx context.Context, source string) []lsp.Diagnostic {
	script := c.findScript()
	if script == "" {
		return nil
	}
	cmd := exec.CommandContext(ctx, script, "--file", "-", "--json")
	cmd.Dir = filepath.Dir(filepath.Dir(script))
	cmd.Stdin = strings.NewReader(source)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	_ = cmd.Run()
	if ctx.Err() != nil {
		return nil
	}
	out := bytes.TrimSpace(stderr.Bytes())
	if len(out) == 0 {
		return nil
	}
	var report conventionsReport
	if err := json.Unmarshal(out, &report); err != nil {
		if c.logger != nil {
			c.logger.Debug("qml-lint-conventions output not JSON",
				"err", err, "stderr", stderr.String())
		}
		return nil
	}
	return conventionsDiagnostics(report.Violations, source)
}

// conventionsDiagnostics converts violations into LSP diagnostics. The script
// reports no column, so each diagnostic spans its whole line.
func conventionsDiagnostics(violations []conventionsViolation, source string) []lsp.Diagnostic {
	if len(violations) == 0 {
		return nil
	}
	lines := strings.Split(source, "\n")
	severity := lsp.SeverityWarning
	diags := make([]lsp.Diagnostic, 0, len(violations))
	for _, v := range violations {
		// 1-based; anything outside the buffer is pinned to line 0 so it
		// still shows up.
		line := v.Line - 1
		if line < 0 || line >= len(lines) {
			line = 0
		}
		end := 0
		if line < len(lines) {
			end = len(lines[line])
		}
		diag := lsp.Diagnostic{
			Range: lsp.Range{
				Start: lsp.Position{Line: line, Character: 0},
				End:   lsp.Position{Line: line, Character: end},
			},
			Severity: &severity,
			Source:   diagSourceConventions,
			Message:  v.Message,
		}
		if v.Rule != "" {
			if code, err := json.Marshal(v.Rule); err == nil {
				diag.Code = code
			}
		}
		diags = append(diags, diag)
	}
	return diags
}

// Run executes the conventions script with `--fix` from its project root so
// it can rewrite every `*.qml` in the project in place. The caller's ctx
// bounds the subprocess; we don't read stdout or otherwise act on the
// script's output — file changes reach the editor through its own file
// watcher. No-op when the script isn't present. Blocks until the script
// exits; callers that want to avoid stalling a save response should invoke
// this in a goroutine.
func (c *conventionsRunner) Run(ctx context.Context) {
	script := c.findScript()
	if script == "" {
		return
	}
	// Project root = the dir that contains scripts/, i.e. the script's
	// grandparent. Running from there matches how a developer would invoke
	// the script by hand and lets it find any relative config files.
	projectRoot := filepath.Dir(filepath.Dir(script))
	cmd := exec.CommandContext(ctx, script, "--fix")
	cmd.Dir = projectRoot
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil && c.logger != nil {
		c.logger.Debug("qml-lint-conventions failed",
			"err", err, "stderr", stderr.String())
	}
}
