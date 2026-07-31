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

// TestSharedWorldFastPathServesStdlibGSXImports pins the LoadRoot rule: a .gsx
// importing a stdlib package (the common case — strings, fmt, time) must still
// take the shared path, because the closure already contains those types. The
// adversarial review demonstrated the opposite: any stdlib .gsx import silently
// fell back to the full per-Module load, evaporating the fast path for most
// real projects.
func TestSharedWorldFastPathServesStdlibGSXImports(t *testing.T) {
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
	write("go.mod", "module example.com/stdgsx\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	write("views/c.gsx", "package views\n\nimport \"strings\"\n\ncomponent C(name string) {\n\t<p>{strings.ToUpper(name)}</p>\n}\n")

	before := sharedWorldFast.Load()
	m := openTiny(t, root, "example.com/stdgsx")
	if _, err := m.externalImporter(); err != nil {
		t.Fatalf("externalImporter: %v", err)
	}
	if got := sharedWorldFast.Load() - before; got != 1 {
		t.Fatalf("shared-world fast path taken %d times for a stdlib-importing module, want 1", got)
	}
}

// TestSharedWorldSurfacesBrokenRuntimeErrors pins that a runtime that fails to
// type-check produces a loud error through the fast path's synthetic entries —
// not silently partial types. `gsx dev` edits the runtime; this is its most
// common failure mode.
func TestSharedWorldSurfacesBrokenRuntimeErrors(t *testing.T) {
	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	runtimeCopy := t.TempDir()
	if err := copyTree(repoRoot, runtimeCopy); err != nil {
		t.Skipf("could not stage a runtime copy: %v", err)
	}
	if err := os.WriteFile(filepath.Join(runtimeCopy, "zzbroken.go"), []byte("package gsx\n\nvar _ = zzUndefinedIdentifier\n"), 0o644); err != nil {
		t.Fatal(err)
	}
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
	write("go.mod", "module example.com/broke\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+runtimeCopy+"\n")
	write("views/c.gsx", "package views\n\ncomponent C(name string) {\n\t<p>{name}</p>\n}\n")

	m := openTiny(t, root, "example.com/broke")
	if _, err := m.externalImporter(); err != nil {
		t.Fatalf("externalImporter itself should not fail (errors surface per package): %v", err)
	}
	m.mu.Lock()
	errs := m.extErrs[gsxRuntimeImportPath]
	m.mu.Unlock()
	if len(errs) == 0 {
		t.Fatal("broken runtime produced no recorded load errors on the fast path: silent partial types")
	}
}

// TestPositionResolverSurvivesFsetRebuild pins the snapshot contract the LSP
// depends on: a retained PackageResult keeps resolving positions against the
// FileSets it was built from, even after the Module rebuilds its fset. The live
// -read version of this raced rebuildFset and returned plausible wrong FILES
// (demonstrated in the adversarial review, both Pos ranges).
func TestPositionResolverSurvivesFsetRebuild(t *testing.T) {
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
	write("go.mod", "module example.com/x\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	// helper.go exists in the manifest from the start; overriding a tracked Go
	// source is what dirties the source inventory and forces the rebuild.
	write("components/helper.go", "package components\n\nconst helperTag = \"one\"\n")
	write("components/card.gsx", "package components\n\ncomponent X(title string) {\n\t<div>{title}{helperTag}</div>\n}\n")
	write("pages/page.gsx", "package pages\n\nimport \"example.com/x/components\"\n\ncomponent Page() {\n\t<main><components.X title=\"hello\"/></main>\n}\n")
	m := openTiny(t, root, "example.com/x")
	comp := filepath.Join(root, "components")
	pr1, err := m.Package(comp)
	if err != nil {
		t.Fatalf("Package: %v", err)
	}
	obj := pr1.Types.Scope().Lookup("X")
	if obj == nil || !obj.Pos().IsValid() {
		t.Fatal("component X not found in analysis result")
	}
	before := pr1.PositionFor(obj.Pos())
	if before.Filename == "" {
		t.Fatalf("component position did not resolve: %+v", before)
	}

	// Force a rebuild: dirty the inventory and re-analyze another dir. Guard
	// against vacuity — the test only means something if the Module really did
	// replace its FileSet underneath the retained result.
	m.mu.Lock()
	oldFset := m.fset
	m.mu.Unlock()
	m.SetOverride(filepath.Join(root, "components", "helper.go"), []byte("package components\n\nconst helperTag = \"two\"\n"))
	if _, err := m.Package(filepath.Join(root, "pages")); err != nil {
		t.Fatalf("second Package: %v", err)
	}
	m.mu.Lock()
	rebuilt := m.fset != oldFset
	m.mu.Unlock()
	if !rebuilt {
		t.Fatal("fset did not rebuild — the scenario this test exists for never happened")
	}

	after := pr1.PositionFor(obj.Pos())
	if after != before {
		t.Fatalf("retained resolver drifted after fset rebuild: %v -> %v", before, after)
	}
}
