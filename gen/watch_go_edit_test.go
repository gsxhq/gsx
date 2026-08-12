package gen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gsxhq/gsx/internal/diag"
)

// TestWatchSession_GoEditRegeneratesOnlyDependents pins the routing this task
// changes: an authored .go edit is a closure-scoped goDirty event, not a
// depDirty full-session reopen. Module layout:
//
//   - dep/   go-only package (no .gsx)
//   - page/  imports dep, renders dep.Value()
//   - other/ unrelated .gsx package, imports nothing from dep or page
//
// A dep/dep.go edit must regenerate exactly page's reverse closure (dep
// itself is skipped — onlyGeneratedRemains, nothing to emit there) and must
// NOT touch other/. The Module's lazy in-place world reload (Task 1/2, not
// orchestrated here) must still pick up dep's new content/types, including a
// signature change, while staying on the closure-scoped path.
func TestWatchSession_GoEditRegeneratesOnlyDependents(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeMod(t, root)
	writeFileT(t, filepath.Join(root, "dep", "dep.go"),
		"package dep\n\nfunc Value() string { return \"v1\" }\n")
	writeFileT(t, filepath.Join(root, "page", "page.gsx"),
		"package page\n\nimport \"example.com/m/dep\"\n\ncomponent Page() {\n\t<p>{dep.Value()}</p>\n}\n")
	writeFileT(t, filepath.Join(root, "other", "other.gsx"),
		"package other\n\ncomponent Other() {\n\t<p>unrelated</p>\n}\n")

	depDir := filepath.Join(root, "dep")
	pageDir := filepath.Join(root, "page")
	otherDir := filepath.Join(root, "other")

	sess, startup, err := startWatchSessionForTest(watchConfig{paths: []string{root}})
	if err != nil {
		t.Fatalf("startWatchSessionForTest: %v", err)
	}
	for _, r := range startup {
		if !r.OK {
			t.Fatalf("startup regen not OK: dir=%s err=%v diags=%v", r.Dir, r.Err, r.Diags)
		}
	}

	// Step 2: body-only edit to dep.Value — the string signature is
	// unchanged, only the returned literal changes.
	writeFileT(t, filepath.Join(depDir, "dep.go"),
		"package dep\n\nfunc Value() string { return \"v2\" }\n")

	// Simulate the watch loop's routing decision directly: a plain .go write
	// sets goDirty and queues its own dir as pending — see queueWatchSource.
	dirty := newWatchDirtySet()
	dirty.dirs[depDir] = true
	dirty.goDirty = true

	results, rebuild, err := dirty.regenerate(sess.regenPending)
	if err != nil {
		t.Fatalf("regenerate after go edit: %v", err)
	}

	// Step 6: rebuild must be true for the goDirty cycle even though depDirty
	// stayed false throughout.
	if !rebuild {
		t.Fatal("regenerate()'s rebuild return must be true for a goDirty cycle even when depDirty is false")
	}

	// Step 3: closure-scoped regen — exactly page, never other, and dep
	// itself contributes no cycleResult (onlyGeneratedRemains skips it; there
	// is nothing to sweep or generate there).
	if len(results) != 1 {
		t.Fatalf("results = %+v, want exactly one cycleResult (page)", results)
	}
	pageResult := results[0]
	if pageResult.Dir != pageDir {
		t.Fatalf("regenerated dir = %q, want %q", pageResult.Dir, pageDir)
	}
	for _, r := range results {
		if r.Dir == otherDir {
			t.Fatalf("unrelated dir %q regenerated; go-edit routing must be closure-scoped, not session-wide", otherDir)
		}
		if r.Dir == depDir {
			t.Fatalf("go-only dir %q produced a cycleResult; onlyGeneratedRemains should skip it", depDir)
		}
	}

	// Step 4: page regen OK with zero error diagnostics — the world reloaded
	// in place inside Generate, so dep's refreshed content resolved cleanly.
	if !pageResult.OK {
		t.Fatalf("page regen not OK: err=%v diags=%v", pageResult.Err, pageResult.Diags)
	}
	for _, d := range pageResult.Diags {
		if d.Severity == diag.Error {
			t.Fatalf("page regen has an error diagnostic: %v", d)
		}
	}

	before, err := os.ReadFile(filepath.Join(pageDir, "page.x.go"))
	if err != nil {
		t.Fatalf("reading page.x.go after body-only edit: %v", err)
	}

	// Step 5: signature change — string -> int. {dep.Value()} still compiles
	// (TextAny/numeric text-hole support accepts any renderable numeric
	// type), but codegen must emit a different writer call (IntInto instead
	// of Text(string(...))) — proof that fresh types actually flowed through
	// the closure-scoped regen, not stale cached ones.
	writeFileT(t, filepath.Join(depDir, "dep.go"),
		"package dep\n\nfunc Value() int { return 42 }\n")

	dirty.dirs[depDir] = true
	dirty.goDirty = true
	results2, rebuild2, err := dirty.regenerate(sess.regenPending)
	if err != nil {
		t.Fatalf("regenerate after signature change: %v", err)
	}
	if !rebuild2 {
		t.Fatal("regenerate()'s rebuild return must be true for the signature-change goDirty cycle too")
	}
	if len(results2) != 1 || results2[0].Dir != pageDir {
		t.Fatalf("results2 = %+v, want exactly one cycleResult for page", results2)
	}
	if !results2[0].OK {
		t.Fatalf("page regen after signature change not OK: err=%v diags=%v", results2[0].Err, results2[0].Diags)
	}

	after, err := os.ReadFile(filepath.Join(pageDir, "page.x.go"))
	if err != nil {
		t.Fatalf("reading page.x.go after signature change: %v", err)
	}
	if string(before) == string(after) {
		t.Fatal("generated bytes did not change after dep.Value()'s signature changed string -> int")
	}
	if !strings.Contains(string(after), "IntInto") {
		t.Fatalf("expected an int-typed writer call after the signature change, got:\n%s", after)
	}
}

