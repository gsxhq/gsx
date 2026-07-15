package gen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestWatchDepChange verifies the depDirty path in the fire handler: when a
// companion .go file in a gsx package is modified (isDepFile returns true),
// the watch loop calls sess.reopen() rather than a per-dir regen. reopen()
// rebuilds all Modules and re-runs regenDir for every discovered dir so the
// type graph incorporates the new .go file content.
//
// This test simulates that path directly — it modifies a companion .go, asserts
// isDepFile classifies it correctly, calls sess.reopen(), then verifies the
// package regenerated with no error.
func TestWatchDepChange(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping module-resolution test in -short mode")
	}

	root := t.TempDir()
	writeMod(t, root)

	// Package with a companion .go file whose symbol is used by the .gsx.
	helperPath := filepath.Join(root, "blog", "helper.go")
	writeFileT(t, helperPath,
		"package blog\n\nfunc greeting() string { return \"hello\" }\n")
	writeFileT(t, filepath.Join(root, "blog", "page.gsx"),
		"package blog\n\ncomponent Page() {\n\t<h1>{greeting()}</h1>\n}\n")

	blogDir := filepath.Join(root, "blog")

	// Cold-start: openModule + regenDir writes blog/page.x.go.
	sess, startup, err := startWatchSessionForTest(watchConfig{paths: []string{blogDir}})
	if err != nil {
		t.Fatalf("startWatchSessionForTest: %v", err)
	}
	for _, r := range startup {
		if !r.OK {
			t.Fatalf("startup regen not OK: err=%v diags=%v", r.Err, r.Diags)
		}
	}

	// Verify the initial .x.go references greeting().
	initial, ioErr := os.ReadFile(filepath.Join(blogDir, "page.x.go"))
	if ioErr != nil {
		t.Fatalf("page.x.go missing after startup: %v", ioErr)
	}
	if !strings.Contains(string(initial), "greeting()") {
		t.Fatalf("page.x.go should reference greeting(), got:\n%s", initial)
	}

	// isDepFile gate: a plain .go file must be classified as a dep-change trigger.
	if !isDepFile(helperPath) {
		t.Fatalf("helper.go must be classified as dep file by isDepFile")
	}

	// Simulate the dep-change: add a new function to the companion .go.
	// greeting() is kept intact so the .gsx still compiles.
	writeFileT(t, helperPath,
		"package blog\n\nfunc greeting() string { return \"hello\" }\n\nfunc farewell() string { return \"goodbye\" }\n")

	// Drive the depDirty path: reopen() re-opens all Modules and re-runs
	// regenDir for every dir, incorporating the new .go file content.
	results, reopenErr := sess.reopen()
	if reopenErr != nil {
		t.Fatalf("sess.reopen() after dep change: %v", reopenErr)
	}

	// Regression guard: reopen must return non-empty results — a dep-change
	// cycle must NOT be silent. Before the fix, results were discarded and
	// callers never received diagnostics or notifications for dep-change cycles.
	if len(results) == 0 {
		t.Fatal("sess.reopen() returned no cycleResults: dep-change cycle is silent (regression)")
	}

	// The blog dir must appear in the results and must have regenerated OK.
	var found bool
	for _, r := range results {
		if r.Dir == blogDir {
			found = true
			if !r.OK {
				t.Fatalf("reopen cycleResult for blogDir not OK: err=%v diags=%v", r.Err, r.Diags)
			}
			break
		}
	}
	if !found {
		t.Fatalf("reopen results do not contain blogDir %q; got %d results", blogDir, len(results))
	}

	// After reopen the .x.go must still be valid and non-empty.
	post, postErr := os.ReadFile(filepath.Join(blogDir, "page.x.go"))
	if postErr != nil {
		t.Fatalf("page.x.go missing after reopen: %v", postErr)
	}
	if len(post) == 0 {
		t.Fatal("page.x.go empty after reopen")
	}
	if !strings.Contains(string(post), "greeting()") {
		t.Fatalf("page.x.go after reopen should still reference greeting(), got:\n%s", post)
	}
}

// TestWatchDepChange_GoMod verifies that a go.mod file is classified as a dep
// file.  This does not start a full session; it just confirms isDepFile's
// classification so the watch handler's depDirty logic is exercised correctly.
func TestWatchDepChange_GoMod(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"go.mod", "go.sum"} {
		if !isDepFile("/some/module/" + name) {
			t.Errorf("isDepFile(%q) = false, want true", name)
		}
	}
	root := t.TempDir()
	paired := filepath.Join(root, "page.x.go")
	unpaired := filepath.Join(root, "helper.x.go")
	writeFileT(t, filepath.Join(root, "page.gsx"), "package sample\n")
	writeFileT(t, paired, "package sample\n")
	writeFileT(t, unpaired, "package sample\n")
	// Only an .x.go paired with an exact same-base .gsx source is generated
	// output. An unpaired .x.go is ordinary authored Go and must rebuild the
	// dependency graph.
	if isDepFile(paired) {
		t.Error("isDepFile(paired page.x.go) = true, want false")
	}
	if !isDepFile(unpaired) {
		t.Error("isDepFile(unpaired helper.x.go) = false, want true")
	}
}

func TestSourceTrackerSkipsUnchangedFollowupEvents(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	path := filepath.Join(root, "main.go")
	writeFileT(t, path, "package main\n\nfunc main() {}\n")

	tracker := newSourceTracker([]string{root})
	if tracker.changed(path) {
		t.Fatal("unchanged file event reported as changed")
	}

	writeFileT(t, path, "package main\n\nfunc main() { println(\"hi\") }\n")
	if !tracker.changed(path) {
		t.Fatal("content edit was not reported as changed")
	}
	if tracker.changed(path) {
		t.Fatal("unchanged follow-up event after edit reported as changed")
	}
}

func TestSourceTrackerTreatsDeletionAsOneChange(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	path := filepath.Join(root, "page.gsx")
	writeFileT(t, path, "package main\n\ncomponent Page() { <div/> }\n")

	tracker := newSourceTracker([]string{root})
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove source: %v", err)
	}
	if !tracker.changed(path) {
		t.Fatal("deleted source was not reported as changed")
	}
	if tracker.changed(path) {
		t.Fatal("unchanged follow-up deletion event reported as changed")
	}
}

func TestSourceTrackerHonorsExplicitExcludedNamedRoot(t *testing.T) {
	t.Parallel()

	root := filepath.Join(t.TempDir(), "tmp")
	path := filepath.Join(root, "main.go")
	writeFileT(t, path, "package main\n\nfunc main() {}\n")

	tracker := newSourceTracker([]string{root})
	if tracker.changed(path) {
		t.Fatal("unchanged file under an explicitly watched tmp root was not inventoried")
	}
}
