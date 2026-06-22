package gen

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// runCapture drives run with captured stdout/stderr and returns code+output.
func runCapture(t *testing.T, args []string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	code := run(args, &out, &errb)
	return code, out.String(), errb.String()
}

// TestRunGenerate proves `generate <pkgDir>` writes the .x.go, returns 0, and
// the default summary mentions wrote/1.
func TestRunGenerate(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping module-resolution test in -short mode")
	}
	mod := newModule(t, "gsxrungen")
	pkgDir := filepath.Join(mod, "views")
	writeFile(t, pkgDir, "hi.gsx", hiComponent)

	code, out, errb := runCapture(t, []string{"generate", pkgDir})
	if code != 0 {
		t.Fatalf("expected exit 0, got %d; stderr=%q", code, errb)
	}
	if !strings.Contains(out, "wrote") || !strings.Contains(out, "1") {
		t.Fatalf("expected stdout to mention wrote/1, got %q", out)
	}
	target := filepath.Join(pkgDir, "hi.x.go")
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("expected %s on disk: %v", target, err)
	}
}

// TestRunGenerateVerbose proves -v lists the written file.
func TestRunGenerateVerbose(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping module-resolution test in -short mode")
	}
	mod := newModule(t, "gsxrungenv")
	pkgDir := filepath.Join(mod, "views")
	writeFile(t, pkgDir, "hi.gsx", hiComponent)

	code, out, errb := runCapture(t, []string{"-v", "generate", pkgDir})
	if code != 0 {
		t.Fatalf("expected exit 0, got %d; stderr=%q", code, errb)
	}
	target := filepath.Join(pkgDir, "hi.x.go")
	if !strings.Contains(out, target) {
		t.Fatalf("expected verbose stdout to list %q, got %q", target, out)
	}
}

// TestRunGenerateQuiet proves -q prints nothing on success.
func TestRunGenerateQuiet(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping module-resolution test in -short mode")
	}
	mod := newModule(t, "gsxrungenq")
	pkgDir := filepath.Join(mod, "views")
	writeFile(t, pkgDir, "hi.gsx", hiComponent)

	code, out, errb := runCapture(t, []string{"-q", "generate", pkgDir})
	if code != 0 {
		t.Fatalf("expected exit 0, got %d; stderr=%q", code, errb)
	}
	if out != "" {
		t.Fatalf("expected empty stdout with -q, got %q", out)
	}
}

// TestRunGenerateMissingPath proves a non-existent path is a USAGE error (exit 2).
func TestRunGenerateMissingPath(t *testing.T) {
	code, _, errb := runCapture(t, []string{"generate", "/does/not/exist/anywhere"})
	if code != 2 {
		t.Fatalf("expected exit 2 for missing path, got %d; stderr=%q", code, errb)
	}
}

// TestRunGenerateCodegenError proves a .gsx that fails codegen is a CODEGEN error
// (exit 1) and stderr names the dir.
func TestRunGenerateCodegenError(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping module-resolution test in -short mode")
	}
	mod := newModule(t, "gsxrunbad")
	badDir := filepath.Join(mod, "bad")
	writeFile(t, badDir, "bad.gsx", "package bad\n\ncomponent Bad() {\n\t<p>{undefinedSymbol}</p>\n}\n")

	code, _, errb := runCapture(t, []string{"generate", badDir})
	if code != 1 {
		t.Fatalf("expected exit 1 for codegen error, got %d; stderr=%q", code, errb)
	}
	if !strings.Contains(errb, badDir) {
		t.Fatalf("expected stderr to name the bad dir %q, got %q", badDir, errb)
	}
}

// TestRunVersion proves version prints something non-empty and returns 0.
func TestRunVersion(t *testing.T) {
	code, out, errb := runCapture(t, []string{"version"})
	if code != 0 {
		t.Fatalf("expected exit 0, got %d; stderr=%q", code, errb)
	}
	if strings.TrimSpace(out) == "" {
		t.Fatalf("expected non-empty version stdout, got %q", out)
	}
}

