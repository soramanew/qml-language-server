package handler

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"time"

	"github.com/owenrumney/go-lsp/lsp"
)

// trsCheckScriptRelPath is the project-relative path of the translation
// checker. If a workspace root ships it, every diagnostic round pipes the
// current buffer through it. Matched by path only, like the conventions
// script.
const trsCheckScriptRelPath = "scripts/trs-check.py"

// trsCheckTimeout bounds a single buffer check — one file on stdin, so this
// is only a hedge against a wedged interpreter.
const trsCheckTimeout = 5 * time.Second

// diagSourceTrsCheck is the diagnostics' `source` field and cache key.
const diagSourceTrsCheck = "trs-check"

// trsCheckLevelError is the script's level for an issue that breaks the
// string for translators; everything else it reports is merely suspicious.
const trsCheckLevelError = "error"

// trsCheckReport is the `--json` payload, written to stderr.
type trsCheckReport struct {
	Issues []trsCheckIssue `json:"issues"`
}

type trsCheckIssue struct {
	scriptRange
	File    string `json:"file"`
	Level   string `json:"level"`
	Rule    string `json:"rule"`
	Message string `json:"message"`
}

// trsCheckRunner drives the per-project translation checker over unsaved
// buffers. Check-only: the script has no `--fix`, so unlike the conventions
// script there is no post-save counterpart. Strictly best-effort — missing
// script → no-op, failing or non-JSON subprocess → no diagnostics.
type trsCheckRunner struct {
	scriptLocator
}

func newTrsCheckRunner(logger *slog.Logger) *trsCheckRunner {
	return &trsCheckRunner{scriptLocator{relPath: trsCheckScriptRelPath, logger: logger}}
}

// SetRoots records the workspace roots to search for the checker script.
func (r *trsCheckRunner) SetRoots(roots []string) {
	if r == nil {
		return
	}
	r.setRoots(roots)
}

// findScript returns the absolute path of the checker script, or "".
func (r *trsCheckRunner) findScript() string {
	if r == nil {
		return ""
	}
	return r.find()
}

// Check runs the translation checker over an unsaved buffer piped in on
// stdin and returns its issues as diagnostics. `--stdin-name` hands the
// script the buffer's real path: it labels the issues with it, uses the
// extension to tell QML from C++, and skips the files implementing the
// helpers themselves. Returns nil on a missing, failing or non-JSON script.
func (r *trsCheckRunner) Check(ctx context.Context, path, source string) []lsp.Diagnostic {
	if r == nil {
		return nil
	}
	out := r.checkBuffer(ctx, source, "--file", "-", "--stdin-name", path, "--json")
	if len(out) == 0 {
		return nil
	}
	var report trsCheckReport
	if err := json.Unmarshal(out, &report); err != nil {
		if r.logger != nil {
			r.logger.Debug("trs-check output not JSON",
				"err", err, "stderr", string(out))
		}
		return nil
	}
	return trsCheckDiagnostics(report.Issues, source)
}

// trsCheckDiagnostics converts issues into LSP diagnostics, mapping the
// script's two levels onto error and warning severity.
func trsCheckDiagnostics(issues []trsCheckIssue, source string) []lsp.Diagnostic {
	if len(issues) == 0 {
		return nil
	}
	lines := strings.Split(source, "\n")
	errSeverity, warnSeverity := lsp.SeverityError, lsp.SeverityWarning
	diags := make([]lsp.Diagnostic, 0, len(issues))
	for _, i := range issues {
		severity := &warnSeverity
		if i.Level == trsCheckLevelError {
			severity = &errSeverity
		}
		diags = append(diags, lsp.Diagnostic{
			Range:    reportedRange(lines, i.scriptRange),
			Severity: severity,
			Source:   diagSourceTrsCheck,
			Code:     ruleCode(i.Rule),
			Message:  i.Message,
		})
	}
	return diags
}
