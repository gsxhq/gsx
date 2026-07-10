package gen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

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

	// newWatchSession opens a Module and runs startup regenDir for blogDir, fully
	// populating the import graph (including the sibling .go file).
	s, startup, err := newWatchSession(watchConfig{paths: []string{blogDir}})
	if err != nil {
		t.Fatalf("newWatchSession: %v", err)
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

	// newWatchSession runs startup regenDir for both dirs, writing their .x.go
	// files and populating the cross-package import graph.
	s, _, err := newWatchSession(watchConfig{paths: []string{compDir, viewsDir}})
	if err != nil {
		t.Fatalf("newWatchSession: %v", err)
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

	s, _, err := newWatchSession(watchConfig{paths: []string{filepath.Join(root, "views")}})
	if err != nil {
		t.Fatalf("newWatchSession: %v", err)
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

// TestWatchSession_RegenError proves that a broken .gsx yields OK=false with
// diagnostics.
func TestWatchSession_RegenError(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeMod(t, root)
	gsxPath := filepath.Join(root, "views", "page.gsx")
	writeFileT(t, gsxPath, "package views\n\ncomponent Page() {\n\t<h1>{undefinedSym}</h1>\n}\n")

	s, _, err := newWatchSession(watchConfig{paths: []string{filepath.Join(root, "views")}})
	if err != nil {
		t.Fatalf("newWatchSession: %v", err)
	}
	r := s.regenDir(filepath.Join(root, "views"))
	if r.OK || len(r.Diags) == 0 {
		t.Fatalf("expected OK=false with diagnostics, got OK=%v diags=%v", r.OK, r.Diags)
	}
}

// TestWatchSession_BadMergerRefused proves that newWatchSession returns a clear
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

	_, _, err := newWatchSession(watchConfig{
		paths:       []string{filepath.Join(root, "views")},
		classMerger: &codegen.ClassMergerRef{PkgPath: "example.com/m/mrg", FuncName: "Merge"},
	})
	if err == nil {
		t.Fatal("want error for bad-signature merger under --watch, got nil")
	}
	if !strings.Contains(err.Error(), "func([]string) string") {
		t.Fatalf("want signature error, got: %v", err)
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

	s, startup, err := newWatchSession(watchConfig{paths: []string{viewsDir}})
	if err != nil {
		t.Fatalf("newWatchSession: %v", err)
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
	results, err := s.regenPending(map[string]bool{viewsDir: true}, false)
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

	s, startup, err := newWatchSession(watchConfig{paths: []string{viewsDir}})
	if err != nil {
		t.Fatalf("newWatchSession: %v", err)
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
	results, err := s.regenPending(map[string]bool{viewsDir: true}, false)
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

	s, startup, err := newWatchSession(watchConfig{paths: []string{viewsDir}})
	if err != nil {
		t.Fatalf("newWatchSession: %v", err)
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
