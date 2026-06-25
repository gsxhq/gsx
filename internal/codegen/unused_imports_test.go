package codegen

import (
	"path/filepath"
	"testing"
)

// writeFile is defined in the codegen test package (see navindex_test.go).

// TestUnusedImportsDetected: a .gsx that imports "strings" and "os" but uses
// neither lists both in UnusedImports; a used import is absent.
func TestUnusedImportsDetected(t *testing.T) {
	dir := t.TempDir()
	repoRoot, _ := filepath.Abs("../..")
	writeFile(t, dir, "go.mod",
		"module example.com/u\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	// imports strings (unused), os (unused), fmt (USED in the interp).
	writeFile(t, dir, "card.gsx",
		"package u\n\nimport (\n\t\"strings\"\n\t\"os\"\n\t\"fmt\"\n)\n\ncomponent Card(name string) {\n\t<p>{ fmt.Sprintf(\"%s\", name) }</p>\n}\n")

	out, err := GeneratePackagesWithFilters(dir, []string{dir}, nil, nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	pr := out[dir]
	if pr == nil {
		t.Fatalf("no result for %s", dir)
	}
	gsxPath := filepath.Join(dir, "card.gsx")
	got := map[string]bool{}
	for _, u := range pr.UnusedImports[gsxPath] {
		got[u.Path] = true
	}
	if !got["strings"] || !got["os"] {
		t.Errorf("want strings+os unused, got %v (all: %+v)", got, pr.UnusedImports)
	}
	if got["fmt"] {
		t.Errorf("fmt is used but was reported unused")
	}
}

// TestUnusedImportsGateOnBrokenImport: an import that is REFERENCED but cannot
// be resolved (typo'd / not fetched) produces a "could not import" error on the
// import line, NOT "imported and not used". It must never be reported unused.
func TestUnusedImportsGateOnBrokenImport(t *testing.T) {
	dir := t.TempDir()
	repoRoot, _ := filepath.Abs("../..")
	writeFile(t, dir, "go.mod",
		"module example.com/u\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	writeFile(t, dir, "card.gsx",
		"package u\n\nimport \"example.com/u/nope/stringz\"\n\ncomponent Card() {\n\t<p>{ stringz.X() }</p>\n}\n")

	out, err := GeneratePackagesWithFilters(dir, []string{dir}, nil, nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	pr := out[dir]
	if pr == nil {
		t.Fatalf("no result")
	}
	if n := len(pr.UnusedImports); n != 0 {
		t.Errorf("a referenced-but-unresolvable import must not be removable, got %+v", pr.UnusedImports)
	}
}

// TestUnusedImportsGateOnOtherError: when the package has a NON-import type
// error, UnusedImports stays empty even though an unused import is present —
// removing under uncertainty is unsafe.
func TestUnusedImportsGateOnOtherError(t *testing.T) {
	dir := t.TempDir()
	repoRoot, _ := filepath.Abs("../..")
	writeFile(t, dir, "go.mod",
		"module example.com/u\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	// "strings" is unused AND there is an undefined symbol (a non-import error).
	writeFile(t, dir, "card.gsx",
		"package u\n\nimport \"strings\"\n\ncomponent Card() {\n\t<p>{ Nope() }</p>\n}\n")

	out, err := GeneratePackagesWithFilters(dir, []string{dir}, nil, nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	pr := out[dir]
	if pr == nil {
		t.Fatalf("no result")
	}
	if n := len(pr.UnusedImports); n != 0 {
		t.Errorf("expected NO removals under an unrelated error, got %+v", pr.UnusedImports)
	}
}
