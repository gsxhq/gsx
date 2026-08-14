package gen

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/fsnotify/fsnotify"
)

func TestSourceTrackerAuthoritativeReconcileFindsMissedChanges(t *testing.T) {
	root := t.TempDir()
	changed := filepath.Join(root, "ui", "card.gsx")
	removed := filepath.Join(root, "model", "model.go")
	writeTestFile(t, changed, "package ui\n")
	writeTestFile(t, removed, "package model\n")
	tracker, err := newSourceTracker([]string{root}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	writeTestFile(t, changed, "package ui\n// changed while events were lost\n")
	if err := os.Remove(removed); err != nil {
		t.Fatal(err)
	}
	dirty := newWatchDirtySet()
	changedAny, err := tracker.reconcile([]string{root}, dirty)
	if err != nil {
		t.Fatal(err)
	}
	if !changedAny || !dirty.dirs[filepath.Dir(changed)] {
		t.Fatalf("reconciled dirs = %v, changed = %v", dirty.dirs, changedAny)
	}
	// removed is a plain .go file (not go.mod/go.sum): its loss regenerates
	// the dependent closure in place, so it lands in the Go lane — goDirs plus
	// the goDirty rebuild latch, NOT depDirty (reserved for module
	// dependency-surface files that require a full session reopen) and NOT
	// dirs, whose .gsx refresh would only re-scan the directory a second time.
	if dirty.dirs[filepath.Dir(removed)] {
		t.Fatalf("missed authored Go removal queued %q on the .gsx lane; it belongs to goDirs alone", filepath.Dir(removed))
	}
	if !dirty.goDirs[filepath.Dir(removed)] {
		t.Fatalf("missed authored Go removal did not queue %q into goDirs, got %v", filepath.Dir(removed), dirty.goDirs)
	}
	if dirty.depDirty {
		t.Fatal("missed authored Go removal incorrectly set depDirty")
	}
	if !dirty.goDirty {
		t.Fatal("missed authored Go removal did not set goDirty")
	}

	// The tracker commits the authoritative scan, so repeating it is a no-op.
	dirty = newWatchDirtySet()
	changedAny, err = tracker.reconcile([]string{root}, dirty)
	if err != nil || changedAny || !dirty.empty() {
		t.Fatalf("second reconcile = (%v, %v, %v, %v, %v), want no-op", changedAny, dirty.dirs, dirty.goDirs, dirty.depDirty, err)
	}
}

func TestExplicitExcludedRootRecreationIsRearmedAndInventoried(t *testing.T) {
	module := t.TempDir()
	explicit := filepath.Join(module, "tmp")
	if err := os.MkdirAll(explicit, 0o755); err != nil {
		t.Fatal(err)
	}
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		t.Fatal(err)
	}
	defer watcher.Close()
	if err := addWatchTree(watcher, []string{module, explicit}); err != nil {
		t.Fatal(err)
	}
	tracker, err := newSourceTracker([]string{module, explicit}, []string{explicit}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(explicit); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(explicit, 0o755); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(explicit, "page.gsx")
	writeTestFile(t, source, "package tmp\n")

	dirty := newWatchDirtySet()
	changed, err := applyWatchEvent(watcher, fsnotify.Event{Name: explicit, Op: fsnotify.Create}, tracker, dirty)
	if err != nil {
		t.Fatal(err)
	}
	if !changed || !dirty.dirs[explicit] {
		t.Fatalf("explicit recreated root = changed %v, dirs %v", changed, dirty.dirs)
	}
}

func TestExplicitRootBelowExcludedAncestorRearmsAcrossAncestorRecreation(t *testing.T) {
	module := t.TempDir()
	excluded := filepath.Join(module, "tmp")
	explicit := filepath.Join(excluded, "selected")
	source := filepath.Join(explicit, "page.gsx")
	writeTestFile(t, source, "package selected\n")

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		t.Fatal(err)
	}
	defer watcher.Close()
	if err := addWatchTree(watcher, []string{module, explicit}); err != nil {
		t.Fatal(err)
	}
	tracker, err := newSourceTracker([]string{module, explicit}, []string{explicit}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := addRequestedRootSentinels(watcher, tracker); err != nil {
		t.Fatal(err)
	}

	if err := os.RemoveAll(excluded); err != nil {
		t.Fatal(err)
	}
	dirty := newWatchDirtySet()
	changed, err := applyWatchEvent(watcher, fsnotify.Event{Name: excluded, Op: fsnotify.Remove}, tracker, dirty)
	if err != nil {
		t.Fatal(err)
	}
	if !changed || !dirty.dirs[explicit] {
		t.Fatalf("excluded ancestor removal = changed %v, dirs %v", changed, dirty.dirs)
	}

	writeTestFile(t, source, "package selected\n// recreated\n")
	changed, err = applyWatchEvent(watcher, fsnotify.Event{Name: excluded, Op: fsnotify.Create}, tracker, dirty)
	if err != nil {
		t.Fatal(err)
	}
	if !changed || !dirty.dirs[explicit] {
		t.Fatalf("excluded ancestor recreation = changed %v, dirs %v", changed, dirty.dirs)
	}
}

func writeTestFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}