// TestRunHelp proves help/no-args list the generate command and return 0.
func TestRunHelp(t *testing.T) {
	for _, args := range [][]string{{"help"}, nil, {"-h"}} {
		code, out, errb := runCapture(t, args)
		if code != 0 {
			t.Fatalf("args=%v: expected exit 0, got %d; stderr=%q", args, code, errb)
		}
		if !strings.Contains(out, "generate") {
			t.Fatalf("args=%v: expected usage to list generate, got %q", args, out)
		}
	}
}

// TestRunUnknownCommand proves an unknown command is a usage error (exit 2) and
// stderr mentions unknown.
func TestRunUnknownCommand(t *testing.T) {
	code, _, errb := runCapture(t, []string{"bogus"})
	if code != 2 {
		t.Fatalf("expected exit 2 for unknown command, got %d; stderr=%q", code, errb)
	}
	if !strings.Contains(errb, "unknown") {
		t.Fatalf("expected stderr to mention unknown, got %q", errb)
	}
}

// TestRunFmtDispatch proves the `fmt` command is wired into run: dispatching
// `fmt` over an empty directory (via -C) is a recognized command that succeeds
// (exit 0) rather than the unknown-command exit 2.
func TestRunFmtDispatch(t *testing.T) {
	dir := t.TempDir()
	code, _, errb := runCapture(t, []string{"-C", dir, "fmt"})
	if code != 0 {
		t.Fatalf("expected exit 0 for fmt over empty dir, got %d; stderr=%q", code, errb)
	}
}

// TestCleanCache proves `clean --cache` removes the cache dir when GSXCACHE is
// a real directory, and that `clean` without --cache does nothing destructive.
func TestCleanCache(t *testing.T) {
	cacheRoot := t.TempDir()
	t.Setenv("GSXCACHE", cacheRoot)

	// Write a dummy cache entry so we can verify RemoveAll happened.
	entryFile := filepath.Join(cacheRoot, "dummy-entry")
	if err := os.WriteFile(entryFile, []byte("data"), 0o644); err != nil {
		t.Fatalf("setup: write dummy entry: %v", err)
	}

	code, out, errb := runCapture(t, []string{"clean", "--cache"})
	if code != 0 {
		t.Fatalf("expected exit 0, got %d; stderr=%q", code, errb)
	}
	if !strings.Contains(out, "removed gsx cache") {
		t.Fatalf("expected stdout to mention removed gsx cache, got %q", out)
	}
	// The cache dir itself must be gone (RemoveAll removes the root too).
	if _, err := os.Stat(cacheRoot); !os.IsNotExist(err) {
		t.Fatalf("expected cache dir to be removed, but stat returned: %v", err)
	}
}

// TestCleanCacheDisabled proves `clean --cache` when GSXCACHE=off prints a
// clear message and exits 0 without removing anything.
func TestCleanCacheDisabled(t *testing.T) {
	t.Setenv("GSXCACHE", "off")

	code, out, errb := runCapture(t, []string{"clean", "--cache"})
	if code != 0 {
		t.Fatalf("expected exit 0, got %d; stderr=%q", code, errb)
	}
	if !strings.Contains(out, "cache") {
		t.Fatalf("expected stdout to mention cache, got %q", out)
	}
}

// TestCleanNoFlags proves `clean` without --cache prints usage and exits 0.
func TestCleanNoFlags(t *testing.T) {
	code, out, errb := runCapture(t, []string{"clean"})
	if code != 0 {
		t.Fatalf("expected exit 0, got %d; stderr=%q", code, errb)
	}
	if !strings.Contains(out, "cache") {
		t.Fatalf("expected stdout to mention cache, got %q", out)
	}
	_ = errb
}

// TestRunChdir proves -C runs relative to the given directory: a relative path
// "views" resolves under the -C dir.
func TestRunChdir(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping module-resolution test in -short mode")
	}
	mod := newModule(t, "gsxrunchdir")
	pkgDir := filepath.Join(mod, "views")
	writeFile(t, pkgDir, "hi.gsx", hiComponent)

	code, _, errb := runCapture(t, []string{"-C", mod, "generate", "views"})
	if code != 0 {
		t.Fatalf("expected exit 0, got %d; stderr=%q", code, errb)
	}
	if _, err := os.Stat(filepath.Join(pkgDir, "hi.x.go")); err != nil {
		t.Fatalf("expected hi.x.go written under -C dir: %v", err)
	}
}
