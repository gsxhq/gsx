package gen

import (
	"testing"

	"github.com/gsxhq/gsx/internal/attrclass"
)

func TestManifestRoundTrip(t *testing.T) {
	dir := t.TempDir()
	const modPath = "github.com/example/app"
	want := manifest{
		SchemaVersion:  1,
		Module:         modPath,
		UserRules:      attrclass.Rules{JS: []attrclass.Rule{{Prefix: "wire:"}}},
		HasPredicate:   true,
		PredicateLabel: "fancy",
	}
	if err := saveManifest(dir, modPath, want); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, ok := loadManifest(dir, modPath)
	if !ok {
		t.Fatal("loadManifest: not found after save")
	}
	if got.Module != want.Module || got.HasPredicate != want.HasPredicate || got.PredicateLabel != want.PredicateLabel {
		t.Errorf("round-trip mismatch: got %+v want %+v", got, want)
	}
	if len(got.UserRules.JS) != 1 || got.UserRules.JS[0].Prefix != "wire:" {
		t.Errorf("rules lost in round-trip: %+v", got.UserRules)
	}
}

func TestManifestStableKey(t *testing.T) {
	dir := t.TempDir()
	const modPath = "github.com/example/app"
	// A second tool computes the same path from only the module path.
	if manifestPath(dir, modPath) != manifestPath(dir, modPath) {
		t.Fatal("manifestPath not stable for same module path")
	}
	if _, ok := loadManifest(dir, modPath); ok {
		t.Fatal("expected miss before any save")
	}
}
