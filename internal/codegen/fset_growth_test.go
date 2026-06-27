package codegen

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestOpenReadsFsetRebuildThreshold(t *testing.T) {
	// Default when env unset.
	t.Setenv("GSX_FSET_REBUILD_BYTES", "")
	os.Unsetenv("GSX_FSET_REBUILD_BYTES")
	m, err := Open(Options{ModuleRoot: t.TempDir(), ModulePath: "example.com/x"})
	if err != nil {
		t.Fatal(err)
	}
	if m.fsetRebuildBytes != defaultFsetRebuildBytes {
		t.Errorf("default threshold = %d, want %d", m.fsetRebuildBytes, defaultFsetRebuildBytes)
	}
	// Env override (including 0 = disabled).
	for _, tc := range []struct {
		env  string
		want int
	}{{"4096", 4096}, {"0", 0}, {"bogus", defaultFsetRebuildBytes}, {"-5", defaultFsetRebuildBytes}} {
		t.Setenv("GSX_FSET_REBUILD_BYTES", tc.env)
		m, err := Open(Options{ModuleRoot: t.TempDir(), ModulePath: "example.com/x"})
		if err != nil {
			t.Fatal(err)
		}
		if m.fsetRebuildBytes != tc.want {
			t.Errorf("env %q: threshold = %d, want %d", tc.env, m.fsetRebuildBytes, tc.want)
		}
	}
}

func TestFsetGrowthIsBounded(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	m, root := setupChainModule(t)
	util := filepath.Join(root, "util")
	utilFile := filepath.Join(util, "util.gsx")
	// Force frequent rebuilds: tiny threshold.
	m.fsetRebuildBytes = 4096
	var maxBase int
	for i := 0; i < 12; i++ {
		// Each edit changes content (distinct label text) so the dir is marked dirty
		// and re-parsed, growing the fset.
		src := fmt.Appendf(nil, "package util\n\ncomponent Y(label string) {\n\t<span>%d:{label}</span>\n}\n", i)
		m.SetOverride(utilFile, src)
		if _, err := m.Package(util); err != nil {
			t.Fatalf("edit %d: %v", i, err)
		}
		if b := m.fset.Base(); b > maxBase {
			maxBase = b
		}
	}
	if m.rebuilds() == 0 {
		t.Fatalf("expected ≥1 rebuild under a 4 KiB threshold over 12 edits; got 0 (maxBase=%d)", maxBase)
	}
	// Bounded: the final fset.Base() reflects at most a post-rebuild baseline + a bit,
	// far below the unbounded 12×-growth it would reach without rebuilds.
	t.Logf("rebuilds=%d finalBase=%d maxBase=%d", m.rebuilds(), m.fset.Base(), maxBase)
}

func TestFsetRebuildDisabledAtZero(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	m, root := setupChainModule(t)
	m.fsetRebuildBytes = 0 // disabled
	util := filepath.Join(root, "util")
	utilFile := filepath.Join(util, "util.gsx")
	for i := 0; i < 5; i++ {
		m.SetOverride(utilFile, fmt.Appendf(nil, "package util\n\ncomponent Y(label string) {\n\t<b>%d:{label}</b>\n}\n", i))
		if _, err := m.Package(util); err != nil {
			t.Fatal(err)
		}
	}
	if m.rebuilds() != 0 {
		t.Errorf("threshold 0 must disable rebuilding; got %d rebuilds", m.rebuilds())
	}
}
