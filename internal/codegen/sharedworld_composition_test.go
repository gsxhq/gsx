package codegen

import (
	"slices"
	"testing"
)

// TestSharedWorldOriginFollowsWholeModuleGraph pins what the world key knows
// about WHERE a project's packages resolve from. The origin used to hash only
// the go.mod lines that named a composed package; the adversarial review probed
// what that misses and found it serves stale types with no signal: bumping a
// DEPENDENCY of a composed package (or swapping its replace target) changes the
// types that package is built from while touching no admitted line, so the
// cached world stayed keyed and stayed fresh — and emitted different .x.go
// bytes depending only on cache warmth. Hashing the whole file makes any
// resolution change re-key, which is the only rule that is right by
// construction rather than by enumeration.
func TestSharedWorldOriginFollowsWholeModuleGraph(t *testing.T) {
	const base = "module example.com/app\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => /elsewhere/gsx\n"
	const composed = base + "\nrequire github.com/jackielii/structpages v1.0.0\n"
	// The transitive dependency of the composed package: no line here names a
	// composed path, which is exactly what the old relevance filter ignored.
	const depBumped = composed + "\nrequire github.com/jackielii/ctxkey v1.0.1 // indirect\n"
	const depBumpedNewer = composed + "\nrequire github.com/jackielii/ctxkey v1.2.0 // indirect\n"

	originOf := func(t *testing.T, gomod, gosum string, vendored bool) string {
		t.Helper()
		root := t.TempDir()
		writeFile(t, root, "go.mod", gomod)
		if gosum != "" {
			writeFile(t, root, "go.sum", gosum)
		}
		return sharedWorldOrigin(root, nil, vendored)
	}

	if originOf(t, depBumped, "", false) == originOf(t, depBumpedNewer, "", false) {
		t.Fatal("bumping a composed package's transitive dependency did not change the world origin")
	}
	if originOf(t, base, "", false) == originOf(t, composed, "", false) {
		t.Fatal("adding a composed require did not change the world origin")
	}
	// go.sum content participates: a same-version re-resolution with different
	// bytes must not reuse a world.
	if originOf(t, composed, "h1:aaa\n", false) == originOf(t, composed, "h1:bbb\n", false) {
		t.Fatal("differing go.sum content did not change the world origin")
	}

	// Vendoring forfeits content hashing entirely: go.mod does not constrain
	// what is in vendor/, so two roots with identical files must not share.
	vendoredA := originOf(t, composed, "", true)
	vendoredB := originOf(t, composed, "", true)
	if vendoredA == vendoredB {
		t.Fatal("two vendored roots with identical go.mod share one world origin: vendor/ contents are not keyed by anything else")
	}
	if vendoredA == originOf(t, composed, "", false) {
		t.Fatal("a vendored root and an unvendored root with identical go.mod share one world origin")
	}
}

// TestSharedWorldRootBound is a pure table test over composed path sets: it
// decides whether a world may be shared between two module roots. A world
// holding only the fixed base closure is root-independent (sharedWorldOrigin
// already keys where the runtime resolves from); a world holding a package
// under the main module's own import path is not, because two roots can
// declare the same module path and hold different code there — two checkouts
// of one project — and nothing else in the key would tell them apart. Since
// main-module code stopped composing, the reachable case is a nested module
// named as a load root, which the extension tier composes.
func TestSharedWorldRootBound(t *testing.T) {
	base := []string{gsxRuntimeImportPath, stdImportPath}
	tests := []struct {
		name       string
		paths      []string
		modulePath string
		want       bool
	}{
		{"base closure alone is shareable across roots", base, "example.com/app", false},
		{
			"an out-of-module config package keeps the world shareable",
			append(append([]string(nil), base...), "github.com/jackielii/structpages"),
			"example.com/app",
			false,
		},
		{
			"a package under the main module's path (a nested module load root) binds the world to this root",
			append(append([]string(nil), base...), "example.com/app/merge"),
			"example.com/app",
			true,
		},
		{
			"the main module package itself binds the world to this root",
			append(append([]string(nil), base...), "example.com/app"),
			"example.com/app",
			true,
		},
		{
			"a prefix neighbour is not a main-module package",
			append(append([]string(nil), base...), "example.com/apps/merge"),
			"example.com/app",
			false,
		},
		{
			"without a module path every config package binds to the root",
			append(append([]string(nil), base...), "github.com/jackielii/structpages"),
			"",
			true,
		},
		{"without a module path the base closure still shares", base, "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sharedWorldRootBound(tt.paths, tt.modulePath); got != tt.want {
				t.Fatalf("sharedWorldRootBound(%v, %q) = %v, want %v", tt.paths, tt.modulePath, got, tt.want)
			}
		})
	}
}

