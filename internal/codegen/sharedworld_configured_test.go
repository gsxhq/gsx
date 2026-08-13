package codegen

import (
	"bytes"
	"path/filepath"
	"sort"
	"testing"

	"github.com/gsxhq/gsx/internal/diag"
)

// The configured-module fixture stages BOTH config-package shapes the shared
// world has to serve at once, because they resolve through different halves of
// the split load:
//
//   - a class merger in the MAIN module (gsxui's `merge/`), whose types come
//     from the retained project source, and whose dir must therefore stay a
//     retained source package even though the world also loads it;
//   - a whole-package filter in a SEPARATE module (the structpages shape),
//     whose types can only come from the world's universe — the reduced project
//     half carries none.
const configuredWorldMerge = `package merge

import "strings"

func Merge(classes []string) string { return strings.Join(classes, " ") }
`

// writeConfiguredWorldModule stages that fixture. mergeSrc is the merger
// package's source so a caller can give the merger an extra main-module import
// (the back-edge shape).
func writeConfiguredWorldModule(t *testing.T, name, mergeSrc string) (root, modPath, filterPath string) {
	t.Helper()
	root = t.TempDir()
	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	modPath = "example.com/" + name
	filterPath = "example.com/" + name + "filters"
	writeFile(t, root, "go.mod", "module "+modPath+"\n\ngo 1.26.1\n\nrequire (\n\tgithub.com/gsxhq/gsx v0.0.0\n\t"+filterPath+" v0.0.0\n)\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n\nreplace "+filterPath+" => ./filters\n")
	writeFile(t, filepath.Join(root, "filters"), "go.mod", "module "+filterPath+"\n\ngo 1.26.1\n")
	writeFile(t, filepath.Join(root, "filters"), "filters.go", "package filters\n\nimport \"strings\"\n\nfunc Shout(s string) string { return strings.ToUpper(s) + \"!\" }\n")
	writeFile(t, filepath.Join(root, "merge"), "merge.go", mergeSrc)
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
	root, modPath, filterPath := writeConfiguredWorldModule(t, "cfgworld", configuredWorldMerge)
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
// that lives in the MAIN module. The world's synthetic entries carry no
// CompiledGoFiles and no syntax, so if the reduced project half stopped
// covering the merger's dir, projectSourcePackages would lose it — and every
// consumer of retained source for that dir (the merger's own type resolution,
// LSP go-to-definition and hover) would go with it.
func TestSharedWorldRetainsMainModuleConfigDir(t *testing.T) {
	root, modPath, filterPath := writeConfiguredWorldModule(t, "cfgretain", configuredWorldMerge)
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

// TestSharedWorldBackedgeFallsBack pins the guard: a config package whose
// closure pulls in main-module packages OUTSIDE the composed config set makes
// the world unservable, so the Module falls back to the single full-mode load
// — visibly (sharedWorldBackedge), and with generation still correct.
func TestSharedWorldBackedgeFallsBack(t *testing.T) {
	mergeSrc := `package merge

import (
	"strings"

	"example.com/cfgbackedge/style"
)

func Merge(classes []string) string { return strings.Join(append(classes, style.Extra), " ") }
`
	root, modPath, filterPath := writeConfiguredWorldModule(t, "cfgbackedge", mergeSrc)
	writeFile(t, filepath.Join(root, "style"), "style.go", "package style\n\nconst Extra = \"extra\"\n")
	views := filepath.Join(root, "views")

	beforeBackedge, beforeFast := sharedWorldBackedge.Load(), sharedWorldFast.Load()
	_, out := generateConfiguredWorld(t, configuredWorldOptions(root, modPath, filterPath), views)
	if got := sharedWorldBackedge.Load() - beforeBackedge; got != 1 {
		t.Fatalf("world back-edge fallbacks = %d, want 1", got)
	}
	if got := sharedWorldFast.Load() - beforeFast; got != 0 {
		t.Fatalf("shared-world fast path taken %d times despite a main-module back-edge, want 0", got)
	}
	var emitted string
	for _, src := range out {
		emitted = string(src)
	}
	if !bytes.Contains([]byte(emitted), []byte("_gsxcm.Merge")) {
		t.Fatalf("fallback generation lost the configured merger:\n%s", emitted)
	}
}
