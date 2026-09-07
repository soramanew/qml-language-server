package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"os/exec"
	"strings"
	"time"

	"github.com/owenrumney/go-lsp/lsp"
)

// conventionsScriptRelPath is the project-relative path checked after every
// save. If a workspace root contains this file, we invoke it with `--fix`
// from that root so it can rewrite every `*.qml` in place per the
// project's conventions.
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
	scriptRange
	File    string `json:"file"`
	Rule    string `json:"rule"`
	Message string `json:"message"`
}

// conventionsRunner drives the per-project conventions script: `Check` turns
// an unsaved buffer into diagnostics, `Run` applies `--fix` after a save.
// Both are strictly best-effort — missing script → no-op, failing subprocess
// → logged and otherwise ignored.
type conventionsRunner struct {
	scriptLocator
}

func newConventionsRunner(logger *slog.Logger) *conventionsRunner {
	return &conventionsRunner{scriptLocator{relPath: conventionsScriptRelPath, logger: logger}}
}

// SetRoots records the workspace roots to search for the conventions script.
func (c *conventionsRunner) SetRoots(roots []string) {
	if c == nil {
		return
	}
	c.setRoots(roots)
}

// findScript returns the absolute path of the conventions script, or "".
func (c *conventionsRunner) findScript() string {
	if c == nil {
		return ""
	}
	return c.find()
}

// Check runs the conventions script over an unsaved buffer piped in on stdin
// and returns the violations as diagnostics. No `--fix`: it would report
// against the source it rewrote, whose lines don't match the editor's buffer.
// Returns nil on a missing, failing or non-JSON script.
func (c *conventionsRunner) Check(ctx context.Context, source string) []lsp.Diagnostic {
	if c == nil {
		return nil
	}
	out := c.checkBuffer(ctx, source, "--file", "-", "--json")
	if len(out) == 0 {
		return nil
	}
	var report conventionsReport
	if err := json.Unmarshal(out, &report); err != nil {
		if c.logger != nil {
			c.logger.Debug("qml-lint-conventions output not JSON",
				"err", err, "stderr", string(out))
		}
		return nil
	}
	return conventionsDiagnostics(report.Violations, source)
}

// conventionsDiagnostics converts violations into LSP diagnostics. Every
// violation is a style nit, so they're uniformly warnings.
func conventionsDiagnostics(violations []conventionsViolation, source string) []lsp.Diagnostic {
	if len(violations) == 0 {
		return nil
	}
	lines := strings.Split(source, "\n")
	severity := lsp.SeverityWarning
	diags := make([]lsp.Diagnostic, 0, len(violations))
	for _, v := range violations {
		diags = append(diags, lsp.Diagnostic{
			Range:    reportedRange(lines, v.scriptRange),
			Severity: &severity,
			Source:   diagSourceConventions,
			Code:     ruleCode(v.Rule),
			Message:  v.Message,
		})
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
	if c == nil {
		return
	}
	script := c.find()
	if script == "" {
		return
	}
	cmd := exec.CommandContext(ctx, script, "--fix")
	cmd.Dir = projectRoot(script)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil && c.logger != nil {
		c.logger.Debug("qml-lint-conventions failed",
			"err", err, "stderr", stderr.String())
	}
}
