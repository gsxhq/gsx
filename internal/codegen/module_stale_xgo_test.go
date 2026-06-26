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
// source) and must NOT be influenced by the stale disk content.
//
// Non-vacuity is guaranteed by two complementary checks:
//
//  1. Scope check (direct, mechanical): the stale file declares a unique
//     exported variable StaleMarker. If the skip guard (compsByXGo exclusion
//     in analyze) is removed, the stale file IS fed to the type-checker and
//     StaleMarker lands in the package scope. With the guard intact, StaleMarker
//     is absent — proving the stale file was never seen by the type-checker.
//
//  2. Generate output check: Module.Generate must produce func Home( in the
//     output and must not contain StaleMarker (belt-and-suspenders check that
//     the emitted code is derived from the .gsx, not the stale disk file).
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

	// Stale on-disk .x.go — simulates a previously-generated file left on disk.
	// The unique declaration (var StaleMarker) acts as a canary: if this file is
	// fed to the type-checker its symbol appears in the package scope, making the
	// scope check below fail. A plain func Wrong() {} would not reliably surface
	// because it doesn't conflict with the skeleton and wouldn't affect resolved
	// types or emitted output — this variable is the actual detection mechanism.
	staleXGoPath := filepath.Join(pageDir, "page.x.go")
	staleContent := "package page\n\n// StaleMarker is a canary: its presence in the type-checker scope\n// proves the stale file was wrongly included.\nvar StaleMarker = \"STALE\"\n"
	if err := os.WriteFile(staleXGoPath, []byte(staleContent), 0o644); err != nil {
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

	// --- Check 1: scope-level invariant ---
	// Call analyze directly (same package) to inspect the type-checker's
	// package scope. StaleMarker must NOT be there — if it is, the stale file
	// was included in the type-check (skip guard broken).
	ext, err := m.externalImporter()
	if err != nil {
		t.Fatalf("externalImporter: %v", err)
	}
	mi := &moduleImporter{m: m, external: ext, seen: map[string]bool{}}
	a, err := m.analyze(pageDir, mi)
	if err != nil {
		t.Fatalf("analyze: %v", err)
	}
	if a.pkg != nil {
		if obj := a.pkg.Scope().Lookup("StaleMarker"); obj != nil {
			t.Errorf("stale page.x.go was fed to the type-checker: StaleMarker found in package scope (%v) — skip guard broken", obj)
		}
	}

	// --- Check 2: Generate output ---
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

	// Must NOT contain StaleMarker from the stale on-disk .x.go.
	if strings.Contains(got, "StaleMarker") {
		t.Errorf("generated output contains StaleMarker from stale disk .x.go — Module read stale file:\n%s", got)
	}
}
