package codegen

import (
	"slices"
	"testing"
)

// TestSharedWorldOriginCoversConfigModules pins what the world key knows about
// WHERE a config package resolves from. sharedWorldOrigin used to record only
// the gsx runtime's resolution, which was complete while the world held nothing
// else. With config packages composed in, two roots that pin the same external
// config import path at DIFFERENT versions — or replace it to different
// directories — hash to the same key, and sharedWorld.fresh agrees with the
// aliasing because the stamps it checks belong to the other root's files. The
// second project is then served the first's config types.
//
// Unconfigured modules must keep a byte-identical origin: they are the common
// case and their sharing is the whole point of the shared world.
func TestSharedWorldOriginCoversConfigModules(t *testing.T) {
	base := []string{gsxRuntimeImportPath, stdImportPath}
	configPath := "github.com/jackielii/structpages"
	configured := append(append([]string(nil), base...), configPath)

	originOf := func(t *testing.T, gomod string, composed []string) string {
		t.Helper()
		root := t.TempDir()
		writeFile(t, root, "go.mod", gomod)
		return sharedWorldOrigin(root, nil, composed)
	}

	const withoutConfig = "module example.com/app\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => /elsewhere/gsx\n"
	const configV1 = withoutConfig + "\nrequire " + "github.com/jackielii/structpages" + " v1.0.0\n"
	const configV15 = withoutConfig + "\nrequire " + "github.com/jackielii/structpages" + " v1.5.0\n"
	const configReplaced = configV1 + "\nreplace " + "github.com/jackielii/structpages" + " => /elsewhere/structpages\n"

	// Unconfigured: the config module's presence in go.mod must not perturb the
	// origin at all — that is the Task-1 behavior this widening must not change.
	if got, want := originOf(t, configV1, base), originOf(t, withoutConfig, base); got != want {
		t.Fatalf("unconfigured origin changed with an unrelated require:\n got %q\nwant %q", got, want)
	}
	if got, want := originOf(t, configV15, base), originOf(t, configV1, base); got != want {
		t.Fatalf("unconfigured origin is version-sensitive to an uncomposed module:\n got %q\nwant %q", got, want)
	}

	// Configured: the same two roots must now key differently.
	if originOf(t, configV1, configured) == originOf(t, configV15, configured) {
		t.Fatal("two roots pinning different versions of a COMPOSED config module share one world origin")
	}
	if originOf(t, configV1, configured) == originOf(t, configReplaced, configured) {
		t.Fatal("a replace of a COMPOSED config module does not change the world origin")
	}
	// And the composed origin must actually be richer than the base one, or the
	// two checks above could be passing for some unrelated reason.
	if originOf(t, configV1, configured) == originOf(t, configV1, base) {
		t.Fatal("composing a config module did not add anything to the origin")
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
