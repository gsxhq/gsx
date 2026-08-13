package codegen

import (
	"slices"
	"testing"
)

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
