package codegen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestModuleIgnoresStaleOnDiskXGo proves the core on-disk-.x.go-independence
// invariant: if a stale generated .x.go exists on disk, Module.Generate must
// produce the correct output from the in-memory skeleton (derived from the .gsx
// source) and must NOT emit the stale disk content.
//
// This also exercises Finding 1's skip logic: the companion-.go inclusion via
// build.ImportDir must skip the live-skeleton overlay path (page.x.go in
// compsByXGo) so the stale disk file is never read into the type-checker.
func TestModuleIgnoresStaleOnDiskXGo(t *testing.T) {
	root := t.TempDir()
	repoRoot, _ := filepath.Abs("../..")
	writeFile(t, root, "go.mod",
		"module example.com/app\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")

	pageDir := filepath.Join(root, "page")
	if err := os.MkdirAll(pageDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Source: a simple component.
	gsxPath := filepath.Join(pageDir, "page.gsx")
	writeFile(t, pageDir, "page.gsx",
		"package page\n\ncomponent Home() {\n\t<h1>hello</h1>\n}\n")

	// Deliberately wrong on-disk .x.go — simulates stale generated file.
	staleXGoPath := filepath.Join(pageDir, "page.x.go")
	if err := os.WriteFile(staleXGoPath, []byte("package page\n\nfunc Wrong() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	m, err := Open(Options{
		ModuleRoot: root,
		ModulePath: "example.com/app",
		FilterPkgs: []string{StdImportPath},
	})
	if err != nil {
		t.Fatal(err)
	}

	out, diags, err := m.Generate(pageDir)
	if err != nil {
		t.Fatalf("Generate: %v (diags=%v)", err, diags)
	}

	got := string(out[gsxPath])
	if got == "" {
		t.Fatalf("Generate produced no output for page.gsx; out=%v", out)
	}

	// Must contain the correct component function.
	if !strings.Contains(got, "func Home(") {
		t.Errorf("generated output missing func Home(; got:\n%s", got)
	}

	// Must NOT contain the stale Wrong() function from the on-disk .x.go.
	if strings.Contains(got, "func Wrong()") {
		t.Errorf("generated output contains stale 'func Wrong()' from disk .x.go — Module read stale file:\n%s", got)
	}
}