// TestSharedWorldComposition is a pure table test over Options shapes — it
// never calls packages.Load. sharedWorldComposition derives the shared-world
// load-path set the same way the full-mode loadPaths derivation in
// externalImporter does (module.go:704-727): it reuses the already-resolved
// package-path fields (ClassMergerRef.PkgPath, FilterAlias.PkgPath,
// RendererAlias.PkgPath) rather than re-parsing "pkg.Func" strings.
func TestSharedWorldComposition(t *testing.T) {
	base := []string{gsxRuntimeImportPath, stdImportPath}

	tests := []struct {
		name      string
		opts      Options
		wantOK    bool
		wantPaths []string
	}{
		{
			name:      "unconfigured module composes to exactly {runtime, std}",
			opts:      Options{},
			wantOK:    true,
			wantPaths: base,
		},
		{
			name: "class merger package joins the world",
			opts: Options{
				ClassMerger: &ClassMergerRef{PkgPath: "example.com/m/merge", FuncName: "Merge"},
			},
			wantOK:    true,
			wantPaths: append(append([]string(nil), base...), "example.com/m/merge"),
		},
		{
			name: "filter alias package joins the world",
			opts: Options{
				Aliases: []FilterAlias{
					{Name: "url", PkgPath: "github.com/jackielii/structpages", FuncName: "URLFor"},
				},
			},
			wantOK:    true,
			wantPaths: append(append([]string(nil), base...), "github.com/jackielii/structpages"),
		},
		{
			name: "renderer alias package joins the world",
			opts: Options{
				Renderers: []RendererAlias{
					{TypeKey: "example.com/m.Widget", PkgPath: "example.com/m/render", FuncName: "Render"},
				},
			},
			wantOK:    true,
			wantPaths: append(append([]string(nil), base...), "example.com/m/render"),
		},
		{
			name: "renderer alias uses finalRendererAliases: only the last registration per TypeKey survives",
			opts: Options{
				Renderers: []RendererAlias{
					{TypeKey: "example.com/m.Widget", PkgPath: "example.com/m/old", FuncName: "Render"},
					{TypeKey: "example.com/m.Widget", PkgPath: "example.com/m/new", FuncName: "Render"},
				},
			},
			wantOK:    true,
			wantPaths: append(append([]string(nil), base...), "example.com/m/new"),
		},
		{
			name: "LoadPkgs joins the world",
			opts: Options{
				LoadPkgs: []string{"example.com/extra"},
			},
			wantOK:    true,
			wantPaths: append(append([]string(nil), base...), "example.com/extra"),
		},
		{
			name: "non-std whole-package FilterPkgs joins the world",
			opts: Options{
				FilterPkgs: []string{"example.com/wholefilter"},
			},
			wantOK:    true,
			wantPaths: append(append([]string(nil), base...), "example.com/wholefilter"),
		},
		{
			name: "std-only FilterPkgs adds nothing beyond the base pair",
			opts: Options{
				FilterPkgs: []string{stdImportPath},
			},
			wantOK:    true,
			wantPaths: base,
		},
		{
			name: "a class merger in the MAIN module is left out of the world",
			opts: Options{
				ModulePath:  "example.com/m",
				ClassMerger: &ClassMergerRef{PkgPath: "example.com/m/merge", FuncName: "Merge"},
			},
			wantOK: true,
			// Its types come from retained source, and its external dependencies
			// reach the world through the extension tier. Composing it only
			// stamped its files into every tier, so a merger edit rebuilt them all.
			wantPaths: base,
		},
		{
			name: "an out-of-module config package still joins the world",
			opts: Options{
				ModulePath: "example.com/m",
				FilterPkgs: []string{"github.com/jackielii/structpages"},
			},
			wantOK:    true,
			wantPaths: append(append([]string(nil), base...), "github.com/jackielii/structpages"),
		},
		{
			name: "the exclusion is by import-path prefix, so a nested module under the main module's path is left out too",
			opts: Options{
				ModulePath: "example.com/m",
				LoadPkgs:   []string{"example.com/m/nested/tools"},
			},
			wantOK: true,
			// Over-exclusion is safe: the project half references it, so the
			// extension tier composes it with its non-main-module identity read
			// from the load rather than guessed from the path.
			wantPaths: base,
		},
		{
			name: "a module that IS the gsx runtime has no external world left to share",
			opts: Options{
				ModulePath: gsxRuntimeImportPath,
			},
			wantOK: false,
		},
		{
			name: "per-dir class merger disqualifies composition",
			opts: Options{
				PerDir: map[string]DirOptions{
					"dirA": {ClassMerger: &ClassMergerRef{PkgPath: "example.com/dirA/merge", FuncName: "Merge"}},
				},
			},
			wantOK: false,
		},
		{
			name: "per-dir non-std FilterPkgs disqualifies composition",
			opts: Options{
				PerDir: map[string]DirOptions{
					"dirA": {FilterPkgs: []string{"example.com/dirA/filter"}},
				},
			},
			wantOK: false,
		},
		{
			name: "per-dir std-only FilterPkgs does not disqualify composition",
			opts: Options{
				PerDir: map[string]DirOptions{
					"dirA": {FilterPkgs: []string{stdImportPath}},
				},
			},
			wantOK:    true,
			wantPaths: base,
		},
		{
			name: "everything configured at once composes to one sorted unique set",
			opts: Options{
				ClassMerger: &ClassMergerRef{PkgPath: "example.com/m/merge", FuncName: "Merge"},
				Aliases: []FilterAlias{
					{Name: "url", PkgPath: "github.com/jackielii/structpages", FuncName: "URLFor"},
				},
				Renderers: []RendererAlias{
					{TypeKey: "example.com/m.Widget", PkgPath: "example.com/m/render", FuncName: "Render"},
				},
				LoadPkgs:   []string{"example.com/extra"},
				FilterPkgs: []string{stdImportPath, "example.com/wholefilter"},
				PerDir: map[string]DirOptions{
					"dirA": {FilterPkgs: []string{stdImportPath}},
				},
			},
			wantOK: true,
			wantPaths: []string{
				gsxRuntimeImportPath,
				stdImportPath,
				"example.com/extra",
				"example.com/m/merge",
				"example.com/m/render",
				"example.com/wholefilter",
				"github.com/jackielii/structpages",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := &Module{opts: tt.opts}
			gotPaths, gotOK := m.sharedWorldComposition()
			if gotOK != tt.wantOK {
				t.Fatalf("ok = %v, want %v (paths %v)", gotOK, tt.wantOK, gotPaths)
			}
			if !tt.wantOK {
				return
			}
			wantSorted := append([]string(nil), tt.wantPaths...)
			slices.Sort(wantSorted)
			if !slices.Equal(gotPaths, wantSorted) {
				t.Fatalf("paths = %v, want %v", gotPaths, wantSorted)
			}
		})
	}
}
