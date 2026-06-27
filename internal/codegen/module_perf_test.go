package codegen

import (
	"fmt"
	"path/filepath"
	"testing"
)

// TestWarmRegenDoesNoGoListReloads is a performance regression guard. The two
// expensive, non-incremental costs in analyze() are go-list / packages.Load
// calls: the external importer (externalImporter) and the filter table
// (cachedFilterTable). Each is ~150ms; they depend only on Module-immutable
// inputs, so they MUST be loaded once and reused across every warm regen. If a
// future change reintroduces a per-edit go-list, a `gsx generate --watch` cycle
// (or an LSP keystroke) regresses from ~10ms back to hundreds of ms.
//
// This guards the invariant by COUNT, not wall-clock (which is machine-flaky):
// across a cold generate plus many edit-driven regens, externalLoads() and
// filterTableLoads() must each stay at 1.
func TestWarmRegenDoesNoGoListReloads(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	m, root := setupChainModule(t)
	pages := filepath.Join(root, "pages")
	comp := filepath.Join(root, "components")
	card := filepath.Join(comp, "card.gsx")

	// Cold generate the whole chain: this is where the one allowed go-list pair
	// (ext + filter) happens.
	if _, _, err := m.Generate(pages); err != nil {
		t.Fatalf("cold generate: %v", err)
	}
	if el, fl := m.externalLoads(), m.filterTableLoads(); el != 1 || fl != 1 {
		t.Fatalf("after cold generate: externalLoads=%d filterTableLoads=%d; want 1,1", el, fl)
	}

	// 10 edit-driven warm regens of the components package. Each is a real
	// content change → applyDirty → re-analyze. None may reload ext or filters.
	for i := range 10 {
		m.SetOverride(card, fmt.Appendf(nil,
			"package components\n\nimport \"example.com/x/util\"\n\ncomponent X(title string) {\n\t<div>%d<util.Y label={ title }/></div>\n}\n", i))
		if _, _, err := m.Generate(comp); err != nil {
			t.Fatalf("warm regen #%d: %v", i, err)
		}
	}
	if el, fl := m.externalLoads(), m.filterTableLoads(); el != 1 || fl != 1 {
		t.Fatalf("after 10 warm regens: externalLoads=%d filterTableLoads=%d; want 1,1 "+
			"(a value > 1 means a per-edit go-list crept back in — the watch/LSP slowdown)", el, fl)
	}

	// Generating a DIFFERENT package in the same module must also reuse both
	// caches — the go-list inputs are module-wide, not per-package.
	if _, _, err := m.Generate(pages); err != nil {
		t.Fatalf("regen pages: %v", err)
	}
	if el, fl := m.externalLoads(), m.filterTableLoads(); el != 1 || fl != 1 {
		t.Fatalf("after cross-package regen: externalLoads=%d filterTableLoads=%d; want 1,1", el, fl)
	}
}
