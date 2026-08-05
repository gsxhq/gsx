package gen

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// newNI drives newWith non-interactively (no TTY, no real subprocess) so
// scaffold-style tests never depend on the ambient terminal. workDir anchors
// relative dir resolution (see absAgainst).
func newNI(t *testing.T, workDir string, args ...string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	noop := func(_ []string, _ string, _, _ io.Writer) error { return nil }
	code := newWith(args, strings.NewReader(""), &out, &errb, false, noop, workDir)
	return code, out.String(), errb.String()
}

func TestNewCreatesDir(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	code, out, errb := newNI(t, dir, "myapp")
	if code != 0 {
		t.Fatalf("exit %d; stderr=%q", code, errb)
	}
	gomod, err := os.ReadFile(filepath.Join(dir, "myapp", "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(gomod), "module myapp") {
		t.Fatalf("module not derived from dir basename: %s", gomod)
	}
	if !strings.Contains(out, "npm run dev") {
		t.Fatalf("next steps not printed: %q", out)
	}
}

func TestNewBareNonInteractiveIsUsageError(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	code, _, errb := newNI(t, dir)
	if code != 2 {
		t.Fatalf("expected exit 2, got %d; stderr=%q", code, errb)
	}
	if !strings.Contains(errb, "directory") {
		t.Fatalf("expected usage error mentioning a directory argument, got %q", errb)
	}
	if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
		t.Fatalf("bare non-interactive new must not scaffold into workDir")
	}
}

func TestNewBareInteractivePromptsName(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	calls, run := recordingRunner(-1, nil)
	var out, errb bytes.Buffer
	code := newWith(nil, strings.NewReader("myproj\ny\ny\ny\n"), &out, &errb, true, run, dir)
	if code != 0 {
		t.Fatalf("exit %d; stderr=%q", code, errb.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "myproj", "go.mod")); err != nil {
		t.Fatalf("myproj not scaffolded: %v", err)
	}
	if len(*calls) != 3 {
		t.Fatalf("expected 3 steps, got %d: %v", len(*calls), *calls)
	}
	if !strings.Contains(out.String(), "Project name") {
		t.Fatalf("expected a project-name prompt, got %q", out.String())
	}
}

func TestNewModuleFlag(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	code, _, errb := newNI(t, dir, "--module", "example.com/foo", "myapp")
	if code != 0 {
		t.Fatalf("exit %d; stderr=%q", code, errb)
	}
	gomod, _ := os.ReadFile(filepath.Join(dir, "myapp", "go.mod"))
	if !strings.Contains(string(gomod), "module example.com/foo") {
		t.Fatalf("go.mod = %s", gomod)
	}
	pkg, _ := os.ReadFile(filepath.Join(dir, "myapp", "package.json"))
	if !strings.Contains(string(pkg), "\"name\": \"foo\"") {
		t.Fatalf("package.json name not basename: %s", pkg)
	}
}

func TestNewRefusesExistingProject(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	target := filepath.Join(dir, "myapp")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "go.mod"), []byte("module x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, _, errb := newNI(t, dir, "myapp")
	if code != 2 {
		t.Fatalf("expected exit 2 for existing go.mod, got %d", code)
	}
	if !strings.Contains(errb, "--force") {
		t.Fatalf("error should mention --force: %q", errb)
	}
	// --force proceeds:
	code, _, errb = newNI(t, dir, "--force", "myapp")
	if code != 0 {
		t.Fatalf("--force should succeed, got %d; %q", code, errb)
	}
}

func TestNewFlagsAfterDir(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	code, _, errb := newNI(t, dir, "myapp", "--module", "example.com/after")
	if code != 0 {
		t.Fatalf("exit %d; stderr=%q", code, errb)
	}
	gomod, _ := os.ReadFile(filepath.Join(dir, "myapp", "go.mod"))
	if !strings.Contains(string(gomod), "module example.com/after") {
		t.Fatalf("flag after dir ignored: go.mod = %s", gomod)
	}
}