// TestIsDepFileRouting table-extends isDepFile's classification: only
// go.mod/go.sum force a full session reopen. Any other .go file — including
// an unpaired *.x.go, which is authored Go rather than generated output —
// must NOT be classified as a dep file; it routes as an ordinary source event
// instead (see TestQueueWatchSourceRoutesAuthoredGoAsSourceEvent).
func TestIsDepFileRouting(t *testing.T) {
	t.Parallel()
	cases := []struct {
		path string
		want bool
	}{
		{"a/b.go", false},
		{"go.mod", true},
		{"go.sum", true},
		{"/some/module/go.mod", true},
		{"/some/module/go.sum", true},
		{"/some/module/dep/dep.go", false},
	}
	for _, c := range cases {
		if got := isDepFile(c.path); got != c.want {
			t.Errorf("isDepFile(%q) = %v, want %v", c.path, got, c.want)
		}
	}
}

// TestQueueWatchSourceRoutesAuthoredGoAsSourceEvent pins queueWatchSource's
// classification for .go files: a paired generated output (an .x.go whose
// exact same-base .gsx sibling exists) is ignored entirely — not watchable,
// so it never reaches classification. An unpaired .x.go is ordinary authored
// Go: it is watchable, is never a dep file, and sets goDirty (not depDirty)
// while queuing its directory as pending.
func TestQueueWatchSourceRoutesAuthoredGoAsSourceEvent(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	paired := filepath.Join(root, "page.x.go")
	unpaired := filepath.Join(root, "helper.x.go")
	writeFileT(t, filepath.Join(root, "page.gsx"), "package sample\n")
	writeFileT(t, paired, "package sample\n")
	writeFileT(t, unpaired, "package sample\n")

	tracker, err := newSourceTracker([]string{root}, nil)
	if err != nil {
		t.Fatal(err)
	}

	// Edit both after the tracker's baseline inventory so a "changed" check
	// would pass for either — isolating the assertion below to the paired-
	// output exclusion itself, not an unrelated "nothing changed" false.
	writeFileT(t, paired, "package sample\n// changed\n")
	writeFileT(t, unpaired, "package sample\n// changed\n")

	pending := map[string]bool{}
	var depDirty, goDirty bool
	if queueWatchSource(paired, tracker, pending, &depDirty, &goDirty) {
		t.Fatal("paired generated output must be ignored entirely (not watchable)")
	}
	if depDirty || goDirty || len(pending) != 0 {
		t.Fatalf("paired generated output must not affect classification state: depDirty=%v goDirty=%v pending=%v", depDirty, goDirty, pending)
	}

	if !queueWatchSource(unpaired, tracker, pending, &depDirty, &goDirty) {
		t.Fatal("unpaired .x.go must route as a watchable source event")
	}
	if depDirty {
		t.Error("unpaired .x.go must NOT be classified as a dep file")
	}
	if !goDirty {
		t.Error("unpaired .x.go must set goDirty")
	}
	if !pending[root] {
		t.Errorf("unpaired .x.go must queue its directory as pending, got pending=%v", pending)
	}
}
