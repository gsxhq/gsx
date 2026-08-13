package codegen

import (
	"os"
	"path/filepath"
	"testing"
)

// The out-of-module-import fixture is gsxui's real shape, the one the Task-5
// A/B found the world could not serve: a configured module (main-module class
// merger + out-of-module filter package) whose `.gsx` ALSO imports a package
// from a module NO configuration names — gsxui's site/pages/document.gsx
// importing github.com/gsxhq/vite. Nothing in the config surface reaches it,
// so a world composed from configuration alone lacks its types, and every
// reload of the whole module fell back to the full load.
//
// libSrc/extraSrc are two packages of that out-of-module module so a test can
// stage "the module now imports a package the world does not carry" — the only
// edit that may legitimately re-key the world.
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
	writeFile(t, filepath.Join(root, "lib", "extra"), "extra.go", "package extra\n\nfunc Tag() string { return \"extra\" }\n")
	writeFile(t, filepath.Join(root, "merge"), "merge.go", configuredWorldMerge)
	writeFile(t, filepath.Join(root, "views"), "card.gsx", viewsSrc)
	return root, modPath, filterPath, libPath
}

// outOfModuleViews renders the fixture's views source. extra adds an import of
// the SECOND out-of-module package; body varies the emitted text without
// touching any import.
func outOfModuleViews(libPath, body string, extra bool) string {
	imports := "\t\"github.com/gsxhq/gsx\"\n\t\"" + libPath + "\"\n"
	call := "{lib.Tag()}"
	if extra {
		imports += "\t\"" + libPath + "/extra\"\n"
		call += "{extra.Tag()}"
	}
	return "package views\n\nimport (\n" + imports + ")\n\ncomponent Card(attrs gsx.Attrs, children gsx.Node, label string) {\n\t<section class=\"card\" { attrs... }>" + body + call + "{label |> shout}{children}</section>\n}\n"
}

// TestSharedWorldServesOutOfModuleGsxImport is the fix for the Task-5 finding.
// A `.gsx` import of a package outside the composed configuration used to
// disqualify the WHOLE module from the fast path — and disqualify it
// expensively, after the world and project-half loads had already been paid
// for. The world now composes that gap, so the module is served, and the
// steady-state cost of a reload is the reduced project-half load alone.
func TestSharedWorldServesOutOfModuleGsxImport(t *testing.T) {
	root, modPath, filterPath, libPath := writeOutOfModuleImportFixture(t, "cfgvite",
		outOfModuleViews("example.com/cfgvitelib", "", false))
	views := filepath.Join(root, "views")
	opts := configuredWorldOptions(root, modPath, filterPath)

	beforeFast, beforeFell := sharedWorldFast.Load(), sharedWorldFellBack.Load()
	fast, fastOut := generateConfiguredWorld(t, opts, views)
	if got := sharedWorldFast.Load() - beforeFast; got != 1 {
		t.Fatalf("shared-world fast path taken %d times for an out-of-module .gsx import, want 1", got)
	}
	if got := sharedWorldFellBack.Load() - beforeFell; got != 0 {
		t.Fatalf("coverage fallbacks = %d, want 0: the world was supposed to compose the gap", got)
	}

	// The gap package's types must come from the world's universe like every
	// other external package — one universe, decided numerically.
	fast.mu.Lock()
	libPkg := fast.extPkgs[libPath]
	fast.mu.Unlock()
	if libPkg == nil {
		t.Fatalf("out-of-module package %q is missing from the published external types", libPath)
	}
	if tag := libPkg.Scope().Lookup("Tag"); tag == nil || tag.Pos() < sharedWorldBase {
		t.Fatalf("out-of-module declaration did not come from the world's FileSet: %v", tag)
	}

	// Steady state — what a dev cycle actually pays. A second Module over the
	// same root reuses both world tiers and issues exactly the reduced
	// project-half load. Before this fix the same second Module issued three.
	beforeLoads, beforeWorlds, beforeHits := ProjectLoadCalls(), SharedWorldLoads(), SharedWorldHits()
	_, warmOut := generateConfiguredWorld(t, opts, views)
	if got := ProjectLoadCalls() - beforeLoads; got != 1 {
		t.Fatalf("a warm Module over the same root issued %d packages.Load calls, want 1 (the project half)", got)
	}
	if got := SharedWorldLoads() - beforeWorlds; got != 0 {
		t.Fatalf("warm Module rebuilt the world %d times, want 0", got)
	}
	if got := SharedWorldHits() - beforeHits; got < 2 {
		t.Fatalf("warm Module recorded %d world hits, want both tiers served from the cache", got)
	}
	assertGeneratedEqual(t, fastOut, warmOut)

	// Byte identity against the full load, for the shape that just changed
	// paths: this module took the full load before the fix.
	control := opts
	control.PerDir = map[string]DirOptions{
		filepath.Join(root, "unused"): {ClassMerger: opts.ClassMerger},
	}
	beforeIneligible := sharedWorldIneligible.Load()
	_, controlOut := generateConfiguredWorld(t, control, views)
	if got := sharedWorldIneligible.Load() - beforeIneligible; got != 1 {
		t.Fatalf("control module took the full load %d times, want 1 (the comparison is vacuous otherwise)", got)
	}
	assertGeneratedEqual(t, controlOut, fastOut)
}

