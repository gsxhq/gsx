package gen

import (
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// TestRemoveOrphanXgo_DeletesOwnedOrphan proves the core rule: a gsx-owned
// .x.go with no corresponding .gsx is deleted; a gsx-owned .x.go WITH its
// .gsx present is left alone.
func TestRemoveOrphanXgo_DeletesOwnedOrphan(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeFile(t, dir, "kept.gsx", "package p\n")
	writeFile(t, dir, "kept.x.go", gsxGeneratedHeader+"\n\npackage p\n")
	writeFile(t, dir, "orphan.x.go", gsxGeneratedHeader+"\n\npackage p\n")

	removed, err := removeOrphanXgo(dir)
	if err != nil {
		t.Fatalf("removeOrphanXgo: %v", err)
	}
	want := []string{filepath.Join(dir, "orphan.x.go")}
	assertPathSet(t, removed, want)

	if _, err := os.Stat(filepath.Join(dir, "orphan.x.go")); !os.IsNotExist(err) {
		t.Fatalf("orphan.x.go still exists (err=%v)", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "kept.x.go")); err != nil {
		t.Fatalf("kept.x.go (its .gsx is present) was removed: %v", err)
	}
}

// TestRemoveOrphanXgo_NotGsxOwnedNeverDeleted: a hand-written file named
// foo.x.go WITHOUT the gsx header on line 1 is never deleted, even when no
// foo.gsx exists — the ownership test is the header, not the name+absence.
func TestRemoveOrphanXgo_NotGsxOwnedNeverDeleted(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeFile(t, dir, "hand.x.go", "package p\n\n// hand-written, not gsx output\n")

	removed, err := removeOrphanXgo(dir)
	if err != nil {
		t.Fatalf("removeOrphanXgo: %v", err)
	}
	if len(removed) != 0 {
		t.Fatalf("deleted a hand-written file without the gsx header: %v", removed)
	}
	if _, err := os.Stat(filepath.Join(dir, "hand.x.go")); err != nil {
		t.Fatalf("hand.x.go was deleted: %v", err)
	}
}

// TestRemoveOrphanXgo_GsxPresentNeverDeleted: a gsx-owned .x.go whose .gsx
// sibling is still present — even a broken one currently poisoned — must
// never be deleted. Overwriting it is poison's job, not delete's.
func TestRemoveOrphanXgo_GsxPresentNeverDeleted(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeFile(t, dir, "broken.gsx", "package p\n\ncomponent X() {\n\t<p>{undefined}</p>\n}\n")
	writeFile(t, dir, "broken.x.go", gsxGeneratedHeader+"\n\npackage p\n\n// GSX GENERATION FAILED\nvar _ = GSX_GENERATION_FAILED\n")

	removed, err := removeOrphanXgo(dir)
	if err != nil {
		t.Fatalf("removeOrphanXgo: %v", err)
	}
	if len(removed) != 0 {
		t.Fatalf("deleted a .x.go whose .gsx is still present: %v", removed)
	}
}

// TestRemoveOrphanXgo_MissingDirIsNotAnError: sweeping a dir that no longer
// exists (e.g. the whole package dir was removed) is a no-op, not an error.
func TestRemoveOrphanXgo_MissingDirIsNotAnError(t *testing.T) {
	t.Parallel()
	removed, err := removeOrphanXgo(filepath.Join(t.TempDir(), "gone"))
	if err != nil {
		t.Fatalf("removeOrphanXgo on a missing dir returned an error: %v", err)
	}
	if len(removed) != 0 {
		t.Fatalf("removed = %v, want none", removed)
	}
}

// TestSweepOrphanDirs_SingleGsxDirDroppedFromDiscovery proves the walk-level
// sweep catches a directory whose ONLY .gsx was deleted: discoverDirs no
// longer returns it (it directly contains no .gsx), so sweepOrphanDirs must
// walk down to it independently and remove its orphaned .x.go.
func TestSweepOrphanDirs_SingleGsxDirDroppedFromDiscovery(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	views := filepath.Join(root, "views")
	writeFile(t, views, "orphan.x.go", gsxGeneratedHeader+"\n\npackage views\n")

	dirs, err := discoverDirs([]string{root})
	if err != nil {
		t.Fatalf("discoverDirs: %v", err)
	}
	if len(dirs) != 0 {
		t.Fatalf("expected no discovered dirs (no .gsx anywhere), got %v", dirs)
	}

	removed, err := sweepOrphanDirs([]string{root}, dirs)
	if err != nil {
		t.Fatalf("sweepOrphanDirs: %v", err)
	}
	want := []string{filepath.Join(views, "orphan.x.go")}
	assertPathSet(t, removed, want)
}

// TestSweepOrphanDirs_SkipsKeptDirs proves the walk-level sweep never touches
// a directory discovery DID return (kept) — that dir's orphans, if any, are
// the dir-scoped sweep's job (writeDirOutcome/regenDir), not this one's. This
// pins the no-double-report invariant: the two sweeps must partition dirs
// disjointly.
func TestSweepOrphanDirs_SkipsKeptDirs(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	views := filepath.Join(root, "views")
	writeFile(t, views, "kept.gsx", "package views\n")
	writeFile(t, views, "stray.x.go", gsxGeneratedHeader+"\n\npackage views\n")

	dirs, err := discoverDirs([]string{root})
	if err != nil {
		t.Fatalf("discoverDirs: %v", err)
	}
	if len(dirs) != 1 {
		t.Fatalf("expected exactly one discovered dir, got %v", dirs)
	}

	removed, err := sweepOrphanDirs([]string{root}, dirs)
	if err != nil {
		t.Fatalf("sweepOrphanDirs: %v", err)
	}
	if len(removed) != 0 {
		t.Fatalf("walk-level sweep touched a kept dir (double-report risk): %v", removed)
	}
	// stray.x.go must still be on disk — only the dir-scoped sweep may remove it.
	if _, err := os.Stat(filepath.Join(views, "stray.x.go")); err != nil {
		t.Fatalf("stray.x.go was removed by the walk-level sweep: %v", err)
	}
}

// assertPathSet fails the test unless got and want contain the same set of
// paths (order-independent).
func assertPathSet(t *testing.T, got, want []string) {
	t.Helper()
	g := append([]string(nil), got...)
	w := append([]string(nil), want...)
	sort.Strings(g)
	sort.Strings(w)
	if len(g) != len(w) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range g {
		if g[i] != w[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}
