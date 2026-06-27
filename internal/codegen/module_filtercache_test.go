package codegen

import (
	"fmt"
	"path/filepath"
	"testing"
)

// TestFilterTableCachedAcrossRegens proves the filter table — a ~150ms
// packages.Load — is harvested ONCE per Module and reused across every warm
// regen, rather than reloaded on each analyze(). Before caching, every
// gsx generate --watch cycle paid the full filter-package load, turning ~10ms
// warm regens into ~150ms ones.
func TestFilterTableCachedAcrossRegens(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	m, root := setupChainModule(t)
	comp := filepath.Join(root, "components")

	// Cold + several warm regens of the same package.
	for i := range 4 {
		if _, _, err := m.Generate(comp); err != nil {
			t.Fatalf("generate #%d: %v", i, err)
		}
	}
	if got := m.filterTableLoads(); got != 1 {
		t.Fatalf("filter table loaded %d times across 4 regens; want 1 (cache miss every cycle = the watch slowdown)", got)
	}

	// An edit-driven regen (content change → applyDirty → re-analyze) must also
	// reuse the cached table — a .gsx edit cannot change the filter packages.
	card := filepath.Join(comp, "card.gsx")
	m.SetOverride(card, []byte("package components\n\nimport \"example.com/x/util\"\n\ncomponent X(title string) {\n\t<div><util.Y label={ title }/></div>\n}\n"))
	if _, _, err := m.Generate(comp); err != nil {
		t.Fatal(err)
	}
	if got := m.filterTableLoads(); got != 1 {
		t.Fatalf("filter table reloaded after a .gsx edit: loads=%d; want 1", got)
	}

	// A FileSet rebuild drops derived caches, so the next analyze reloads it once.
	m.rebuildFset()
	m.SetOverride(card, fmt.Appendf(nil, "package components\n\nimport \"example.com/x/util\"\n\ncomponent X(title string) {\n\t<p><util.Y label={ title }/></p>\n}\n"))
	if _, _, err := m.Generate(comp); err != nil {
		t.Fatal(err)
	}
	if got := m.filterTableLoads(); got != 2 {
		t.Fatalf("filter table loads after rebuildFset = %d; want 2 (one reload post-rebuild)", got)
	}
}
