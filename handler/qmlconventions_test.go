package handler

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// writeConventionsScript drops an executable shell script at
// <proj>/scripts/qml-lint-conventions.py. We use /bin/sh via a shebang
// rather than real Python so these tests don't depend on a Python
// interpreter being installed.
func writeConventionsScript(t *testing.T, proj, body string) {
	t.Helper()
	dir := filepath.Join(proj, "scripts")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "qml-lint-conventions.py"), []byte(body), 0o755); err != nil {
		t.Fatalf("write script: %v", err)
	}
}

// TestConventionsRunnerRewritesProjectQmlFiles verifies the post-save flow:
// the script runs with cwd=project root and is free to edit every *.qml in
// place. We confirm that by dropping two QML files in the project and
// checking both end up modified.
func TestConventionsRunnerRewritesProjectQmlFiles(t *testing.T) {
	proj := t.TempDir()
	writeConventionsScript(t, proj,
		"#!/bin/sh\nif [ \"$1\" = \"--fix\" ]; then for f in *.qml; do [ -e \"$f\" ] || continue; printf '// fixed\\n' >> \"$f\"; done; fi\n")

	files := []string{"Foo.qml", "Bar.qml"}
	for _, name := range files {
		if err := os.WriteFile(filepath.Join(proj, name), []byte("// before\n"), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	c := newConventionsRunner(nil)
	c.SetRoots([]string{proj})
	c.Run(context.Background())

	for _, name := range files {
		got, err := os.ReadFile(filepath.Join(proj, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if string(got) != "// before\n// fixed\n" {
			t.Errorf("%s = %q, want script-applied edits", name, string(got))
		}
	}
}

// TestConventionsRunnerNoOpWithoutScript locks in the "best effort"
// contract: no script in the project, Run is a silent no-op.
func TestConventionsRunnerNoOpWithoutScript(t *testing.T) {
	proj := t.TempDir()
	c := newConventionsRunner(nil)
	c.SetRoots([]string{proj})
	c.Run(context.Background()) // must not panic or error
}

// TestConventionsRunnerSwallowsFailures proves a broken conventions script
// does not bubble up — Run returns normally so the save response isn't
// held up.
func TestConventionsRunnerSwallowsFailures(t *testing.T) {
	proj := t.TempDir()
	writeConventionsScript(t, proj, "#!/bin/sh\nexit 1\n")
	c := newConventionsRunner(nil)
	c.SetRoots([]string{proj})
	c.Run(context.Background())
}

// TestConventionsRunnerFindScriptReturnsEmptyWhenMissing is a direct unit
// test on the lookup helper — makes sure we don't false-positive on, say,
// a scripts/ directory that lacks the expected file.
func TestConventionsRunnerFindScriptReturnsEmptyWhenMissing(t *testing.T) {
	proj := t.TempDir()
	if err := os.MkdirAll(filepath.Join(proj, "scripts"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	c := newConventionsRunner(nil)
	c.SetRoots([]string{proj})
	if got := c.findScript(); got != "" {
		t.Errorf("findScript = %q, want empty", got)
	}
}
