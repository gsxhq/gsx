package gen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestOrphan_E2E_StickyPoisonRemoved_MultiFile is the exact final-review probe
// that motivated this decision: broken.gsx poisons broken.x.go; deleting
// broken.gsx must not leave the poison stuck on disk forever. Multi-file
// shape — a valid sibling .gsx keeps the dir in discovery, so the dir-scoped
// sweep (writeDirOutcome) must remove the orphan and the sibling must
// regenerate cleanly (never re-poisoned).
func TestOrphan_E2E_StickyPoisonRemoved_MultiFile(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping module-resolution test in -short mode")
	}
	mod := newModule(t, "example.com/orphan1")
	views := filepath.Join(mod, "views")
	writeFile(t, views, "good.gsx", "package views\n\ncomponent Good() {\n\t<p>ok</p>\n}\n")
	writeFile(t, views, "broken.gsx", brokenComponent)

	if _, err := Generate([]string{mod}); err == nil {
		t.Fatal("expected Generate to report failure (broken.gsx)")
	}
	if b, _ := os.ReadFile(filepath.Join(views, "broken.x.go")); !strings.Contains(string(b), "GSX GENERATION FAILED") {
		t.Fatal("broken.x.go not poisoned — precondition for this test failed")
	}

	// Delete the broken .gsx. The sticky-poison trap: without orphan removal,
	// broken.x.go stays on disk forever and generate re-poisons it (blaming a
	// file that no longer exists) on every future run.
	if err := os.Remove(filepath.Join(views, "broken.gsx")); err != nil {
		t.Fatal(err)
	}

	res, err := Generate([]string{mod})
	if err != nil {
		t.Fatalf("generate after deleting the broken .gsx must succeed (sticky poison): %v", err)
	}
	if _, err := os.Stat(filepath.Join(views, "broken.x.go")); !os.IsNotExist(err) {
		t.Fatalf("orphan poison broken.x.go was not removed (err=%v)", err)
	}
	found := false
	for _, r := range res.Removed {
		if r == filepath.Join(views, "broken.x.go") {
			found = true
		}
	}
	if !found {
		t.Errorf("Result.Removed does not list broken.x.go: %v", res.Removed)
	}
	goBuild(t, mod)
}

// TestOrphan_E2E_StickyPoisonRemoved_SingleFile is the single-.gsx-dir shape
// of the sticky-poison trap: the broken .gsx is the ONLY source in its dir, so
// deleting it drops the dir out of discovery entirely (discoverDirs only
// returns dirs that directly contain a .gsx). Without the walk-level sweep,
// generate would exit 0 while the orphan poison stays on disk and `go build`
// stays permanently broken.
func TestOrphan_E2E_StickyPoisonRemoved_SingleFile(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping module-resolution test in -short mode")
	}
	mod := newModule(t, "example.com/orphan2")
	views := filepath.Join(mod, "views")
	writeFile(t, views, "broken.gsx", brokenComponent)

	if _, err := Generate([]string{mod}); err == nil {
		t.Fatal("expected Generate to report failure (broken.gsx)")
	}
	if b, _ := os.ReadFile(filepath.Join(views, "broken.x.go")); !strings.Contains(string(b), "GSX GENERATION FAILED") {
		t.Fatal("broken.x.go not poisoned — precondition for this test failed")
	}

	if err := os.Remove(filepath.Join(views, "broken.gsx")); err != nil {
		t.Fatal(err)
	}

	res, err := Generate([]string{mod})
	if err != nil {
		t.Fatalf("generate after deleting the only .gsx in the dir must succeed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(views, "broken.x.go")); !os.IsNotExist(err) {
		t.Fatalf("orphan poison broken.x.go was not removed by the walk-level sweep (err=%v)", err)
	}
	found := false
	for _, r := range res.Removed {
		if r == filepath.Join(views, "broken.x.go") {
			found = true
		}
	}
	if !found {
		t.Errorf("Result.Removed does not list broken.x.go: %v", res.Removed)
	}
	goBuild(t, mod)
}

// TestOrphan_E2E_NonPoisonOrphanRemoved: user decision was ALL gsx-owned
// orphans, not just poison ones. A cleanly generated .x.go whose .gsx is
// later deleted must also be removed on the next generate.
func TestOrphan_E2E_NonPoisonOrphanRemoved(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping module-resolution test in -short mode")
	}
	mod := newModule(t, "example.com/orphan3")
	views := filepath.Join(mod, "views")
	writeFile(t, views, "hi.gsx", hiComponent)

	if _, err := Generate([]string{mod}); err != nil {
		t.Fatalf("clean generate failed: %v", err)
	}
	xgo := filepath.Join(views, "hi.x.go")
	if _, err := os.Stat(xgo); err != nil {
		t.Fatalf("hi.x.go not written: %v", err)
	}

	if err := os.Remove(filepath.Join(views, "hi.gsx")); err != nil {
		t.Fatal(err)
	}
	res, err := Generate([]string{mod})
	if err != nil {
		t.Fatalf("generate after deleting a clean .gsx must succeed: %v", err)
	}
	if _, err := os.Stat(xgo); !os.IsNotExist(err) {
		t.Fatalf("orphaned (non-poison) hi.x.go was not removed (err=%v)", err)
	}
	if len(res.Removed) != 1 || res.Removed[0] != xgo {
		t.Errorf("Result.Removed = %v, want [%s]", res.Removed, xgo)
	}
}
