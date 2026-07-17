package gen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestArmedWatchSessionQueuesEditBeforeInitialGeneration(t *testing.T) {
	root := t.TempDir()
	writeMod(t, root)
	dir := filepath.Join(root, "views")
	gsxPath := filepath.Join(dir, "page.gsx")
	writeFileT(t, gsxPath, "package views\ncomponent Page() { <h1>old</h1> }\n")

	armed, err := armWatchSession(watchConfig{paths: []string{dir}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := armed.Close(); err != nil {
			t.Error(err)
		}
	}()

	// armWatchSession has registered real fsnotify watches and captured the
	// source-tracker baseline, but has not taken the authoritative generation
	// snapshot. This edit must be queued, not lost in startup.
	writeFileT(t, gsxPath, "package views\ncomponent Page() { <h1>new</h1> }\n")
	startup, err := armed.session.initialGenerate()
	if err != nil {
		t.Fatal(err)
	}
	if len(startup) != 1 || !startup[0].OK {
		t.Fatalf("initial generation = %+v", startup)
	}
	generated, err := os.ReadFile(filepath.Join(dir, "page.x.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(generated), "new</h1>") {
		t.Fatalf("initial generation did not use the post-arm source snapshot:\n%s", generated)
	}

	pending := map[string]bool{}
	depDirty := false
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	for len(pending) == 0 {
		select {
		case event := <-armed.watcher.Events:
			if _, eventErr := applyWatchEvent(armed.watcher, event, armed.sources, pending, &depDirty); eventErr != nil {
				t.Fatal(eventErr)
			}
		case watchErr := <-armed.watcher.Errors:
			t.Fatalf("watch error: %v", watchErr)
		case <-deadline.C:
			t.Fatal("post-arm edit was not queued for a follow-up generation")
		}
	}

	results, err := armed.session.regenPending(pending, depDirty)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || !results[0].OK || results[0].Dir != dir {
		t.Fatalf("queued follow-up generation = %+v, want one successful views cycle", results)
	}
}