// TestSharedWorldKeyFollowsImportsNotEdits pins the churn contract the extended
// composition buys its keying with: the world key must move ONLY when the set
// of packages the module needs from outside itself changes. A body edit — the
// thing a dev loop does every few seconds — must not re-key, or every cycle
// would pay a world rebuild and the phase would be a pessimization.
func TestSharedWorldKeyFollowsImportsNotEdits(t *testing.T) {
	root, modPath, filterPath, libPath := writeOutOfModuleImportFixture(t, "cfgkey",
		outOfModuleViews("example.com/cfgkeylib", "", false))
	views := filepath.Join(root, "views")
	card := filepath.Join(views, "card.gsx")
	opts := configuredWorldOptions(root, modPath, filterPath)

	generateConfiguredWorld(t, opts, views)

	// A body edit: same imports, different text.
	if err := os.WriteFile(card, []byte(outOfModuleViews(libPath, "edited ", false)), 0o644); err != nil {
		t.Fatal(err)
	}
	beforeWorlds := SharedWorldLoads()
	generateConfiguredWorld(t, opts, views)
	if got := SharedWorldLoads() - beforeWorlds; got != 0 {
		t.Fatalf("a .gsx BODY edit rebuilt the world %d times, want 0", got)
	}

	// An import of a package the world does not carry: this one must re-key.
	if err := os.WriteFile(card, []byte(outOfModuleViews(libPath, "edited ", true)), 0o644); err != nil {
		t.Fatal(err)
	}
	beforeWorlds = SharedWorldLoads()
	beforeFast := sharedWorldFast.Load()
	generateConfiguredWorld(t, opts, views)
	if got := SharedWorldLoads() - beforeWorlds; got != 1 {
		t.Fatalf("a NEW out-of-module import rebuilt the world %d times, want 1", got)
	}
	if got := sharedWorldFast.Load() - beforeFast; got != 1 {
		t.Fatalf("the re-keyed module took the shared-world path %d times, want 1", got)
	}
}

// TestSharedWorldIneligibleModulePaysOneLoad pins the cost of the one
// disqualification that is decidable before any load: per-dir config variance.
// Such a module must pay exactly what it paid before the shared world existed —
// a single full-mode load — and no world load at all. The Task-5 A/B found the
// opposite for the coverage fallback (three loads where one would do), which
// the composition above removes; this pins the remaining pre-load path so it
// cannot regress the same way.
func TestSharedWorldIneligibleModulePaysOneLoad(t *testing.T) {
	root, modPath, filterPath := configuredWorldFixture{name: "cfgineligible"}.write(t)
	views := filepath.Join(root, "views")
	opts := configuredWorldOptions(root, modPath, filterPath)
	// Per-dir merger variance: one world cannot serve the module.
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
