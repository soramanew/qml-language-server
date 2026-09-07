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

	"github.com/owenrumney/go-lsp/lsp"
)

// scriptLocator is the shared half of a project-shipped checker script:
// resolve a project-relative path against the workspace roots, and run the
// script over an unsaved buffer. Scripts are matched by path only — no
// contents or shebang check — so a project can swap in anything speaking
// the same flags.
type scriptLocator struct {
	relPath string
	logger  *slog.Logger

	mu    sync.RWMutex
	roots []string
}

// setRoots records the workspace roots to search. First match wins, so put
// the primary root first when there are several.
func (s *scriptLocator) setRoots(roots []string) {
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
	s.mu.Lock()
	s.roots = abs
	s.mu.Unlock()
}

// find returns the absolute path of the script under the first workspace
// root that contains it, or "" when none do.
func (s *scriptLocator) find() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, r := range s.roots {
		p := filepath.Join(r, s.relPath)
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			return p
		}
	}
	return ""
}

// projectRoot is the directory containing scripts/, i.e. the script's
// grandparent. Running from there matches how a developer would invoke the
// script by hand and lets it find any relative config files.
func projectRoot(script string) string {
	return filepath.Dir(filepath.Dir(script))
}

// checkBuffer runs the script with args and source piped in on stdin, and
// returns its stderr report. The report goes to stderr — stdout is reserved
// for fixed source — and the script exits non-zero on any finding, so exit
// status is ignored and callers key off parseable JSON.
// Returns nil when the script is missing, or the run was cancelled.
func (s *scriptLocator) checkBuffer(ctx context.Context, source string, args ...string) []byte {
	script := s.find()
	if script == "" {
		return nil
	}
	cmd := exec.CommandContext(ctx, script, args...)
	cmd.Dir = projectRoot(script)
	cmd.Stdin = strings.NewReader(source)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	_ = cmd.Run()
	if ctx.Err() != nil {
		return nil
	}
	return bytes.TrimSpace(stderr.Bytes())
}

// scriptRange is the position a checker script reports: a 1-based line and
// column for each end, with the end exclusive — an LSP range in 1-based
// clothing. Embedded in the script's own report type, whose remaining
// fields differ.
type scriptRange struct {
	Line      int `json:"line"`
	Column    int `json:"column"`
	EndLine   int `json:"endLine"`
	EndColumn int `json:"endColumn"`
}

// reportedRange converts a reported range into an LSP one — the same range,
// shifted to 0-based — clamped to the buffer. The script is trusted to pick
// a sensible extent (the conventions script spans the offending line's
// content), so the clamping is defence against a report that raced an edit,
// not second-guessing. Nothing is ever dropped for being out of range: a
// line the buffer no longer has falls back to the whole of the first line,
// columns past the end of a line that shrank clamp to it, and a range that
// comes out empty or inverted widens to the end of its line, since a
// zero-width range renders as a bare caret.
func reportedRange(lines []string, r scriptRange) lsp.Range {
	lineLen := func(i int) int {
		if i < 0 || i >= len(lines) {
			return 0
		}
		return len(lines[i])
	}
	startLine := r.Line - 1
	if startLine < 0 || startLine >= len(lines) {
		// The line is gone, so the columns and the end describe text we
		// can't find either: fall back to the whole of the first line
		// rather than trusting half a stale position.
		return lsp.Range{
			Start: lsp.Position{Line: 0, Character: 0},
			End:   lsp.Position{Line: 0, Character: lineLen(0)},
		}
	}
	endLine := max(startLine, min(r.EndLine-1, len(lines)-1))
	start := max(0, min(r.Column-1, lineLen(startLine)))
	end := max(0, min(r.EndColumn-1, lineLen(endLine)))
	if endLine == startLine && end <= start {
		end = lineLen(startLine)
	}
	return lsp.Range{
		Start: lsp.Position{Line: startLine, Character: start},
		End:   lsp.Position{Line: endLine, Character: end},
	}
}

// ruleCode renders a rule name as a diagnostic code, or nil when unnamed.
func ruleCode(rule string) json.RawMessage {
	if rule == "" {
		return nil
	}
	code, err := json.Marshal(rule)
	if err != nil {
		return nil
	}
	return code
}
