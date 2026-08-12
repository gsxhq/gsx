package gen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestWatchDepChange verifies the goDirty path in the fire handler: when a
// companion .go file in a gsx package is modified, isDepFile does NOT
// classify it as a dep file (that is reserved for go.mod/go.sum, which can
// change the module's own import resolution) — instead it routes as an
// ordinary pending dir and the watch loop calls sess.regenPending(pending,
// false), which regenerates only the dependent closure in place. The Module's
// lazy world reload (triggered inside Generate, not orchestrated by watch)
// still incorporates the new .go file content.
//
// This test simulates that path directly — it modifies a companion .go, asserts
// isDepFile does NOT classify it as a dep file, calls
// sess.regenPending(pending, false), then verifies the package regenerated
// with no error.
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

	// isDepFile gate: a plain .go file is NOT a dep file — only go.mod/go.sum
	// force a full session reopen. helper.go routes through goDirty instead.
	if isDepFile(helperPath) {
		t.Fatalf("helper.go must NOT be classified as dep file by isDepFile")
	}

	// Simulate the dep-change: add a new function to the companion .go.
	// greeting() is kept intact so the .gsx still compiles.
	writeFileT(t, helperPath,
		"package blog\n\nfunc greeting() string { return \"hello\" }\n\nfunc farewell() string { return \"goodbye\" }\n")

	// Drive the goDirty path: regenPending(pending, false) refreshes and
	// invalidates just blogDir, regenerating its reverse closure in place
	// (blogDir's own .gsx makes it non-empty, so it is regenerated directly)
	// rather than reopening every Module in the session.
	results, regenErr := sess.regenPending(map[string]bool{blogDir: true}, false)
	if regenErr != nil {
		t.Fatalf("sess.regenPending() after dep change: %v", regenErr)
	}

	// Regression guard: the cycle must return non-empty results — a go-edit
	// cycle must NOT be silent.
	if len(results) == 0 {
		t.Fatal("sess.regenPending() returned no cycleResults: go-edit cycle is silent (regression)")
	}

	// The blog dir must appear in the results and must have regenerated OK.
	var found bool
	for _, r := range results {
		if r.Dir == blogDir {
			found = true
			if !r.OK {
				t.Fatalf("regenPending cycleResult for blogDir not OK: err=%v diags=%v", r.Err, r.Diags)
			}
			break
		}
	}
	if !found {
		t.Fatalf("regenPending results do not contain blogDir %q; got %d results", blogDir, len(results))
	}

	// After the closure-scoped regen the .x.go must still be valid and
	// non-empty.
	post, postErr := os.ReadFile(filepath.Join(blogDir, "page.x.go"))
	if postErr != nil {
		t.Fatalf("page.x.go missing after regenPending: %v", postErr)
	}
	if len(post) == 0 {
		t.Fatal("page.x.go empty after regenPending")
	}
	if !strings.Contains(string(post), "greeting()") {
		t.Fatalf("page.x.go after regenPending should still reference greeting(), got:\n%s", post)
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
	// output. An unpaired .x.go is ordinary authored Go: it is never a dep
	// file (that classification is reserved for go.mod/go.sum), but it does
	// route as a watchable source event via isGoSourceFile/watchable, so the
	// dependent closure regenerates in place.
	if isDepFile(paired) {
		t.Error("isDepFile(paired page.x.go) = true, want false")
	}
	if isDepFile(unpaired) {
		t.Error("isDepFile(unpaired helper.x.go) = true, want false")
	}
	if watchable(paired) {
		t.Error("watchable(paired page.x.go) = true, want false (ignored generated output)")
	}
	if !watchable(unpaired) {
		t.Error("watchable(unpaired helper.x.go) = false, want true (routes as a source event)")
	}
}

func TestSourceTrackerSkipsUnchangedFollowupEvents(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	path := filepath.Join(root, "main.go")
	writeFileT(t, path, "package main\n\nfunc main() {}\n")

	tracker, err := newSourceTracker([]string{root}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
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

	tracker, err := newSourceTracker([]string{root}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
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

	tracker, err := newSourceTracker([]string{root}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if tracker.changed(path) {
		t.Fatal("unchanged file under an explicitly watched tmp root was not inventoried")
	}
}
