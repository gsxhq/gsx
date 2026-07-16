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
	tracker, err := newSourceTracker([]string{root}, nil)
	if err != nil {
		t.Fatal(err)
	}

	writeTestFile(t, changed, "package ui\n// changed while events were lost\n")
	if err := os.Remove(removed); err != nil {
		t.Fatal(err)
	}
	pending := map[string]bool{}
	depDirty := false
	changedAny, err := tracker.reconcile([]string{root}, pending, &depDirty)
	if err != nil {
		t.Fatal(err)
	}
	if !changedAny || !pending[filepath.Dir(changed)] || !pending[filepath.Dir(removed)] {
		t.Fatalf("reconciled pending = %v, changed = %v", pending, changedAny)
	}
	if !depDirty {
		t.Fatal("missed authored Go removal did not invalidate dependency state")
	}

	// The tracker commits the authoritative scan, so repeating it is a no-op.
	pending = map[string]bool{}
	depDirty = false
	changedAny, err = tracker.reconcile([]string{root}, pending, &depDirty)
	if err != nil || changedAny || len(pending) != 0 || depDirty {
		t.Fatalf("second reconcile = (%v, %v, %v, %v), want no-op", changedAny, pending, depDirty, err)
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
	tracker, err := newSourceTracker([]string{module, explicit}, []string{explicit})
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

	pending := map[string]bool{}
	depDirty := false
	changed, err := applyWatchEvent(watcher, fsnotify.Event{Name: explicit, Op: fsnotify.Create}, tracker, pending, &depDirty)
	if err != nil {
		t.Fatal(err)
	}
	if !changed || !pending[explicit] {
		t.Fatalf("explicit recreated root = changed %v, pending %v", changed, pending)
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
	tracker, err := newSourceTracker([]string{module, explicit}, []string{explicit})
	if err != nil {
		t.Fatal(err)
	}
	if err := addRequestedRootSentinels(watcher, tracker); err != nil {
		t.Fatal(err)
	}

	if err := os.RemoveAll(excluded); err != nil {
		t.Fatal(err)
	}
	pending := map[string]bool{}
	depDirty := false
	changed, err := applyWatchEvent(watcher, fsnotify.Event{Name: excluded, Op: fsnotify.Remove}, tracker, pending, &depDirty)
	if err != nil {
		t.Fatal(err)
	}
	if !changed || !pending[explicit] {
		t.Fatalf("excluded ancestor removal = changed %v, pending %v", changed, pending)
	}

	writeTestFile(t, source, "package selected\n// recreated\n")
	changed, err = applyWatchEvent(watcher, fsnotify.Event{Name: excluded, Op: fsnotify.Create}, tracker, pending, &depDirty)
	if err != nil {
		t.Fatal(err)
	}
	if !changed || !pending[explicit] {
		t.Fatalf("excluded ancestor recreation = changed %v, pending %v", changed, pending)
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
