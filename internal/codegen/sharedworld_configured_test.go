package codegen

import (
	"bytes"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/gsxhq/gsx/internal/diag"
)

// The configured-module fixture stages BOTH config-package shapes the shared
// world has to serve at once, because they resolve through different halves of
// the split load:
//
//   - a class merger in the MAIN module (gsxui's `merge/`), whose types come
//     from the retained project source and which therefore never enters a
//     world at all — its dir must stay a retained source package, since that
//     retention IS the type authority for it;
//   - a whole-package filter in a SEPARATE module (the structpages shape),
//     whose types can only come from the world's universe — the reduced project
//     half carries none.
const configuredWorldMerge = `package merge

import "strings"

func Merge(classes []string) string { return strings.Join(classes, " ") }
`

const configuredWorldFilter = `package filters

import "strings"

func Shout(s string) string { return strings.ToUpper(s) + "!" }
`

// configuredWorldFixture parameterizes that fixture. The zero merge/filter
// source means the plain shape; a caller overrides one of them to stage a
// back-edge — an OUT-OF-MODULE config package reaching back into the main
// module, the only remaining boundary crossing — and sets filterRequiresMain
// so the filters module can import it.
type configuredWorldFixture struct {
	name                string
	merge               string
	filter              string
	filterRequiresMain  bool
	extraMainModuleFile [2]string // relative dir, source; empty to skip
}

func (f configuredWorldFixture) write(t *testing.T) (root, modPath, filterPath string) {
	t.Helper()
	root = t.TempDir()
	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	modPath = "example.com/" + f.name
	filterPath = "example.com/" + f.name + "filters"
	merge, filter := f.merge, f.filter
	if merge == "" {
		merge = configuredWorldMerge
	}
	if filter == "" {
		filter = configuredWorldFilter
	}
	filterMod := "module " + filterPath + "\n\ngo 1.26.1\n"
	if f.filterRequiresMain {
		// No replace needed: the main module always resolves to itself, which is
		// exactly how a real external package importing back into the project
		// under development resolves (see externalBackedgeTestModule).
		filterMod += "\nrequire " + modPath + " v0.0.0\n"
	}
	writeFile(t, root, "go.mod", "module "+modPath+"\n\ngo 1.26.1\n\nrequire (\n\tgithub.com/gsxhq/gsx v0.0.0\n\t"+filterPath+" v0.0.0\n)\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n\nreplace "+filterPath+" => ./filters\n")
	writeFile(t, filepath.Join(root, "filters"), "go.mod", filterMod)
	writeFile(t, filepath.Join(root, "filters"), "filters.go", filter)
	writeFile(t, filepath.Join(root, "merge"), "merge.go", merge)
	if dir := f.extraMainModuleFile[0]; dir != "" {
		writeFile(t, filepath.Join(root, dir), filepath.Base(dir)+".go", f.extraMainModuleFile[1])
	}
	writeFile(t, filepath.Join(root, "views"), "card.gsx", "package views\n\nimport \"github.com/gsxhq/gsx\"\n\ncomponent Card(attrs gsx.Attrs, children gsx.Node, label string) {\n\t<section class=\"card\" { attrs... }>{label |> shout}{children}</section>\n}\n")
	return root, modPath, filterPath
}

func configuredWorldOptions(root, modPath, filterPath string) Options {
	return Options{
		ModuleRoot:  root,
		ModulePath:  modPath,
		FilterPkgs:  []string{StdImportPath, filterPath},
		ClassMerger: &ClassMergerRef{PkgPath: modPath + "/merge", FuncName: "Merge"},
	}
}

func generateConfiguredWorld(t *testing.T, opts Options, dir string) (*Module, map[string][]byte) {
	t.Helper()
	m, err := Open(opts)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	out, diags, err := m.Generate(dir)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	for _, d := range diags {
		if d.Severity == diag.Error {
			t.Fatalf("Generate diagnostics: %v", diags)
		}
	}
	if len(out) == 0 {
		t.Fatal("Generate produced no output")
	}
	return m, out
}

