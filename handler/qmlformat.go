package handler

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// qmlformatRunner wraps Qt's qmlformat. It has no stdin mode so we round-
// trip through a temp file; the cost is a single os.CreateTemp + one
// subprocess launch per formatting request, which matches what every
// other qmlformat LSP integration does.
type qmlformatRunner struct {
	binary string
	logger *slog.Logger
}

// newQmlformatRunner picks the best qmlformat on this system. Returns nil
// when none is found — the handler treats that as "formatting disabled"
// and the editor gets no edits, same contract as the qmlls client.
func newQmlformatRunner(logger *slog.Logger) *qmlformatRunner {
	bin := detectQmlformatBinary()
	if bin == "" {
		if logger != nil {
			logger.Info("qmlformat not found; formatting disabled")
		}
		return nil
	}
	if logger != nil {
		logger.Info("qmlformat detected", "path", bin)
	}
	return &qmlformatRunner{binary: bin, logger: logger}
}

// detectQmlformatBinary searches $PATH and the common Qt install
// locations — same strategy qmllint and qmlls use.
func detectQmlformatBinary() string {
	for _, name := range []string{"qmlformat-qt6", "qmlformat6"} {
		if p, err := exec.LookPath(name); err == nil {
			return p
		}
	}
	for _, p := range []string{
		"/usr/lib/qt6/bin/qmlformat",
		"/usr/lib64/qt6/bin/qmlformat",
		"/opt/Qt/6/gcc_64/bin/qmlformat",
	} {
		if _, err := exec.LookPath(p); err == nil {
			return p
		}
	}
	if p, err := exec.LookPath("qmlformat"); err == nil {
		return p
	}
	return ""
}

// Format runs qmlformat on text and returns the formatted output. Errors
// from qmlformat (syntax errors, binary missing) are returned to the
// caller; the Formatting handler treats them as "no edits" rather than
// propagating the error up to the editor, because a non-fatal error
// shouldn't pop a dialog mid-save.
func (r *qmlformatRunner) Format(ctx context.Context, text string) (string, error) {
	if r == nil || r.binary == "" {
		return "", nil
	}
	// qmlformat has no stdin mode. Write to a short-lived temp file in the
	// system temp dir — we never modify the user's actual file.
	f, err := os.CreateTemp("", "qmlformat-*.qml")
	if err != nil {
		return "", err
	}
	tmpPath := f.Name()
	defer os.Remove(tmpPath)
	if _, err := f.WriteString(text); err != nil {
		f.Close()
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	cmd := exec.CommandContext(ctx, r.binary, tmpPath)
	cmd.Dir = filepath.Dir(tmpPath)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if r.logger != nil {
			r.logger.Debug("qmlformat failed", "err", err, "stderr", strings.TrimSpace(stderr.String()))
		}
		return "", err
	}
	return stdout.String(), nil
}
