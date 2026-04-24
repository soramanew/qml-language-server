package handler

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"
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
