package codegen

import (
	"path/filepath"
	"testing"
)

// The out-of-module-import fixture is gsxui's real shape: a configured module
// (main-module class merger + out-of-module filter package) whose `.gsx` ALSO
// imports a package from a module NO configuration names — gsxui's
// site/pages/document.gsx importing github.com/gsxhq/vite. The world serves
// what the configuration names, so this module is not servable, and the point
// of the fixture is that finding that out must cost nothing.
func writeOutOfModuleImportFixture(t *testing.T, name, viewsSrc string) (root, modPath, filterPath, libPath string) {
	t.Helper()
	root = t.TempDir()
	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	modPath = "example.com/" + name
	filterPath = "example.com/" + name + "filters"
	libPath = "example.com/" + name + "lib"
	writeFile(t, root, "go.mod", "module "+modPath+"\n\ngo 1.26.1\n\nrequire (\n\tgithub.com/gsxhq/gsx v0.0.0\n\t"+filterPath+" v0.0.0\n\t"+libPath+" v0.0.0\n)\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n\nreplace "+filterPath+" => ./filters\n\nreplace "+libPath+" => ./lib\n")
	writeFile(t, filepath.Join(root, "filters"), "go.mod", "module "+filterPath+"\n\ngo 1.26.1\n")
	writeFile(t, filepath.Join(root, "filters"), "filters.go", configuredWorldFilter)
	writeFile(t, filepath.Join(root, "lib"), "go.mod", "module "+libPath+"\n\ngo 1.26.1\n")
	writeFile(t, filepath.Join(root, "lib"), "lib.go", "package lib\n\nfunc Tag() string { return \"lib\" }\n")
	writeFile(t, filepath.Join(root, "merge"), "merge.go", configuredWorldMerge)
	writeFile(t, filepath.Join(root, "views"), "card.gsx", viewsSrc)
	return root, modPath, filterPath, libPath
}

func outOfModuleViews(libPath string) string {
	return "package views\n\nimport (\n\t\"github.com/gsxhq/gsx\"\n\t\"" + libPath + "\"\n)\n\ncomponent Card(attrs gsx.Attrs, children gsx.Node, label string) {\n\t<section class=\"card\" { attrs... }>{lib.Tag()}{label |> shout}{children}</section>\n}\n"
}

// TestSharedWorldOutOfConfigImportRefusedForFree pins the cost of the shape the
// world cannot serve. A `.gsx` import of a package outside the configured
// closure needs types no world composed from this module's configuration
// carries, so the Module takes the single full-mode load it took before the
// shared world existed — and takes it BEFORE loading anything else, so it pays
// exactly one packages.Load, not three. Task 5 measured what the third load
// costs: 12–18% on every gsxui dev cycle.
//
// The byte-identity half matters just as much: refusing must produce the same
// output as before, which is what the control comparison pins.
func TestSharedWorldOutOfConfigImportRefusedForFree(t *testing.T) {
	root, modPath, filterPath, _ := writeOutOfModuleImportFixture(t, "cfgvite",
		outOfModuleViews("example.com/cfgvitelib"))
	views := filepath.Join(root, "views")
	opts := configuredWorldOptions(root, modPath, filterPath)

	beforeFast, beforePre := sharedWorldFast.Load(), SharedWorldPreloadFallbacks()
	beforeLoads, beforeWorlds := ProjectLoadCalls(), SharedWorldLoads()
	_, out := generateConfiguredWorld(t, opts, views)
	if got := ProjectLoadCalls() - beforeLoads; got != 1 {
		t.Fatalf("an unservable module issued %d packages.Load calls, want exactly 1 (the pre-shared-world cost)", got)
	}
	if got := SharedWorldLoads() - beforeWorlds; got != 0 {
		t.Fatalf("an unservable module built %d worlds, want 0 — the refusal must come before any world load", got)
	}
	if got := SharedWorldPreloadFallbacks() - beforePre; got != 1 {
		t.Fatalf("pre-load refusals = %d, want 1", got)
	}
	if got := sharedWorldFast.Load() - beforeFast; got != 0 {
		t.Fatalf("shared-world fast path taken %d times for an out-of-config import, want 0", got)
	}

	// Same Module shape, forced down the full load by per-dir config variance:
	// the bytes must match, because refusing is supposed to be invisible.
	control := opts
	control.PerDir = map[string]DirOptions{
		filepath.Join(root, "unused"): {ClassMerger: opts.ClassMerger},
	}
	_, controlOut := generateConfiguredWorld(t, control, views)
	assertGeneratedEqual(t, controlOut, out)
}

