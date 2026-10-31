package gen

import (
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gsxhq/gsx/internal/codegen"
)

// TestWatchSession_SiblingGoSymbol proves that Module-based warm regen correctly
// resolves symbols defined in hand-written .go files (not in .gsx files). A
// package whose .gsx references a symbol in a sibling .go file (the
// structpages/blog pattern) must not report "undefined" on warm regeneration.
func TestWatchSession_SiblingGoSymbol(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	write := func(p, s string) {
		full := filepath.Join(root, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module example.com/m\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+gsxModuleDir(t)+"\n")
	// helper() lives in a hand-written .go file, NOT a .gsx.
	write("blog/helper.go", "package blog\n\nfunc helper(n int) string { return \"r\" }\n")
	write("blog/page.gsx", "package blog\n\ncomponent Page(items []int) {\n\t<p>{helper(len(items))}</p>\n}\n")

	blogDir := filepath.Join(root, "blog")

	// startWatchSessionForTest opens a Module and runs startup regenDir for blogDir, fully
	// populating the import graph (including the sibling .go file).
	s, startup, err := startWatchSessionForTest(watchConfig{paths: []string{blogDir}})
	if err != nil {
		t.Fatalf("startWatchSessionForTest: %v", err)
	}
	for _, r := range startup {
		if !r.OK {
			t.Fatalf("startup regen not OK: err=%v diags=%v", r.Err, r.Diags)
		}
		for _, d := range r.Diags {
			if strings.Contains(d.Message, "helper") {
				t.Fatalf("startup regen reported a false undefined for a sibling-.go symbol: %s", d.Message)
			}
		}
	}

	// Warm regen (second call) must still resolve helper() from the .go file.
	r := s.regenDir(blogDir)
	if !r.OK {
		t.Fatalf("warm regenDir must resolve the sibling-.go symbol, got: err=%v diags=%v", r.Err, r.Diags)
	}
	for _, d := range r.Diags {
		if strings.Contains(d.Message, "helper") {
			t.Fatalf("warm regen reported a false undefined for a sibling-.go symbol: %s", d.Message)
		}
	}
}

// TestWatchSession_CrossPackage proves that the Module resolver lets a package
// reference a cross-package component (views → comp.Card) and warm regenDir
// produces output that correctly calls the cross-package component.
func TestWatchSession_CrossPackage(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	write := func(p, s string) {
		full := filepath.Join(root, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module example.com/m\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+gsxModuleDir(t)+"\n")
	write("comp/card.gsx", "package comp\n\ncomponent Card(title string) {\n\t<div class=\"card\">{title}</div>\n}\n")
	write("views/page.gsx", "package views\n\nimport \"example.com/m/comp\"\n\ncomponent Page() {\n\t<comp.Card title=\"hi\"/>\n}\n")

	compDir := filepath.Join(root, "comp")
	viewsDir := filepath.Join(root, "views")

	// startWatchSessionForTest runs startup regenDir for both dirs, writing their .x.go
	// files and populating the cross-package import graph.
	s, _, err := startWatchSessionForTest(watchConfig{paths: []string{compDir, viewsDir}})
	if err != nil {
		t.Fatalf("startWatchSessionForTest: %v", err)
	}

	// Warm regen of views: the Module must resolve comp.Card across packages.
	r := s.regenDir(viewsDir)
	if !r.OK {
		t.Fatalf("warm regenDir(views): err=%v diags=%v", r.Err, r.Diags)
	}
	// The regenerated views/page.x.go must call comp.Card.
	xgo, err := os.ReadFile(filepath.Join(viewsDir, "page.x.go"))
	if err != nil {
		t.Fatalf("reading page.x.go: %v", err)
	}
	if !strings.Contains(string(xgo), "comp.Card") {
		t.Fatalf("regenerated page.x.go does not reference comp.Card:\n%s", xgo)
	}
}

func writeFileT(t *testing.T, path, s string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeMod(t *testing.T, root string) {
	t.Helper()
	writeFileT(t, filepath.Join(root, "go.mod"), "module example.com/m\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+gsxModuleDir(t)+"\n")
}

// TestWatchSession_WarmRegen proves that a pure .gsx edit regenerates via the
// warm Module and updates the .x.go on disk.
func TestWatchSession_WarmRegen(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeMod(t, root)
	gsxPath := filepath.Join(root, "views", "page.gsx")
	writeFileT(t, gsxPath, "package views\n\ncomponent Page() {\n\t<h1>one</h1>\n}\n")

	s, _, err := startWatchSessionForTest(watchConfig{paths: []string{filepath.Join(root, "views")}})
	if err != nil {
		t.Fatalf("startWatchSessionForTest: %v", err)
	}
	// Edit the source, then warm-regen.
	writeFileT(t, gsxPath, "package views\n\ncomponent Page() {\n\t<h1>two</h1>\n}\n")
	r := s.regenDir(filepath.Join(root, "views"))
	if !r.OK {
		t.Fatalf("regenDir not OK: err=%v diags=%v", r.Err, r.Diags)
	}
	xgo, _ := os.ReadFile(filepath.Join(root, "views", "page.x.go"))
	// Coalesced static writes emit `S("<h1>two</h1>")`, so assert on the content.
	if !strings.Contains(string(xgo), `two</h1>`) {
		t.Fatalf("page.x.go not updated to \"two\":\n%s", xgo)
	}
}

func TestWatchSession_RendererEditInvalidatesConsumer(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping module-resolution test in -short mode")
	}
	root := t.TempDir()
	writeMod(t, root)
	writeFileT(t, filepath.Join(root, "pg", "pg.go"), `package pg

type Timestamptz struct { Label string }
`)
	rendererDir := filepath.Join(root, "renderers")
	rendererPath := filepath.Join(rendererDir, "renderers.gsx")
	writeFileT(t, filepath.Join(rendererDir, "package.go"), "package renderers\n")
	writeFileT(t, rendererPath, `package renderers

import "example.com/m/pg"

func Timestamptz(v pg.Timestamptz) string {
	return v.Label
}
`)
	viewsDir := filepath.Join(root, "views")
	viewsPath := filepath.Join(viewsDir, "views.gsx")
	viewsXGo := filepath.Join(viewsDir, "views.x.go")
	writeFileT(t, viewsPath, `package views

import "example.com/m/pg"

component Show(sample pg.Timestamptz) {
	<div>{sample}</div>
}
`)

	s, startup, err := startWatchSessionForTest(watchConfig{
		paths: []string{rendererDir, viewsDir},
		renderers: []codegen.RendererAlias{{
			TypeKey:  "example.com/m/pg.Timestamptz",
			PkgPath:  "example.com/m/renderers",
			FuncName: "Timestamptz",
		}},
	})
	if err != nil {
		t.Fatalf("startWatchSessionForTest: %v", err)
	}
	for _, r := range startup {
		if !r.OK {
			t.Fatalf("startup regen %s: err=%v diags=%v", r.Dir, r.Err, r.Diags)
		}
	}
	initial, err := os.ReadFile(viewsXGo)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(codeShape(t, string(initial)), `_gsxgw.Text(string(_gsxf0.Timestamptz((sample))))`) {
		t.Fatalf("initial consumer did not use string renderer lowering:\n%s", initial)
	}

	// Edit only renderers.gsx on disk. The renderer dir is not a source import
	// of views, but its resolved local GSX declaration is an implicit dependency:
	// this cycle must regenerate both dirs and reclassify the consumer lowering.
	writeFileT(t, rendererPath, `package renderers

import (
	"example.com/m/pg"
	"github.com/gsxhq/gsx"
)

func Timestamptz(v pg.Timestamptz) gsx.Node {
	return <time>{v.Label}</time>
}
`)
	results, err := s.regenPending(map[string]bool{rendererDir: true}, nil, false)
	if err != nil {
		t.Fatalf("regenPending: %v", err)
	}
	dirs := map[string]bool{}
	for _, r := range results {
		dirs[r.Dir] = true
		if !r.OK {
			t.Fatalf("regen %s: err=%v diags=%v", r.Dir, r.Err, r.Diags)
		}
	}
	if !dirs[rendererDir] || !dirs[viewsDir] {
		t.Fatalf("renderer edit regenerated dirs %v, want renderer and consumer", dirs)
	}
	updated, err := os.ReadFile(viewsXGo)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(codeShape(t, string(updated)), `_gsxgw.Node(ctx, _gsxf0.Timestamptz((sample)))`) {
		t.Fatalf("consumer did not update to Node renderer lowering:\n%s", updated)
	}
}

// TestWatchSession_RegenError proves that a broken .gsx yields OK=false with
// diagnostics.
func TestWatchSession_RegenError(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeMod(t, root)
	gsxPath := filepath.Join(root, "views", "page.gsx")
	writeFileT(t, gsxPath, "package views\n\ncomponent Page() {\n\t<h1>{undefinedSym}</h1>\n}\n")

	s, _, err := startWatchSessionForTest(watchConfig{paths: []string{filepath.Join(root, "views")}})
	if err != nil {
		t.Fatalf("startWatchSessionForTest: %v", err)
	}
	r := s.regenDir(filepath.Join(root, "views"))
	if r.OK || len(r.Diags) == 0 {
		t.Fatalf("expected OK=false with diagnostics, got OK=%v diags=%v", r.OK, r.Diags)
	}
}

// TestWatchSession_BadMergerRefused proves that startWatchSessionForTest returns a clear
// signature error when the configured class_merger has the wrong type, instead
// of silently emitting uncompilable .x.go files on each regen cycle.
func TestWatchSession_BadMergerRefused(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping go-build test in -short mode")
	}
	root := t.TempDir()
	writeMod(t, root)
	// A .gsx file so discoverDirs finds the views dir.
	writeFileT(t, filepath.Join(root, "views", "page.gsx"),
		"package views\n\ncomponent Page() {\n\t<h1>hello</h1>\n}\n")
	// A merger package with a bad signature (returns int, not string).
	writeFileT(t, filepath.Join(root, "mrg", "mrg.go"),
		"package mrg\n\nfunc Merge(t []string) int { return 0 }\n")

	s, err := prepareWatchSession(watchConfig{
		paths:       []string{filepath.Join(root, "views")},
		classMerger: &codegen.ClassMergerRef{PkgPath: "example.com/m/mrg", FuncName: "Merge"},
	})
	if err != nil {
		t.Fatalf("prepareWatchSession must not validate mergers synchronously: %v", err)
	}
	s.startMergerValidation()
	s.startMergerValidation() // idempotent
	for range 2 {             // safe to await more than once, same error each time
		err = s.awaitMergerValidation()
		if err == nil {
			t.Fatal("want error for bad-signature merger under --watch, got nil")
		}
		if !strings.Contains(err.Error(), "func([]string) string") {
			t.Fatalf("want signature error, got: %v", err)
		}
	}
}

// TestRunWatch_BadMergerEmitsAndWritesNothing: with a bad merger,
// generate --watch exits 1 with the signature error, emits no start event and
// writes no .x.go.
func TestRunWatch_BadMergerEmitsAndWritesNothing(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping go-build test in -short mode")
	}
	root := t.TempDir()
	writeMod(t, root)
	writeFileT(t, filepath.Join(root, "views", "page.gsx"),
		"package views\n\ncomponent Page() {\n\t<h1>hello</h1>\n}\n")
	writeFileT(t, filepath.Join(root, "mrg", "mrg.go"),
		"package mrg\n\nfunc Merge(t []string) int { return 0 }\n")
	out, errb := &syncBuf{}, &syncBuf{}
	// Validation failing returns 1 before the loop reads stop; if it wrongly
	// passes, the run reaches the loop and exits 0 once stop closes.
	stop := make(chan struct{})
	timer := time.AfterFunc(30*time.Second, func() { close(stop) })
	defer timer.Stop()
	code := runWatchWithStop(watchConfig{
		paths:       []string{filepath.Join(root, "views")},
		format:      "ndjson",
		stdout:      out,
		stderr:      errb,
		classMerger: &codegen.ClassMergerRef{PkgPath: "example.com/m/mrg", FuncName: "Merge"},
	}, stop)
	if code != 1 {
		t.Fatalf("exit = %d, want 1 (0 means the bad merger passed validation); stdout=%s stderr=%s", code, out, errb)
	}
	if strings.Contains(out.String(), `"event":"start"`) || !strings.Contains(out.String(), "func([]string) string") {
		t.Fatalf("want only the signature error, got stdout=%s", out)
	}
	if m, _ := filepath.Glob(filepath.Join(root, "views", "*.x.go")); len(m) != 0 {
		t.Fatalf("wrote generated files: %v", m)
	}
}

// TestWatchSession_NoMergerAwaitIsImmediate: without a configured merger,
// awaitMergerValidation returns nil without blocking, started or not.
func TestWatchSession_NoMergerAwaitIsImmediate(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeMod(t, root)
	writeFileT(t, filepath.Join(root, "views", "page.gsx"),
		"package views\n\ncomponent Page() {\n\t<h1>hello</h1>\n}\n")
	s, err := prepareWatchSession(watchConfig{paths: []string{filepath.Join(root, "views")}})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 2)
	go func() { done <- s.awaitMergerValidation() }() // never started
	s.startMergerValidation()
	go func() { done <- s.awaitMergerValidation() }()
	for range 2 {
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("await = %v, want nil", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("awaitMergerValidation blocked without a merger")
		}
	}
}

