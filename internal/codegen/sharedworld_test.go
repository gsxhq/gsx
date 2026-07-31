package codegen

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// copyTree stages a buildable copy of the gsx module: every Go source file plus
// go.mod/go.sum, minus the directories that are large and irrelevant to a build.
// The root package imports internal/ packages, so a partial copy would not
// resolve — the filter is by directory, not by package.
func copyTree(src, dst string) error {
	skip := map[string]bool{
		".git": true, "node_modules": true, ".claude": true,
		"docs": true, "examples": true, "testdata": true, "dist": true,
	}
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, relErr := filepath.Rel(src, path)
		if relErr != nil {
			return relErr
		}
		if d.IsDir() {
			if rel != "." && (skip[d.Name()] || strings.HasPrefix(d.Name(), ".")) {
				return fs.SkipDir
			}
			return os.MkdirAll(filepath.Join(dst, rel), 0o755)
		}
		switch {
		case strings.HasSuffix(path, ".go"), d.Name() == "go.mod", d.Name() == "go.sum":
		default:
			return nil
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		return os.WriteFile(filepath.Join(dst, rel), data, 0o644)
	})
}

// newTinyModule writes a one-package module that replaces the gsx runtime with
// this checkout, so its external closure is byte-identical to every other test
// module's.
func newTinyModule(t *testing.T, pkg string) string {
	t.Helper()
	root := t.TempDir()
	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	write := func(rel, content string) {
		full := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module example.com/"+pkg+"\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	write("views/c.gsx", "package views\n\ncomponent C(name string) {\n\t<p>{name}</p>\n}\n")
	return root
}

func openTiny(t *testing.T, root, modPath string) *Module {
	t.Helper()
	m, err := Open(Options{ModuleRoot: root, ModulePath: modPath, FilterPkgs: []string{StdImportPath}})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return m
}

// TestExternalClosureLoadsOncePerProcess is the point of the shared external
// world: two Modules over different roots share one gsx-runtime closure, so the
// 85-package / 205k-line dependency graph is parsed and type-checked once.
func TestExternalClosureLoadsOncePerProcess(t *testing.T) {
	rootA := newTinyModule(t, "a")
	rootB := newTinyModule(t, "b")

	before := sharedClosureLoads()

	mA := openTiny(t, rootA, "example.com/a")
	if _, err := mA.externalImporter(); err != nil {
		t.Fatalf("module A externalImporter: %v", err)
	}
	mB := openTiny(t, rootB, "example.com/b")
	if _, err := mB.externalImporter(); err != nil {
		t.Fatalf("module B externalImporter: %v", err)
	}

	// At most one: another test in this process may already have loaded the
	// identical closure, in which case both Modules reuse it and the delta is 0.
	// Two would mean no sharing at all, which is the regression this guards.
	if got := sharedClosureLoads() - before; got > 1 {
		t.Fatalf("gsx runtime closure loaded %d times across two Modules, want at most 1", got)
	}
}

// TestExternalClosureReloadsWhenRuntimeChanges is the invalidation guard. `gsx
// dev` edits the gsx runtime itself, so a shared closure keyed only on the load
// paths would serve stale runtime types and silently generate against them —
// worse than being slow.
func TestExternalClosureReloadsWhenRuntimeChanges(t *testing.T) {
	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	// Stage a private copy of the runtime so the edit cannot touch this checkout.
	runtimeCopy := t.TempDir()
	if err := copyTree(repoRoot, runtimeCopy); err != nil {
		t.Skipf("could not stage a runtime copy: %v", err)
	}

	newModuleAgainst := func(pkg string) string {
		root := t.TempDir()
		write := func(rel, content string) {
			full := filepath.Join(root, rel)
			if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		write("go.mod", "module example.com/"+pkg+"\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+runtimeCopy+"\n")
		write("views/c.gsx", "package views\n\ncomponent C(name string) {\n\t<p>{name}</p>\n}\n")
		return root
	}

	mA := openTiny(t, newModuleAgainst("ra"), "example.com/ra")
	impA, err := mA.externalImporter()
	if err != nil {
		t.Fatalf("first externalImporter: %v", err)
	}
	pkgA, err := impA.Import("github.com/gsxhq/gsx")
	if err != nil {
		t.Fatalf("import runtime: %v", err)
	}
	if pkgA.Scope().Lookup("SharedWorldProbe") != nil {
		t.Fatal("probe symbol already present before the runtime was edited")
	}

	// Add an exported symbol to the copied runtime.
	probe := filepath.Join(runtimeCopy, "sharedworldprobe.go")
	if err := os.WriteFile(probe, []byte("package gsx\n\n// SharedWorldProbe exists only for TestExternalClosureReloadsWhenRuntimeChanges.\nconst SharedWorldProbe = 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	mB := openTiny(t, newModuleAgainst("rb"), "example.com/rb")
	impB, err := mB.externalImporter()
	if err != nil {
		t.Fatalf("second externalImporter: %v", err)
	}
	pkgB, err := impB.Import("github.com/gsxhq/gsx")
	if err != nil {
		t.Fatalf("re-import runtime: %v", err)
	}
	if pkgB.Scope().Lookup("SharedWorldProbe") == nil {
		t.Fatal("edited gsx runtime was not observed: the shared closure served stale types")
	}
}

// TestSharedWorldFastPathEngages pins that an ordinary Module — one whose only
// dependency is the gsx runtime — actually takes the shared path. Without this,
// a future tightening of the eligibility rules could silently return every
// Module to a full per-Module load, costing nothing visible except wall time.
func TestSharedWorldFastPathEngages(t *testing.T) {
	before := sharedWorldFast.Load()
	m := openTiny(t, newTinyModule(t, "fastpath"), "example.com/fastpath")
	if _, err := m.externalImporter(); err != nil {
		t.Fatalf("externalImporter: %v", err)
	}
	if got := sharedWorldFast.Load() - before; got != 1 {
		t.Fatalf("shared-world fast path taken %d times for a gsx-only module, want 1", got)
	}
}
