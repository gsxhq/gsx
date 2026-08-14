package gen

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestWatchDepChange proves that authored Go saves regenerate exactly the GSX
// projection of their pre-change reverse dependency closure, through
// regenPending's goDirs lane. Ported from PR #185 (Hossein Bahmani) and
// adapted to main's classification (isDepFile is go.mod/go.sum only; authored
// .go routes through goDirs) and to main's regenDirs-based regeneration.
//
// The fixture is deliberately layered so no single edge could be a
// coincidence:
//
//	model/ (Go) -> bridge/ (Go) -> blog/ (GSX + helper.go) -> site/ (GSX)
//	unrelated/ (GSX, imports nothing)
//
// It covers, in order: a same-package helper edit; a transitive edit through a
// wholly Go-authored intermediary chain; warm-output/fresh-session byte
// equality (exact closure selection cannot hide stale retained Go semantics);
// a mixed Go+GSX save-all in one cycle; and a deleted directory dirtied
// through BOTH lanes at once.
func TestWatchDepChange(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping module-resolution test in -short mode")
	}

	root := t.TempDir()
	writeMod(t, root)

	modelDir := filepath.Join(root, "model")
	bridgeDir := filepath.Join(root, "bridge")
	blogDir := filepath.Join(root, "blog")
	siteDir := filepath.Join(root, "site")
	unrelatedDir := filepath.Join(root, "unrelated")
	modelPath := filepath.Join(modelDir, "model.go")
	helperPath := filepath.Join(blogDir, "helper.go")
	blogGsx := filepath.Join(blogDir, "page.gsx")
	blogXGo := filepath.Join(blogDir, "page.x.go")
	siteXGo := filepath.Join(siteDir, "home.x.go")

	writeFileT(t, modelPath, "package model\n\ntype Label string\n")
	writeFileT(t, filepath.Join(bridgeDir, "bridge.go"), "package bridge\n\nimport \"example.com/m/model\"\n\ntype Label = model.Label\n")
	writeFileT(t, helperPath, "package blog\n\nfunc helper() string { return \"hello\" }\n")
	writeFileT(t, blogGsx, `package blog

import "example.com/m/bridge"

component Page(label bridge.Label) {
	<p>{helper()}: {label}</p>
}
`)
	writeFileT(t, filepath.Join(siteDir, "home.gsx"), `package site

import (
	"example.com/m/blog"
	"example.com/m/bridge"
)

component Home(label bridge.Label) {
	<blog.Page label={label}/>
}
`)
	writeFileT(t, filepath.Join(unrelatedDir, "aside.gsx"), "package unrelated\n\ncomponent Aside() { <aside>unrelated</aside> }\n")

	sess, startup, err := startWatchSessionForTest(watchConfig{paths: []string{root}})
	if err != nil {
		t.Fatalf("startWatchSessionForTest: %v", err)
	}
	assertWatchResults(t, startup, []string{blogDir, siteDir, unrelatedDir})
	initialBlog := readWatchDepOutput(t, blogXGo)
	if !strings.Contains(string(initialBlog), "helper()") {
		t.Fatalf("page.x.go should reference helper(), got:\n%s", initialBlog)
	}

	// isDepFile gate: a plain .go file is NOT a dep file — only go.mod/go.sum
	// force a full session reopen. helper.go routes through goDirs instead.
	if isDepFile(helperPath) {
		t.Fatalf("helper.go must NOT be classified as a dep file by isDepFile")
	}

	// A same-package helper edit affects blog and its importer, not the
	// unrelated GSX package.
	writeFileT(t, helperPath, "package blog\n\nfunc helper() int { return 7 }\n")
	results, err := sess.regenPending(nil, map[string]bool{blogDir: true}, false)
	if err != nil {
		t.Fatalf("regenPending after same-package Go edit: %v", err)
	}
	assertWatchResults(t, results, []string{blogDir, siteDir})
	if updated := readWatchDepOutput(t, blogXGo); bytes.Equal(updated, initialBlog) {
		t.Fatal("same-package helper signature edit left blog/page.x.go unchanged")
	}

	// model -> bridge is a wholly Go-authored edge. The warm graph must
	// traverse that intermediary, then project the closure to blog and site
	// only.
	writeFileT(t, modelPath, "package model\n\ntype Label int\n")
	results, err = sess.regenPending(nil, map[string]bool{modelDir: true}, false)
	if err != nil {
		t.Fatalf("regenPending after transitive Go edit: %v", err)
	}
	assertWatchResults(t, results, []string{blogDir, siteDir})

	// Warm output must be byte-identical to what a cold session produces from
	// the same disk: an exact-closure regeneration that quietly kept stale
	// retained Go semantics would diverge here.
	warm := map[string][]byte{
		blogXGo: readWatchDepOutput(t, blogXGo),
		siteXGo: readWatchDepOutput(t, siteXGo),
	}
	_, freshResults, err := startWatchSessionForTest(watchConfig{paths: []string{root}})
	if err != nil {
		t.Fatalf("fresh startWatchSessionForTest after Go edits: %v", err)
	}
	assertWatchResults(t, freshResults, []string{blogDir, siteDir, unrelatedDir})
	for path, want := range warm {
		if got := readWatchDepOutput(t, path); !bytes.Equal(got, want) {
			t.Fatalf("warm output for %s differs from fresh-session output\nwarm:\n%s\nfresh:\n%s", path, want, got)
		}
	}

	// One editor save-all touching helper.go and page.gsx together dirties the
	// same directory on BOTH lanes. The Go lane refreshes it (committing both
	// files' facts), the GSX lane must not refresh it again, and the closure
	// still regenerates exactly once per dir. regenDirs does re-inventory the
	// dir a third time as part of its own batch — accepted deliberately, see
	// regenPending: measured 0.25ms (single-file dir) to 1.2ms (12-source
	// dir), no packages.Load, and it is what stamps the reload note.
	writeFileT(t, helperPath, "package blog\n\nfunc helper() string { return \"mixed\" }\n")
	writeFileT(t, blogGsx, `package blog

import "example.com/m/bridge"

component Page(label bridge.Label) {
	<p>mixed {helper()}: {label}</p>
}
`)
	results, err = sess.regenPending(map[string]bool{blogDir: true}, map[string]bool{blogDir: true}, false)
	if err != nil {
		t.Fatalf("regenPending after a mixed Go+GSX save: %v", err)
	}
	assertWatchResults(t, results, []string{blogDir, siteDir})
	if mixed := readWatchDepOutput(t, blogXGo); !bytes.Contains(mixed, []byte("mixed ")) {
		t.Fatalf("mixed save did not reach blog/page.x.go:\n%s", mixed)
	}

	// Deleting a GSX dir that also held authored Go dirties both lanes for the
	// same dir. The vanished seed must be skipped (regenerating it would fail
	// the whole cycle on a package the inventory no longer selects, retaining
	// the transaction forever) while its importer regenerates with
	// authoritative missing-package diagnostics — one committed cycle, no
	// operational error.
	if err := os.RemoveAll(blogDir); err != nil {
		t.Fatal(err)
	}
	results, err = sess.regenPending(map[string]bool{blogDir: true}, map[string]bool{blogDir: true}, false)
	if err != nil {
		t.Fatalf("regenPending after deleting blog/: %v", err)
	}
	sawSite := false
	for _, result := range results {
		if result.Err != nil {
			t.Fatalf("deleted-dir cycle carries operational error for %q: %v", result.Dir, result.Err)
		}
		if result.Dir == blogDir && len(result.Written) > 0 {
			t.Fatalf("deleted dir was regenerated: %+v", result)
		}
		if result.Dir == siteDir {
			sawSite = true
			if result.OK || len(result.Diags) == 0 {
				t.Fatalf("site regen after blog deletion should carry missing-package diagnostics, got OK=%v diags=%v", result.OK, result.Diags)
			}
		}
	}
	if !sawSite {
		t.Fatalf("site was not regenerated after blog deletion: %+v", results)
	}
}

func assertWatchResults(t *testing.T, results []cycleResult, wantDirs []string) {
	t.Helper()
	gotDirs := make([]string, 0, len(results))
	for _, result := range results {
		if !result.OK {
			t.Fatalf("regen %s not OK: err=%v diags=%v", result.Dir, result.Err, result.Diags)
		}
		gotDirs = append(gotDirs, result.Dir)
	}
	slices.Sort(gotDirs)
	wantDirs = append([]string(nil), wantDirs...)
	slices.Sort(wantDirs)
	if !slices.Equal(gotDirs, wantDirs) {
		t.Fatalf("regenerated dirs = %v, want exact GSX closure %v", gotDirs, wantDirs)
	}
}

func readWatchDepOutput(t *testing.T, path string) []byte {
	t.Helper()
	output, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read generated output %s: %v", path, err)
	}
	if len(output) == 0 {
		t.Fatalf("generated output %s is empty", path)
	}
	return output
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