// TestRegenPending_RemovesOrphanOnSoleGsxDeleted: a dir whose only .gsx is
// deleted is skipped by regenPending's onlyGeneratedRemains branch (nothing
// left to regenerate) — but its now-orphaned .x.go must still be removed and
// reported via cycleResult.Removed, not silently left on disk.
func TestRegenPending_RemovesOrphanOnSoleGsxDeleted(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeMod(t, root)
	viewsDir := filepath.Join(root, "views")
	gsxPath := filepath.Join(viewsDir, "page.gsx")
	writeFileT(t, gsxPath, "package views\n\ncomponent Page() {\n\t<h1>ok</h1>\n}\n")

	s, startup, err := startWatchSessionForTest(watchConfig{paths: []string{viewsDir}})
	if err != nil {
		t.Fatalf("startWatchSessionForTest: %v", err)
	}
	for _, r := range startup {
		if !r.OK {
			t.Fatalf("startup not OK: %v %v", r.Err, r.Diags)
		}
	}
	xgo := filepath.Join(viewsDir, "page.x.go")
	if _, err := os.Stat(xgo); err != nil {
		t.Fatalf("page.x.go not written by startup: %v", err)
	}

	if err := os.Remove(gsxPath); err != nil {
		t.Fatal(err)
	}
	results, err := s.regenPending(map[string]bool{viewsDir: true}, nil, false)
	if err != nil {
		t.Fatalf("regenPending: %v", err)
	}
	if _, err := os.Stat(xgo); !os.IsNotExist(err) {
		t.Fatalf("orphaned page.x.go not removed by regenPending (err=%v)", err)
	}
	var found bool
	for _, r := range results {
		for _, rm := range r.Removed {
			if rm == xgo {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("cycleResult.Removed does not report page.x.go; results=%+v", results)
	}
}

// TestRegenPending_MultiFileDirRegeneratesSiblingAndRemovesOrphan: the
// multi-file variant — deleting one of two .gsx in a dir keeps the dir in
// discovery (its sibling still exists), so regenPending routes it through the
// normal Invalidate/Dependents/regenDir path. regenDir's dir-scoped sweep must
// remove the orphan while the sibling regenerates cleanly.
func TestRegenPending_MultiFileDirRegeneratesSiblingAndRemovesOrphan(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeMod(t, root)
	viewsDir := filepath.Join(root, "views")
	keepPath := filepath.Join(viewsDir, "keep.gsx")
	dropPath := filepath.Join(viewsDir, "drop.gsx")
	writeFileT(t, keepPath, "package views\n\ncomponent Keep() {\n\t<h1>keep</h1>\n}\n")
	writeFileT(t, dropPath, "package views\n\ncomponent Drop() {\n\t<h1>drop</h1>\n}\n")

	s, startup, err := startWatchSessionForTest(watchConfig{paths: []string{viewsDir}})
	if err != nil {
		t.Fatalf("startWatchSessionForTest: %v", err)
	}
	for _, r := range startup {
		if !r.OK {
			t.Fatalf("startup not OK: %v %v", r.Err, r.Diags)
		}
	}
	dropXgo := filepath.Join(viewsDir, "drop.x.go")
	keepXgo := filepath.Join(viewsDir, "keep.x.go")
	if _, err := os.Stat(dropXgo); err != nil {
		t.Fatalf("drop.x.go not written by startup: %v", err)
	}

	if err := os.Remove(dropPath); err != nil {
		t.Fatal(err)
	}
	results, err := s.regenPending(map[string]bool{viewsDir: true}, nil, false)
	if err != nil {
		t.Fatalf("regenPending: %v", err)
	}
	if _, err := os.Stat(dropXgo); !os.IsNotExist(err) {
		t.Fatalf("orphaned drop.x.go not removed (err=%v)", err)
	}
	if _, err := os.Stat(keepXgo); err != nil {
		t.Fatalf("sibling keep.x.go missing after regen: %v", err)
	}
	var found bool
	for _, r := range results {
		if !r.OK {
			t.Fatalf("regen not OK: err=%v diags=%v", r.Err, r.Diags)
		}
		for _, rm := range r.Removed {
			if rm == dropXgo {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("cycleResult.Removed does not report drop.x.go; results=%+v", results)
	}
}

// TestWatchSessionStartup_SweepsOrphanAtColdStart proves that startWatchSessionForTest
// performs the same walk-level orphan sweep as the batch path's
// generateCached (sweepOrphanDirs). Without it, a directory whose only .gsx
// was deleted before `gsx dev` ever started drops out of discovery entirely
// (discoverDirs only returns dirs that still directly contain a .gsx) and its
// stale gsx-owned .x.go would survive cold start indefinitely (Task 8
// reviewer's gap).
func TestWatchSessionStartup_SweepsOrphanAtColdStart(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeMod(t, root)
	// views/ has a live .gsx — discovered and regenerated normally.
	writeFileT(t, filepath.Join(root, "views", "page.gsx"),
		"package views\n\ncomponent Page() {\n\t<h1>ok</h1>\n}\n")
	// old/ has ONLY a gsx-owned orphan .x.go — no .gsx sibling at all, as if
	// the last .gsx in this dir was deleted before `gsx dev` ever ran.
	oldXgo := filepath.Join(root, "old", "old.x.go")
	writeFileT(t, oldXgo, gsxGeneratedHeader+"\n\npackage old\n\nfunc unused() {}\n")
	// Negative guard: a header-less *.x.go (hand-written, or not gsx-owned)
	// in the same orphan-only dir must survive the sweep untouched.
	notOurs := filepath.Join(root, "old", "notours.x.go")
	writeFileT(t, notOurs, "package old\n\nfunc keep() {}\n")

	s, startup, err := startWatchSessionForTest(watchConfig{paths: []string{root}})
	if err != nil {
		t.Fatalf("startWatchSessionForTest: %v", err)
	}
	if s == nil {
		t.Fatal("startWatchSessionForTest returned nil session")
	}

	if _, statErr := os.Stat(oldXgo); !os.IsNotExist(statErr) {
		t.Fatalf("old/old.x.go (orphan, no .gsx) survived startWatchSessionForTest cold start (err=%v)", statErr)
	}
	if _, statErr := os.Stat(notOurs); statErr != nil {
		t.Fatalf("old/notours.x.go (header-less, not gsx-owned) was removed by the sweep: %v", statErr)
	}

	// The removal must be reported through the startup cycleResults, exactly
	// like a warm-loop orphan removal (regenPending's onlyGeneratedRemains
	// branch), so dev's overlay/log output isn't silently missing it.
	var found bool
	for _, r := range startup {
		for _, rm := range r.Removed {
			if rm == oldXgo {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("startup cycleResults do not report old/old.x.go removed; startup=%+v", startup)
	}

	// views/page.x.go must still have been generated normally.
	if _, statErr := os.Stat(filepath.Join(root, "views", "page.x.go")); statErr != nil {
		t.Fatalf("views/page.x.go not written by startup: %v", statErr)
	}
}

// TestWatchSession_Reopen_SweepsOrphanAtColdStart is the reopen() analogue of
// TestWatchSessionStartup_SweepsOrphanAtColdStart: reopen() re-runs the same
// discoverDirs → per-dir regen sequence as startWatchSessionForTest (after a dep-file
// change), so it must sweep walk-level orphans too, not just the initial
// startWatchSessionForTest call.
func TestWatchSession_Reopen_SweepsOrphanAtColdStart(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeMod(t, root)
	viewsGsx := filepath.Join(root, "views", "page.gsx")
	writeFileT(t, viewsGsx, "package views\n\ncomponent Page() {\n\t<h1>ok</h1>\n}\n")

	s, startup, err := startWatchSessionForTest(watchConfig{paths: []string{root}})
	if err != nil {
		t.Fatalf("startWatchSessionForTest: %v", err)
	}
	for _, r := range startup {
		if !r.OK {
			t.Fatalf("startup not OK: err=%v diags=%v", r.Err, r.Diags)
		}
	}

	// Now, out from under the session (simulating an edit that happened while
	// `gsx dev` wasn't watching, or a race the debounce collapsed), delete the
	// sole .gsx of a brand-new orphan-only dir and drop a stale gsx-owned
	// .x.go there directly — reopen() must sweep it just like cold start does.
	oldXgo := filepath.Join(root, "old", "old.x.go")
	writeFileT(t, oldXgo, gsxGeneratedHeader+"\n\npackage old\n\nfunc unused() {}\n")

	results, err := s.reopen()
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if _, statErr := os.Stat(oldXgo); !os.IsNotExist(statErr) {
		t.Fatalf("old/old.x.go survived reopen (err=%v)", statErr)
	}
	var found bool
	for _, r := range results {
		for _, rm := range r.Removed {
			if rm == oldXgo {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("reopen results do not report old/old.x.go removed; results=%+v", results)
	}
}

// gsxModuleDir returns the absolute path of this gsx module checkout, for the
// test fixture's replace directive.
func gsxModuleDir(t *testing.T) string {
	t.Helper()
	// gen/ is one level under the module root.
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Dir(wd)
}

// TestWatchSession_PoisonOnRegenError: a failed warm regen writes a poison
// .x.go (OK=false, Written non-empty), and the following fixed regen
// overwrites it with clean output.
func TestWatchSession_PoisonOnRegenError(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeMod(t, root)
	viewsDir := filepath.Join(root, "views")
	gsxPath := filepath.Join(viewsDir, "page.gsx")
	writeFileT(t, gsxPath, "package views\n\ncomponent Page() {\n\t<h1>ok</h1>\n}\n")

	s, startup, err := startWatchSessionForTest(watchConfig{paths: []string{viewsDir}})
	if err != nil {
		t.Fatalf("startWatchSessionForTest: %v", err)
	}
	for _, r := range startup {
		if !r.OK {
			t.Fatalf("clean startup not OK: %v %v", r.Err, r.Diags)
		}
	}

	// Break it → warm regen must poison.
	writeFileT(t, gsxPath, "package views\n\ncomponent Page() {\n\t<h1>{undefinedSym}</h1>\n}\n")
	r := s.regenDir(viewsDir)
	if r.OK {
		t.Fatal("expected OK=false")
	}
	if len(r.Written) == 0 {
		t.Fatal("failed cycle wrote no poison")
	}
	b, err := os.ReadFile(filepath.Join(viewsDir, "page.x.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "GSX GENERATION FAILED") {
		t.Errorf("page.x.go not poisoned:\n%s", b)
	}

	// Fix it → warm regen overwrites the poison (gqlgen-trap regression:
	// the poison on disk must not block the warm Module's regeneration).
	writeFileT(t, gsxPath, "package views\n\ncomponent Page() {\n\t<h1>ok</h1>\n}\n")
	r = s.regenDir(viewsDir)
	if !r.OK {
		t.Fatalf("regen after fix not OK (sticky poison): %v %v", r.Err, r.Diags)
	}
	b, err = os.ReadFile(filepath.Join(viewsDir, "page.x.go"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "GSX GENERATION FAILED") {
		t.Error("poison not overwritten after fix")
	}
}

// TestWatchSessionStartup_NoGsxAnywhere proves that a module remains a valid watch
// target before its first .gsx file exists. The session must stay live so a
// later package/file creation can be observed. Cold-start orphan cleanup still
// runs even though there are no package directories to regenerate yet.
func TestWatchSessionStartup_NoGsxAnywhere(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeMod(t, root)
	// old/ has ONLY a gsx-owned orphan .x.go — no .gsx anywhere in the tree,
	// as if the last .gsx was deleted before this watch session ever started.
	oldXgo := filepath.Join(root, "old", "old.x.go")
	writeFileT(t, oldXgo, gsxGeneratedHeader+"\n\npackage old\n\nfunc unused() {}\n")

	s, startup, err := startWatchSessionForTest(watchConfig{paths: []string{root}})
	if err != nil {
		t.Fatalf("startWatchSessionForTest on an empty module: %v", err)
	}
	if s == nil {
		t.Fatal("startWatchSessionForTest returned a nil session")
	}
	if s.root != root {
		t.Fatalf("session root = %q, want %q", s.root, root)
	}
	if s.modules[root] == nil {
		t.Fatalf("session did not open the requested module root %q", root)
	}
	if len(startup) != 1 || !startup[0].OK || len(startup[0].Removed) != 1 || startup[0].Removed[0] != oldXgo {
		t.Fatalf("startup = %+v, want one successful orphan-removal result for %s", startup, oldXgo)
	}
	if _, statErr := os.Stat(oldXgo); !os.IsNotExist(statErr) {
		t.Fatalf("old/old.x.go (orphan, no .gsx anywhere) survived startWatchSessionForTest cold start (err=%v)", statErr)
	}
}

func TestWatchSessionStartup_RegistersNestedModuleWithoutGsx(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeMod(t, root)
	nested := filepath.Join(root, "nested")
	writeFileT(t, filepath.Join(nested, "go.mod"), "module example.com/nested\n\ngo 1.26.1\n")

	s, startup, err := startWatchSessionForTest(watchConfig{paths: []string{root}})
	if err != nil {
		t.Fatalf("startWatchSessionForTest: %v", err)
	}
	if len(startup) != 0 {
		t.Fatalf("startup = %+v, want no generation cycles", startup)
	}
	for _, moduleRoot := range []string{root, nested} {
		if s.modules[moduleRoot] == nil {
			t.Errorf("empty module root %q was not registered", moduleRoot)
		}
	}
	if len(s.watchRoots) != 1 || s.watchRoots[0] != root {
		t.Fatalf("watchRoots = %v, want compact owning tree [%s]", s.watchRoots, root)
	}
}

func TestWatchSessionStartup_HonorsExplicitExcludedNamedRoot(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeMod(t, root)
	explicit := filepath.Join(root, "tmp")
	if err := os.MkdirAll(explicit, 0o755); err != nil {
		t.Fatal(err)
	}

	s, _, err := startWatchSessionForTest(watchConfig{paths: []string{explicit}})
	if err != nil {
		t.Fatalf("startWatchSessionForTest: %v", err)
	}
	want := []string{root, explicit}
	if !slices.Equal(s.watchRoots, want) {
		t.Fatalf("watchRoots = %v, want %v; an explicit tmp root must not be lost behind the owning-root exclusion", s.watchRoots, want)
	}
}

// TestWatchSessionStartup_CachedMatchesUncached pins that a watch session's
// cached startup is indistinguishable from today's uncached one: the same
// tree, started once with the cache off and once against a cache warmed by a
// batch generate, yields DeepEqual startup results (DurMs aside) and
// byte-identical files on disk. Only the hit dirs land in warmDirs.
func TestWatchSessionStartup_CachedMatchesUncached(t *testing.T) {
	const plain = "package %s\n\ncomponent %s() {\n\t<p>%s</p>\n}\n"
	src := func(pkg, name, text string) string { return fmt.Sprintf(plain, pkg, name, text) }
	// base is one module with: hit dirs (one carrying a child-element-spacing
	// warning), a dir whose sibling .gsx was deleted (miss + in-dir orphan), a
	// dir whose only .gsx was deleted (walk-level orphan), an edited dir and
	// its importer (misses), and a codegen-error dir (never stored).
	base := func(t *testing.T, root string) {
		writeMod(t, root)
		writeFileT(t, filepath.Join(root, "hit", "hit.gsx"), src("hit", "Hit", "hit"))
		writeFileT(t, filepath.Join(root, "warn", "warn.gsx"), "package warn\n\ncomponent A(name string) {\n\t<p>\n\t\tHi\n\t\t<b>{name}</b>\n\t</p>\n}\n")
		writeFileT(t, filepath.Join(root, "sib", "keep.gsx"), src("sib", "Keep", "keep"))
		writeFileT(t, filepath.Join(root, "sib", "gone.gsx"), src("sib", "Gone", "gone"))
		writeFileT(t, filepath.Join(root, "old", "old.gsx"), src("old", "Old", "old"))
		writeFileT(t, filepath.Join(root, "edited", "edited.gsx"), src("edited", "Edited", "before"))
		writeFileT(t, filepath.Join(root, "user", "user.gsx"), "package user\n\nimport \"example.com/m/edited\"\n\ncomponent User() {\n\t<edited.Edited />\n}\n")
		writeFileT(t, filepath.Join(root, "bad", "bad.gsx"), "package bad\n\ncomponent Bad() {\n\t<p>{undefinedName}</p>\n}\n")
	}
	// afterWarm makes the post-generate edits: a .gsx edit, two .gsx
	// deletions, and a deleted output that the warning-carrying hit rewrites.
	afterWarm := func(t *testing.T, root string) {
		writeFileT(t, filepath.Join(root, "edited", "edited.gsx"), src("edited", "Edited", "after"))
		for _, p := range []string{filepath.Join(root, "sib", "gone.gsx"), filepath.Join(root, "old", "old.gsx"), filepath.Join(root, "warn", "warn.x.go")} {
			if err := os.Remove(p); err != nil {
				t.Fatal(err)
			}
		}
	}
	rows := []struct {
		name    string
		fixture func(t *testing.T, root string)
		edit    func(t *testing.T, root string) // after afterWarm, if set
		goWork  bool                            // run both sessions under a go.work: uncacheable
		// discovered resolves each module's config as generate --watch does
		// (discoveredModuleConfig), with noCache as its --no-cache flag.
		discovered, noCache bool
		// readOnlyAfter makes the dir of this root-relative output read-only
		// once restore writes it, failing that dir's next write.
		readOnlyAfter string
		wantWarm      []string
	}{
		{name: "one module", fixture: base, wantWarm: []string{"hit", "warn"}},
		{
			// multi's restore writes a.x.go, then fails on z.x.go: the dir
			// and every later hit (warn) fall back to the Module, and multi's
			// result still reports the a.x.go the restore wrote.
			name: "restore fails after a write",
			fixture: func(t *testing.T, root string) {
				base(t, root)
				writeFileT(t, filepath.Join(root, "multi", "a.gsx"), src("multi", "A", "a"))
				writeFileT(t, filepath.Join(root, "multi", "z.gsx"), src("multi", "Z", "z"))
			},
			edit: func(t *testing.T, root string) {
				for _, p := range []string{filepath.Join(root, "multi", "a.x.go"), filepath.Join(root, "multi", "z.x.go")} {
					if err := os.Remove(p); err != nil {
						t.Fatal(err)
					}
				}
			},
			readOnlyAfter: "multi/a.x.go",
			wantWarm:      []string{"hit"},
		},
		{
			name: "two modules",
			fixture: func(t *testing.T, root string) {
				base(t, root)
				nested := filepath.Join(root, "other")
				writeFileT(t, filepath.Join(nested, "go.mod"), "module example.com/other\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+gsxModuleDir(t)+"\n")
				writeFileT(t, filepath.Join(nested, "ohit", "ohit.gsx"), src("ohit", "OHit", "other hit"))
				writeFileT(t, filepath.Join(nested, "obad", "obad.gsx"), "package obad\n\ncomponent OBad() {\n\t<p>{alsoUndefined}</p>\n}\n")
			},
			wantWarm: []string{"hit", "other/ohit", "warn"},
		},
		{name: "uncacheable go.work", fixture: base, goWork: true},
		{name: "discovered config", fixture: base, discovered: true, wantWarm: []string{"hit", "warn"}},
		// --no-cache: the cache is warm, yet no dir is restored from it.
		{name: "discovered config with --no-cache", fixture: base, discovered: true, noCache: true},
	}
	cfg := moduleGenerateConfig{useCache: true}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			if row.readOnlyAfter != "" && os.Geteuid() == 0 {
				t.Skip("root ignores directory permissions")
			}
			root := t.TempDir()
			cacheRoot := t.TempDir()
			row.fixture(t, root)
			t.Setenv("GSXCACHE", cacheRoot)
			// The codegen-error dirs make the warming generate fail with
			// diagnostics; only an operational error spoils the fixture.
			warmCfg := fixedModuleConfig(cfg)
			startCfg := warmCfg
			if row.discovered {
				warmCfg = discoveredModuleConfig(config{}, false)
				startCfg = discoveredModuleConfig(config{}, row.noCache)
			}
			if res, _, _ := generateCachedWithReport([]string{root}, warmCfg); len(res.Errs) > 0 {
				t.Fatalf("warming generate: %v", res.Errs)
			}
			afterWarm(t, root)
			if row.edit != nil {
				row.edit(t, root)
			}
			if row.goWork {
				work := filepath.Join(t.TempDir(), "go.work")
				writeFileT(t, work, "go 1.26.1\n\nuse "+root+"\n")
				t.Setenv("GOWORK", work)
			}
			snapshot := readTreeT(t, root)

			start := func(cache string) (*watchSession, []cycleResult, map[string]string) {
				t.Setenv("GSXCACHE", cache)
				if row.readOnlyAfter != "" {
					target := filepath.Join(root, filepath.FromSlash(row.readOnlyAfter))
					afterRestoreWrite = func(written string) {
						if written == target {
							if err := os.Chmod(filepath.Dir(target), 0o555); err != nil {
								t.Error(err)
							}
						}
					}
					t.Cleanup(func() { _ = os.Chmod(filepath.Dir(target), 0o755) })
				}
				s, startup, err := startWatchSessionForTest(watchConfig{paths: []string{root}, moduleConfig: startCfg})
				if row.readOnlyAfter != "" {
					afterRestoreWrite = nil
					if err := os.Chmod(filepath.Dir(filepath.Join(root, filepath.FromSlash(row.readOnlyAfter))), 0o755); err != nil {
						t.Fatal(err)
					}
				}
				if err != nil {
					t.Fatalf("startWatchSessionForTest(GSXCACHE=%s): %v", cache, err)
				}
				for i := range startup {
					startup[i].DurMs = 0
					// A failed write names its random temp file, on either path.
					if err := startup[i].Err; err != nil {
						startup[i].Err = errors.New(tempNameRE.ReplaceAllString(err.Error(), ".gsx-w-*"))
					}
				}
				return s, startup, readTreeT(t, root)
			}
			uncachedSession, uncached, uncachedDisk := start("off")
			writeTreeT(t, root, snapshot)
			cachedSession, cached, cachedDisk := start(cacheRoot)

			if !reflect.DeepEqual(cached, uncached) {
				t.Fatalf("cached startup differs from uncached:\ncached   = %#v\nuncached = %#v", cached, uncached)
			}
			if !maps.Equal(cachedDisk, uncachedDisk) {
				for p, b := range uncachedDisk {
					if cachedDisk[p] != b {
						t.Errorf("%s differs:\ncached   = %q\nuncached = %q", p, cachedDisk[p], b)
					}
				}
				for p := range cachedDisk {
					if _, ok := uncachedDisk[p]; !ok {
						t.Errorf("%s exists only after the cached startup", p)
					}
				}
				t.FailNow()
			}
			for _, r := range uncached {
				if bad := filepath.Base(r.Dir); (bad == "bad" || bad == "obad") && r.OK {
					t.Fatalf("codegen-error dir %s reported OK", r.Dir)
				}
			}
			if row.readOnlyAfter != "" {
				target := filepath.Join(root, filepath.FromSlash(row.readOnlyAfter))
				var r *cycleResult
				for i := range cached {
					if cached[i].Dir == filepath.Dir(target) {
						r = &cached[i]
					}
				}
				if r == nil || !slices.Contains(r.Written, target) || r.OK || r.Err == nil {
					t.Fatalf("result for %s = %+v, want a failed result whose Written includes %s", filepath.Dir(target), r, target)
				}
			}
			if len(uncachedSession.warmDirs) != 0 {
				t.Fatalf("uncached warmDirs = %v, want none", uncachedSession.warmDirs)
			}
			var wantWarm []string
			for _, rel := range row.wantWarm {
				wantWarm = append(wantWarm, filepath.Join(root, filepath.FromSlash(rel)))
			}
			gotWarm := slices.Sorted(slices.Values(cachedSession.warmDirs))
			if !slices.Equal(gotWarm, wantWarm) {
				t.Fatalf("warmDirs = %v, want %v", gotWarm, wantWarm)
			}
		})
	}
}

// tempNameRE matches writeFileAtomic's temp file names.
var tempNameRE = regexp.MustCompile(`\.gsx-w-[0-9]+`)

// readTreeT returns every regular file under root, keyed by its path.
func readTreeT(t *testing.T, root string) map[string]string {
	t.Helper()
	tree := map[string]string{}
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := os.ReadFile(path)
		tree[path] = string(b)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return tree
}

// writeTreeT replaces root's contents with tree.
func writeTreeT(t *testing.T, root string, tree map[string]string) {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if err := os.RemoveAll(filepath.Join(root, e.Name())); err != nil {
			t.Fatal(err)
		}
	}
	for p, b := range tree {
		writeFileT(t, p, b)
	}
}

// TestWatchSession_WarmupThenSharedEdit pins the background warm-up: a startup
// restored entirely from the cache leaves the Module without an import graph,
// so an edit cycle waits for the warm-up and then regenerates every importer
// of an edited shared component. Serial: it sets the warm-up hooks.
func TestWatchSession_WarmupThenSharedEdit(t *testing.T) {
	root := t.TempDir()
	writeMod(t, root)
	card := filepath.Join(root, "comp", "card.gsx")
	writeFileT(t, card, "package comp\n\ncomponent Card(title string) {\n\t<div class=\"card\">{title}</div>\n}\n")
	for _, pkg := range []string{"views", "admin"} {
		writeFileT(t, filepath.Join(root, pkg, pkg+".gsx"), "package "+pkg+"\n\nimport \"example.com/m/comp\"\n\ncomponent Page() {\n\t<comp.Card title=\""+pkg+"\"/>\n}\n")
	}
	t.Setenv("GSXCACHE", t.TempDir())
	cfg := fixedModuleConfig(moduleGenerateConfig{useCache: true})
	if res, _, err := generateCachedWithReport([]string{root}, cfg); err != nil || len(res.Errs) > 0 {
		t.Fatalf("warming generate: %v %v", err, res.Errs)
	}

	s, _, err := startWatchSessionForTest(watchConfig{paths: []string{root}, moduleConfig: cfg})
	if err != nil {
		t.Fatalf("startWatchSessionForTest: %v", err)
	}
	if len(s.warmDirs) != 3 {
		t.Fatalf("warmDirs = %v, want every dir restored", s.warmDirs)
	}
	entered, release, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
	warmupHook = func() { close(entered); <-release }
	warmupDoneHook = func() { close(finished) }
	t.Cleanup(func() { warmupHook, warmupDoneHook = nil, nil })
	s.startWarmup()
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("warm-up did not start")
	}

	writeFileT(t, card, "package comp\n\ncomponent Card(title string) {\n\t<div class=\"card edited\">{title}</div>\n}\n")
	dirty := newWatchDirtySet()
	dirty.dirs[filepath.Join(root, "comp")] = true
	type outcome struct {
		results []cycleResult
		err     error
	}
	cycled := make(chan outcome, 1)
	go func() {
		// As the loops do: wait for the warm-up, then regenerate.
		dirty.absorbWarmup(s.awaitWarmup())
		results, _, err := dirty.regenerate(s.regenPending)
		cycled <- outcome{results, err}
	}()
	select {
	case <-cycled:
		t.Fatal("the edit cycle ran before the warm-up finished")
	case <-time.After(200 * time.Millisecond):
	}
	close(release)
	var got outcome
	select {
	case got = <-cycled:
	case <-time.After(60 * time.Second):
		t.Fatal("the edit cycle did not run after the warm-up")
	}
	<-finished
	if got.err != nil {
		t.Fatalf("edit cycle: %v", got.err)
	}
	var dirs []string
	for _, r := range got.results {
		if !r.OK {
			t.Errorf("%s: err=%v diags=%v", r.Dir, r.Err, r.Diags)
		}
		dirs = append(dirs, r.Dir)
	}
	slices.Sort(dirs)
	want := []string{filepath.Join(root, "admin"), filepath.Join(root, "comp"), filepath.Join(root, "views")}
	if !slices.Equal(dirs, want) {
		t.Fatalf("edit cycle regenerated %v, want %v", dirs, want)
	}
	if s.awaitWarmup() != nil || s.warmDirs != nil {
		t.Fatal("warm-up results not consumed once")
	}
}
