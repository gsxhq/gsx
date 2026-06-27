package gen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
