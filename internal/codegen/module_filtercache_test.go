package codegen

import (
	"fmt"
	"path/filepath"
	"testing"
)

// TestGeneratePathDoesNoFilterTableLoad proves the Generate/Package path never
// runs a standalone `go list` for the filter table. It used to run one per
// Module, 1:1 with the external importer's own load — 148 filter loads against
// 127 importer loads across the gen suite alone — even though the importer had
// already loaded and type-checked those exact packages. The table is now
// harvested from those types.
//
// The old version of this test asserted `filterTableLoads() == 1` and guarded a
// weaker property (the table is not reloaded per warm regen). Zero is the
// stronger claim and subsumes it.
func TestGeneratePathDoesNoFilterTableLoad(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	m, root := setupChainModule(t)
	comp := filepath.Join(root, "components")

	for i := range 4 {
		if _, _, err := m.Generate(comp); err != nil {
			t.Fatalf("generate #%d: %v", i, err)
		}
	}
	if got := m.filterTableLoads(); got != 0 {
		t.Fatalf("filter table loaded %d times; want 0 (harvest it from the importer's types)", got)
	}
	if got := m.externalLoads(); got != 1 {
		t.Fatalf("externalLoads = %d; want 1", got)
	}

	// An edit-driven regen (content change → applyDirty → re-analyze) must not
	// introduce one either — a .gsx edit cannot change the filter packages.
	card := filepath.Join(comp, "card.gsx")
	m.SetOverride(card, []byte("package components\n\nimport \"example.com/x/util\"\n\ncomponent X(title string) {\n\t<div><util.Y label={ title }/></div>\n}\n"))
	if _, _, err := m.Generate(comp); err != nil {
		t.Fatal(err)
	}
	if got := m.filterTableLoads(); got != 0 {
		t.Fatalf("filter table loaded after a .gsx edit: loads=%d; want 0", got)
	}

	// A FileSet rebuild drops ext + the harvested tables together. The next
	// analyze re-loads the importer once and re-harvests from it — still no
	// standalone filter load.
	m.rebuildFset()
	m.SetOverride(card, fmt.Appendf(nil, "package components\n\nimport \"example.com/x/util\"\n\ncomponent X(title string) {\n\t<p><util.Y label={ title }/></p>\n}\n"))
	if _, _, err := m.Generate(comp); err != nil {
		t.Fatal(err)
	}
	if el, fl := m.externalLoads(), m.filterTableLoads(); el != 2 || fl != 0 {
		t.Fatalf("after rebuildFset: externalLoads=%d filterTableLoads=%d; want 2,0", el, fl)
	}
}

// TestFmtPathKeepsStandaloneFilterLoad pins the other half of the split.
// buildPackageSkeletons is `gsx fmt`'s syntactic fast lane: it deliberately
// never loads the external importer (that is what took `gsx fmt -l` from ~16s to
// 0.58s). Harvesting its table from types would force the full "./..." load it
// exists to avoid, so it keeps the standalone loadFilterTableMulti — which loads
// ONLY the filter packages — and caches it for the Module's lifetime. Renderer
// declaration resolution is intentionally excluded from this path.
func TestFmtPathKeepsStandaloneFilterLoad(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	m, root := setupChainModule(t)
	comp := filepath.Join(root, "components")

	for i := range 3 {
		if _, err := m.buildPackageSkeletons(comp); err != nil {
			t.Fatalf("buildPackageSkeletons #%d: %v", i, err)
		}
	}
	if got := m.externalLoads(); got != 0 {
		t.Fatalf("externalLoads = %d; want 0 (the fmt fast path must not go-list the world)", got)
	}
	if got := m.filterTableLoads(); got != 1 {
		t.Fatalf("filterTableLoads = %d; want 1 (loaded once, then cached)", got)
	}
}

func TestFmtPathDoesNotResolveLocalRenderer(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	root, _, viewsDir := localRendererModule(t)
	m, err := Open(Options{
		ModuleRoot: root,
		ModulePath: "example.com/app",
		FilterPkgs: localRendererOptions().FilterPkgs,
		Renderers:  localRendererOptions().Renderers,
	})
	if err != nil {
		t.Fatal(err)
	}
	for i := range 2 {
		if _, err := m.buildPackageSkeletons(viewsDir); err != nil {
			t.Fatalf("buildPackageSkeletons #%d: %v", i, err)
		}
	}
	if got := m.externalLoads(); got != 0 {
		t.Fatalf("externalLoads = %d; want 0 (fmt must not resolve local renderers)", got)
	}
	if got := m.filterTableLoads(); got != 1 {
		t.Fatalf("filterTableLoads = %d; want one cached filter-only load", got)
	}
	m.mu.Lock()
	resolved := m.rendererPkgsDone || m.rendererTblDone
	m.mu.Unlock()
	if resolved {
		t.Fatal("fmt path resolved the module renderer registry")
	}
}