// TestSharedWorldStdlibLoadRootsAreNotRefusedUpFront is the counterweight to
// the refusal above, and the #178 review's lesson in test form. The pre-load
// scan cannot know WHICH stdlib packages the world's closure reaches, so it
// admits every stdlib-shaped load root and lets the post-load coverage check
// decide. Refusing them up front would be catastrophic in the common direction:
// a `.gsx` importing "strings" — which the closure does carry — must stay on
// the fast path (TestSharedWorldFastPathServesStdlibGSXImports pins that side).
//
// This test pins the other side, net/http, which the gsx runtime's stdlib-only
// closure does NOT reach: admitted by the pre-load scan, declined by coverage,
// served by the single full load — the same verdict, and the same bytes, as
// before the shared world existed.
func TestSharedWorldStdlibLoadRootsAreNotRefusedUpFront(t *testing.T) {
	root := t.TempDir()
	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, root, "go.mod", "module example.com/stdroot\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	writeFile(t, filepath.Join(root, "views"), "card.gsx", "package views\n\nimport \"net/http\"\n\ncomponent Card(r *http.Request) {\n\t<p>{r.URL.Path}</p>\n}\n")

	views := filepath.Join(root, "views")
	opts := Options{ModuleRoot: root, ModulePath: "example.com/stdroot", FilterPkgs: []string{StdImportPath}}
	beforePre, beforeFell := SharedWorldPreloadFallbacks(), SharedWorldCoverageFallbacks()
	_, out := generateConfiguredWorld(t, opts, views)
	if got := SharedWorldPreloadFallbacks() - beforePre; got != 0 {
		t.Fatalf("pre-load refusals = %d for a stdlib load root, want 0: the scan must admit stdlib shapes and let coverage decide", got)
	}
	if got := SharedWorldCoverageFallbacks() - beforeFell; got != 1 {
		t.Fatalf("coverage fallbacks = %d, want 1: net/http is not in the gsx runtime's closure, so the world cannot serve it", got)
	}

	// A PerDir non-std filter package makes the module uncomposable, forcing the
	// same single full load by a different route.
	control := opts
	control.PerDir = map[string]DirOptions{filepath.Join(root, "unused"): {FilterPkgs: []string{StdImportPath, "example.com/nope"}}}
	beforeIneligible := sharedWorldIneligible.Load()
	_, controlOut := generateConfiguredWorld(t, control, views)
	if got := sharedWorldIneligible.Load() - beforeIneligible; got != 1 {
		t.Fatalf("control took the full load %d times, want 1", got)
	}
	assertGeneratedEqual(t, controlOut, out)
}

// TestSharedWorldUnservableVerdictIsRemembered pins the other half of the cost
// story. The coverage check can only fire AFTER the project half and the world
// have loaded — three loads — because the references it examines are imports of
// the module's own files, which nothing sees until they are loaded. A dev loop
// re-runs this on every `.go` edit, so the verdict is latched: the second
// analysis of the same Module is back to the pre-shared-world one load.
func TestSharedWorldUnservableVerdictIsRemembered(t *testing.T) {
	root, modPath, filterPath, libPath := writeOutOfModuleImportFixture(t, "cfgremember",
		"package views\n\nimport \"github.com/gsxhq/gsx\"\n\ncomponent Card(attrs gsx.Attrs, children gsx.Node, label string) {\n\t<section class=\"card\" { attrs... }>{label |> shout}{children}</section>\n}\n")
	// A GO file — invisible to the manifest's load roots — imports the
	// out-of-module package, so only the post-load coverage check can catch it.
	writeFile(t, filepath.Join(root, "views"), "helper.go", "package views\n\nimport \""+libPath+"\"\n\nvar _ = lib.Tag\n")
	views := filepath.Join(root, "views")
	opts := configuredWorldOptions(root, modPath, filterPath)

	m, err := Open(opts)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	beforeFell := SharedWorldCoverageFallbacks()
	if _, _, err := m.Generate(views); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if got := SharedWorldCoverageFallbacks() - beforeFell; got != 1 {
		t.Fatalf("coverage fallbacks = %d, want 1: a Go-file import outside the configured closure is only visible after the project half loads", got)
	}
	if fset := m.snapshotSharedFset(); fset != nil {
		t.Fatal("a fallback left the Module holding a shared-world FileSet: positions would resolve against a world this analysis does not use")
	}

	// Second analysis of the SAME Module — what the next dev cycle does.
	m.rebuildFset()
	beforeLoads, beforeWorlds := ProjectLoadCalls(), SharedWorldLoads()
	beforePre := SharedWorldPreloadFallbacks()
	if _, _, err := m.Generate(views); err != nil {
		t.Fatalf("second Generate: %v", err)
	}
	if got := ProjectLoadCalls() - beforeLoads; got != 1 {
		t.Fatalf("the second analysis of an unservable Module issued %d packages.Load calls, want 1", got)
	}
	if got := SharedWorldLoads() - beforeWorlds; got != 0 {
		t.Fatalf("the second analysis built %d worlds, want 0", got)
	}
	if got := SharedWorldPreloadFallbacks() - beforePre; got != 1 {
		t.Fatalf("remembered refusals = %d, want 1", got)
	}
}

// TestSharedWorldIneligibleModulePaysOneLoad pins the cost of the one
// disqualification that is decidable from configuration alone: per-dir config
// variance. Such a module must pay exactly what it paid before the shared world
// existed — a single full-mode load — and no world load at all.
func TestSharedWorldIneligibleModulePaysOneLoad(t *testing.T) {
	root, modPath, filterPath := configuredWorldFixture{name: "cfgineligible"}.write(t)
	views := filepath.Join(root, "views")
	opts := configuredWorldOptions(root, modPath, filterPath)
	opts.PerDir = map[string]DirOptions{
		views: {ClassMerger: &ClassMergerRef{PkgPath: modPath + "/merge", FuncName: "Merge"}},
	}

	beforeLoads, beforeWorlds := ProjectLoadCalls(), SharedWorldLoads()
	beforeIneligible := sharedWorldIneligible.Load()
	generateConfiguredWorld(t, opts, views)
	if got := ProjectLoadCalls() - beforeLoads; got != 1 {
		t.Fatalf("an ineligible module issued %d packages.Load calls, want exactly 1 (the pre-shared-world cost)", got)
	}
	if got := SharedWorldLoads() - beforeWorlds; got != 0 {
		t.Fatalf("an ineligible module built %d worlds, want 0", got)
	}
	if got := sharedWorldIneligible.Load() - beforeIneligible; got != 1 {
		t.Fatalf("ineligible fallbacks = %d, want 1", got)
	}
}