func assertGeneratedEqual(t *testing.T, want, got map[string][]byte) {
	t.Helper()
	keys := func(m map[string][]byte) []string {
		out := make([]string, 0, len(m))
		for k := range m {
			out = append(out, k)
		}
		sort.Strings(out)
		return out
	}
	wantKeys, gotKeys := keys(want), keys(got)
	if len(wantKeys) != len(gotKeys) {
		t.Fatalf("generated files = %v, want %v", gotKeys, wantKeys)
	}
	for i, k := range wantKeys {
		if gotKeys[i] != k {
			t.Fatalf("generated files = %v, want %v", gotKeys, wantKeys)
		}
		if !bytes.Equal(want[k], got[k]) {
			t.Fatalf("shared-world path emitted different bytes for %s:\n--- full load ---\n%s\n--- shared world ---\n%s", k, want[k], got[k])
		}
	}
}

// TestSharedWorldServesConfiguredModule is the byte-identity proof at unit
// scale: a module configured with a main-module class merger AND an
// out-of-module filter package generates the same bytes on the shared-world
// path as it does on the original single full-mode load. The control is the
// same Options plus a PerDir merger override, which is the one shape
// sharedWorldComposition refuses to compose — so it exercises the full load
// with an otherwise identical configuration and an identical load-path set.
func TestSharedWorldServesConfiguredModule(t *testing.T) {
	root, modPath, filterPath := configuredWorldFixture{name: "cfgworld"}.write(t)
	views := filepath.Join(root, "views")
	opts := configuredWorldOptions(root, modPath, filterPath)

	beforeFast, beforeLoads := sharedWorldFast.Load(), ProjectLoadCalls()
	fast, fastOut := generateConfiguredWorld(t, opts, views)
	if got := sharedWorldFast.Load() - beforeFast; got != 1 {
		t.Fatalf("configured module took the shared-world path %d times, want 1", got)
	}
	// At most the cold world build plus the reduced project half (fewer when
	// the world is already cached). A third load would mean the config types
	// were fetched beside the world instead of from it.
	if got := ProjectLoadCalls() - beforeLoads; got > 2 {
		t.Fatalf("configured shared-world generation issued %d packages.Load calls, want at most 2", got)
	}

	// The out-of-module filter package's types must come from the world's own
	// universe — never a second full-mode load beside it, which would make
	// pointer-identity type matching (renderers, mergers) compare objects from
	// two different type universes. The world reserves a Pos range above
	// sharedWorldBase, so provenance is decidable numerically.
	fast.mu.Lock()
	filterPkg := fast.extPkgs[filterPath]
	fast.mu.Unlock()
	if filterPkg == nil {
		t.Fatalf("filter package %q is missing from the published external types", filterPath)
	}
	shout := filterPkg.Scope().Lookup("Shout")
	if shout == nil {
		t.Fatalf("filter package %q has no Shout", filterPath)
	}
	if shout.Pos() < sharedWorldBase {
		t.Fatalf("filter declaration Pos %d is below sharedWorldBase: the config types came from a second load, not the world", shout.Pos())
	}

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

// TestSharedWorldRetainsMainModuleConfigDir pins retention for a config package
// that lives in the MAIN module — which, since main-module code stopped
// entering worlds, is the ONLY source of types for it. If the reduced project
// half stopped covering the merger's dir, projectSourcePackages would lose it,
// and with it the merger's own type resolution and LSP go-to-definition and
// hover; nothing else carries that dir's syntax. The test predates that change
// and needed no edit for it, which is the point: retained source was always
// the authority here.
func TestSharedWorldRetainsMainModuleConfigDir(t *testing.T) {
	root, modPath, filterPath := configuredWorldFixture{name: "cfgretain"}.write(t)
	views := filepath.Join(root, "views")
	mergeDir := filepath.Join(root, "merge")

	beforeFast := sharedWorldFast.Load()
	m, _ := generateConfiguredWorld(t, configuredWorldOptions(root, modPath, filterPath), views)
	if got := sharedWorldFast.Load() - beforeFast; got != 1 {
		t.Fatalf("configured module took the shared-world path %d times, want 1", got)
	}

	pkg, found, ready := m.targetSourcePackage(mergeDir)
	if !ready {
		t.Fatal("source inventory is not ready after a shared-world generation")
	}
	if !found {
		t.Fatalf("merger dir %s is not a retained source package on the shared-world path", mergeDir)
	}
	mergeFile := filepath.Join(mergeDir, "merge.go")
	if len(pkg.compiledGoFiles) != 1 || pkg.compiledGoFiles[0] != mergeFile {
		t.Fatalf("retained compiled files = %v, want [%s]", pkg.compiledGoFiles, mergeFile)
	}
	if pkg.syntaxByFile[mergeFile] == nil {
		t.Fatalf("retained syntax for %s is missing: LSP nav into the merger would fail", mergeFile)
	}
	// The reduced project half must still carry the target-dependent facts every
	// manual go/types check of this dir needs (sizes, language version).
	if len(pkg.invariantErrors) != 0 {
		t.Fatalf("retained merger package is incomplete: %v", pkg.invariantErrors)
	}
	if _, err := m.typeCheckEnvironmentForDir(mergeDir); err != nil {
		t.Fatalf("type-check environment for the merger dir: %v", err)
	}
	if dir, ok := m.sourcePackageDir(modPath + "/merge"); !ok || dir != mergeDir {
		t.Fatalf("sourcePackageDir(%q) = %q, %v; want %q, true", modPath+"/merge", dir, ok, mergeDir)
	}
}

// TestSharedWorldServesMainModuleConfigDependencies pins what replaced the
// design's "hard case". A class merger in the main module is NOT composed into
// any world tier — its types always came from retained source — so:
//
//   - a merger that imports another main-module package is an ordinary local
//     dependency, not a back-edge: both resolve from source, no main-module
//     code is in the world, and the module is served rather than returned to
//     the full load (which is what this test asserted while the merger was
//     composed);
//   - editing the merger no longer invalidates the world at all. It is a
//     main-module `.go` edit like any other: one project-half reload, zero
//     world loads. While the merger was composed, its files were stamped into
//     every tier, so this edit rebuilt them all — +16% on gsxui's merger cycle,
//     the measurement that motivated the change.
func TestSharedWorldServesMainModuleConfigDependencies(t *testing.T) {
	mergeSrc := `package merge

import (
	"strings"

	"example.com/cfgbackedge/style"
)

func Merge(classes []string) string { return strings.Join(append(classes, style.Extra), " ") }
`
	root, modPath, filterPath := configuredWorldFixture{
		name:                "cfgbackedge",
		merge:               mergeSrc,
		extraMainModuleFile: [2]string{"style", "package style\n\nconst Extra = \"extra\"\n"},
	}.write(t)
	views := filepath.Join(root, "views")
	opts := configuredWorldOptions(root, modPath, filterPath)

	beforeBackedge, beforeFast := SharedWorldBackedgeFallbacks(), sharedWorldFast.Load()
	_, out := generateConfiguredWorld(t, opts, views)
	if got := SharedWorldBackedgeFallbacks() - beforeBackedge; got != 0 {
		t.Fatalf("world back-edge fallbacks = %d, want 0: a main-module merger's main-module import is not a boundary crossing", got)
	}
	if got := sharedWorldFast.Load() - beforeFast; got != 1 {
		t.Fatalf("shared-world fast path taken %d times, want 1", got)
	}
	var emitted string
	for _, src := range out {
		emitted = string(src)
	}
	if !bytes.Contains([]byte(emitted), []byte("_gsxcm.Merge")) {
		t.Fatalf("generation lost the configured merger:\n%s", emitted)
	}

	// The merger edit itself: a behavior change (space-joined to comma-joined),
	// the shape gsxui's wide cycle takes.
	if err := os.WriteFile(filepath.Join(root, "merge", "merge.go"), []byte(`package merge

import (
	"strings"

	"example.com/cfgbackedge/style"
)

func Merge(classes []string) string { return strings.Join(append(classes, style.Extra), ",") }
`), 0o644); err != nil {
		t.Fatal(err)
	}
	beforeWorlds, beforeLoads := SharedWorldLoads(), ProjectLoadCalls()
	generateConfiguredWorld(t, opts, views)
	if got := SharedWorldLoads() - beforeWorlds; got != 0 {
		t.Fatalf("editing the class merger rebuilt %d worlds, want 0: main-module code must not be stamped into any world", got)
	}
	if got := ProjectLoadCalls() - beforeLoads; got != 1 {
		t.Fatalf("a merger edit issued %d packages.Load calls, want 1 (the project half, like any other main-module .go edit)", got)
	}
}

// TestSharedWorldExternalConfigBackedgeFallsBack pins the boundary that
// survives: an OUT-OF-MODULE config package whose closure re-enters the main
// module. Here the filter package calls the project's own merger — gsxui's
// shape inverted — so `merge` enters the world as the filter's dependency and
// the guard's flat ownership test catches it.
//
// This test once needed reachability analysis, because the merger was itself
// composed and therefore exempt from the ownership test; dissolving that
// exemption (main-module code no longer composes) made ownership sufficient.
// The externalImporter side is blind either way — synthetic entries carry no
// Imports — so this world-build guard is the sole defence.
//
// The full load rejects this configuration outright (the filter package's
// dependency graph re-enters the main module), so the world path must too, by
// falling back to the load that produces that rejection.
func TestSharedWorldExternalConfigBackedgeFallsBack(t *testing.T) {
	root, modPath, filterPath := configuredWorldFixture{
		name: "cfghole",
		filter: `package filters

import "example.com/cfghole/merge"

func Shout(s string) string { return merge.Merge([]string{s, "!"}) }
`,
		filterRequiresMain: true,
	}.write(t)

	beforeBackedge, beforeFast := SharedWorldBackedgeFallbacks(), sharedWorldFast.Load()
	m, err := Open(configuredWorldOptions(root, modPath, filterPath))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	out, _, err := m.Generate(filepath.Join(root, "views"))
	if err == nil || !strings.Contains(err.Error(), "crosses the external-to-main-module semantic boundary") {
		t.Fatalf("Generate error = %v (%d files emitted), want the configured-package boundary error", err, len(out))
	}
	if got := SharedWorldBackedgeFallbacks() - beforeBackedge; got != 1 {
		t.Fatalf("world back-edge fallbacks = %d, want 1", got)
	}
	if got := sharedWorldFast.Load() - beforeFast; got != 0 {
		t.Fatalf("shared-world fast path taken %d times for a config package that re-enters the main module, want 0", got)
	}
}

// TestSharedWorldDoesNotCacheUnhealthyWorlds pins the admission rule that closed
// the adversarial review's worst finding. A config package that is broken in a
// FILELESS way — build constraints excluding every file, an empty directory, a
// package that does not exist yet — comes back from go/packages as a non-nil,
// EMPTY types.Package carrying an error, and contributes no files to stamp. A
// world holding one is therefore unfalsifiable: fresh() has nothing that can
// fail, no key component moves when the developer fixes the file, and every
// later analysis in the process is served the stale failure. The review wedged
// exactly that, for the life of the process.
//
// So an unhealthy world is served (its errors must surface loudly — #178) and
// NOT published. While broken, each cycle reloads — the pre-shared-world cost,
// for precisely the broken shape — and the cycle after the fix is healthy,
// cached, and correct.
func TestSharedWorldDoesNotCacheUnhealthyWorlds(t *testing.T) {
	root, modPath, filterPath := configuredWorldFixture{
		name:   "cfgunhealthy",
		filter: "//go:build ignore\n\n" + configuredWorldFilter,
	}.write(t)
	views := filepath.Join(root, "views")
	opts := configuredWorldOptions(root, modPath, filterPath)

	before := sharedWorldCacheSize()
	m, err := Open(opts)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	_, _, genErr := m.Generate(views)
	if genErr == nil {
		t.Fatal("a constraint-excluded filter package generated cleanly: the world's errors did not surface")
	}
	if !strings.Contains(genErr.Error(), "build constraints exclude all Go files") {
		t.Fatalf("Generate error = %v, want the go-list error the world carried", genErr)
	}
	if got := sharedWorldCacheSize() - before; got != 0 {
		t.Fatalf("world cache grew by %d entries for a broken closure, want 0: caching it is what makes the failure permanent", got)
	}

	// Fix it on disk. A new Module in the SAME process must see the repair —
	// this is the wedge: nothing about the key or the stamps changed, so only
	// the refusal to cache can heal it.
	if err := os.WriteFile(filepath.Join(root, "filters", "filters.go"), []byte(configuredWorldFilter), 0o644); err != nil {
		t.Fatal(err)
	}
	beforeHealthy := sharedWorldCacheSize()
	fixed, out := generateConfiguredWorld(t, opts, views)
	if got := sharedWorldCacheSize() - beforeHealthy; got != 1 {
		t.Fatalf("the healthy world was cached %d times, want 1", got)
	}
	if len(out) == 0 {
		t.Fatal("no output after the fix")
	}
	if fset := fixed.snapshotSharedFset(); fset == nil {
		t.Fatal("the healed cycle did not take the world path")
	}
}

// TestSharedWorldRendererTypeIdentityMatchesFullLoad is acceptance gate 3: a
// registered renderer whose TYPE is matched in a `.gsx` expression must behave
// identically on the fast path. Renderer matching is by type identity, and on
// the fast path the renderer package's types come from the WORLD's universe
// while the consuming package is type-checked in the Module's own — the exact
// cross-universe shape this design exists to keep singular. If the two ever
// diverged, the match would silently stop firing and the emitted call would
// change shape, which is what the byte comparison against the full-load control
// detects.
func TestSharedWorldRendererTypeIdentityMatchesFullLoad(t *testing.T) {
	root := t.TempDir()
	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	const modPath = "example.com/rndhost"
	const rndPath = "example.com/rndpkg"
	writeFile(t, root, "go.mod", "module "+modPath+"\n\ngo 1.26.1\n\nrequire (\n\tgithub.com/gsxhq/gsx v0.0.0\n\t"+rndPath+" v0.0.0\n)\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n\nreplace "+rndPath+" => ./rnd\n")
	writeFile(t, filepath.Join(root, "rnd"), "go.mod", "module "+rndPath+"\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	writeFile(t, filepath.Join(root, "rnd"), "rnd.go", `package rnd

import "github.com/gsxhq/gsx"

type Widget struct{ Label string }

func Render(w Widget) gsx.Node { return gsx.Raw("<b>" + w.Label + "</b>") }
`)
	writeFile(t, filepath.Join(root, "views"), "card.gsx", "package views\n\nimport \""+rndPath+"\"\n\ncomponent Card(w rnd.Widget) {\n\t<div>{w}</div>\n}\n")

	views := filepath.Join(root, "views")
	opts := Options{
		ModuleRoot: root,
		ModulePath: modPath,
		FilterPkgs: []string{StdImportPath},
		Renderers: []RendererAlias{{
			TypeKey:  rndPath + ".Widget",
			PkgPath:  rndPath,
			FuncName: "Render",
		}},
	}

	beforeFast := sharedWorldFast.Load()
	_, fastOut := generateConfiguredWorld(t, opts, views)
	if got := sharedWorldFast.Load() - beforeFast; got != 1 {
		t.Fatalf("the renderer fixture took the shared-world path %d times, want 1", got)
	}
	var emitted string
	for _, src := range fastOut {
		emitted = string(src)
	}
	if !strings.Contains(emitted, "Render(") {
		t.Fatalf("the registered renderer did not fire on the fast path — type identity was lost:\n%s", emitted)
	}

	control := opts
	control.PerDir = map[string]DirOptions{
		filepath.Join(root, "unused"): {FilterPkgs: []string{StdImportPath, "example.com/nope"}},
	}
	beforeIneligible := sharedWorldIneligible.Load()
	_, controlOut := generateConfiguredWorld(t, control, views)
	if got := sharedWorldIneligible.Load() - beforeIneligible; got != 1 {
		t.Fatalf("control module took the full load %d times, want 1", got)
	}
	assertGeneratedEqual(t, controlOut, fastOut)
}
