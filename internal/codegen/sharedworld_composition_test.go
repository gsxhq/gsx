package codegen

import (
	"slices"
	"testing"
)

// TestSharedWorldRootBound is a pure table test over composed path sets: it
// decides whether a world may be shared between two module roots. A world
// holding only the fixed base closure is root-independent (sharedWorldOrigin
// already keys where the runtime resolves from); a world holding a config
// package that could live in the main module is not, because two roots can
// declare the same module path and the same config — two checkouts of one
// project — and nothing else in the key would tell them apart.
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
			"a main-module config package binds the world to this root",
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
