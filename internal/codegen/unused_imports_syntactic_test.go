package codegen

import (
	"go/parser"
	"go/token"
	"path/filepath"
	"testing"

	"github.com/gsxhq/gsx/internal/attrclass"
)

// openTestModule writes a minimal go.mod plus the given .gsx files (keyed by
// filename) into a fresh temp module root, opens a Module over it, and
// returns the package dir (== the module root, since files are written
// directly into it) and the Module. Shared by the syntactic-unused-import
// tests (Tasks 2, 3, 5).
//
// The go.mod carries a require+replace for github.com/gsxhq/gsx (mirroring
// unused_imports_test.go's pattern) pointed at this checkout: even a
// buildPackageSkeletons-only test exercises cachedFilterTable, which always
// resolves the built-in "github.com/gsxhq/gsx/std" filter package (dedupFilterPkgs
// defaults to it when Options.FilterPkgs is empty) — without the replace, that
// packages.Load fails outright since "github.com/gsxhq/gsx" is not a real
// dependency of the ephemeral "testmod" module. This filter-table load is
// counted separately (m.filterTableLoads()), not by m.externalLoads(), so it
// does not contradict the importer-free claim buildPackageSkeletons makes.
// The go directive must match the real module's (currently 1.26.1, see
// ci.yml's GO_VERSION) — "go 1.26" alone makes the replaced module's higher
// go directive trip "go: updates to go.mod needed; to update it: go mod tidy"
// during the filter-table's packages.Load.
func openTestModule(t *testing.T, files map[string]string) (string, *Module) {
	t.Helper()
	dir := t.TempDir()
	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, dir, "go.mod",
		"module testmod\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	for name, src := range files {
		writeFile(t, dir, name, src)
	}
	m, err := Open(Options{ModuleRoot: dir, ModulePath: "testmod", Classifier: attrclass.Builtin()})
	if err != nil {
		t.Fatal(err)
	}
	return dir, m
}

func TestSkeletonUsedNames(t *testing.T) {
	const src = `package p
import "strings"
func f() { _ = strings.TrimSpace("x"); _ = a.b.c }
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "s.go", src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	used := skeletonUsedNames(f)
	if !used["strings"] {
		t.Errorf("want strings used")
	}
	if !used["a"] { // inner selector a.b of a.b.c
		t.Errorf("want a used")
	}
}

func TestImportBaseName(t *testing.T) {
	for path, want := range map[string]string{
		"strings":            "strings",
		"gopkg.in/yaml.v3":   "yaml.v3", // base is NOT the package name → forces candidate resolution
		"github.com/x/go-fo": "go-fo",
	} {
		if got := importBaseName(path); got != want {
			t.Errorf("importBaseName(%q)=%q want %q", path, got, want)
		}
	}
}

func TestClassifyUnusedImports(t *testing.T) {
	fset := token.NewFileSet()
	used := map[string]bool{"strings": true, "sx": true}
	imps := []importSpec{
		{name: "", path: "strings"},        // default, base used → kept
		{name: "", path: "bytes"},          // default, base unused → candidate
		{name: "sx", path: "text/scanner"}, // aliased, alias used → kept
		{name: "al", path: "os"},           // aliased, alias unused → unused
		{name: "_", path: "embed"},         // blank → never removed
		{name: ".", path: "math"},          // dot → never removed
	}
	unused, candidates := classifyUnusedImports(used, imps, nil, fset)
	if len(unused) != 1 || unused[0].Path != "os" || unused[0].Name != "al" {
		t.Errorf("unused=%+v, want only {al os}", unused)
	}
	if len(candidates) != 1 || candidates[0].path != "bytes" {
		t.Errorf("candidates=%+v, want only bytes", candidates)
	}
}

func TestClassifyUnusedImportsSkipsSunk(t *testing.T) {
	fset := token.NewFileSet()
	tf := fset.AddFile("page.gsx", -1, 1000)
	pos := tf.Pos(0) // offset 0 → line 1

	// A default import whose base name is not referenced: without a sunk entry
	// it would be a removal candidate.
	imps := []importSpec{
		{name: "", path: "github.com/foo/sunk", pos: pos},
	}

	// Contrast/sanity: with no sunk map it IS a candidate, proving the sunk map
	// (not something else) is what excludes it below.
	unused, candidates := classifyUnusedImports(map[string]bool{}, imps, nil, fset)
	if len(unused) != 0 || len(candidates) != 1 {
		t.Fatalf("without sunk: unused=%+v candidates=%+v, want 0 unused / 1 candidate", unused, candidates)
	}

	// With a matching sunk entry the import is used in the .gsx source, so it must
	// be excluded from BOTH unused and candidates.
	sunk := map[sunkImportKey]bool{{line: 1, path: "github.com/foo/sunk"}: true}
	unused, candidates = classifyUnusedImports(map[string]bool{}, imps, sunk, fset)
	if len(unused) != 0 || len(candidates) != 0 {
		t.Errorf("with sunk: unused=%+v candidates=%+v, want both empty", unused, candidates)
	}
}

// TestUnusedImportsSyntactic exercises Module.UnusedImports end to end: strings
// is referenced (via strings.ToUpper) and must be kept, bytes is never
// referenced and must be reported unused.
func TestUnusedImportsSyntactic(t *testing.T) {
	dir, m := openTestModule(t, map[string]string{
		"page.gsx": "package testmod\n\nimport (\n\t\"strings\"\n\t\"bytes\"\n)\n\ncomponent Page() {\n\t<div>{strings.ToUpper(\"x\")}</div>\n}\n",
	})
	got, err := m.UnusedImports(dir)
	if err != nil {
		t.Fatal(err)
	}
	unused := got[filepath.Join(dir, "page.gsx")]
	if len(unused) != 1 || unused[0].Path != "bytes" {
		t.Errorf("unused=%+v, want [bytes]; strings is used and must be kept", unused)
	}
}

// TestUnusedImportsDefaultNameMismatchKept covers the real-name resolution
// path: renamedpkg/lib.go declares "package renamed", so the import path's
// base ("renamedpkg") is never referenced, but the real package name
// ("renamed") is. A base-only scan would wrongly flag testmod/renamedpkg as
// unused; resolvePackageNames must resolve it to "renamed" so it is kept.
func TestUnusedImportsDefaultNameMismatchKept(t *testing.T) {
	dir, m := openTestModule(t, map[string]string{
		"renamedpkg/lib.go": "package renamed\n\nfunc Hello() string { return \"hi\" }\n",
		"page.gsx":          "package testmod\n\nimport \"testmod/renamedpkg\"\n\ncomponent Page() {\n\t<div>{renamed.Hello()}</div>\n}\n",
	})
	got, err := m.UnusedImports(dir)
	if err != nil {
		t.Fatal(err)
	}
	if u := got[filepath.Join(dir, "page.gsx")]; len(u) != 0 {
		t.Errorf("testmod/renamedpkg is used (renamed.Hello) and must be kept; got unused=%+v", u)
	}
}

// TestBuildPackageSkeletonsNoExternalLoad proves buildPackageSkeletons is
// importer-free: it lowers page.gsx to its skeleton AST and reports the
// hoisted "strings" import as referenced in the skeleton, all WITHOUT
// triggering a single external packages.Load (m.externalLoads() stays 0) —
// the whole point of scanning the skeleton instead of type-checking.
func TestBuildPackageSkeletonsNoExternalLoad(t *testing.T) {
	dir, m := openTestModule(t, map[string]string{
		"page.gsx": "package testmod\n\nimport \"strings\"\n\ncomponent Page() {\n\t<div>{strings.ToUpper(\"x\")}</div>\n}\n",
	})
	ps, err := m.buildPackageSkeletons(dir)
	if err != nil {
		t.Fatal(err)
	}
	fs, ok := ps.byGsx[filepath.Join(dir, "page.gsx")]
	if !ok {
		t.Fatalf("no skeleton for page.gsx; got %v", ps.byGsx)
	}
	if len(fs.imps) != 1 || fs.imps[0].path != "strings" {
		t.Errorf("imps=%+v, want [strings]", fs.imps)
	}
	used := skeletonUsedNames(fs.skel)
	if !used["strings"] {
		t.Errorf("expected strings referenced in skeleton; used=%v", used)
	}
	if n := m.externalLoads(); n != 0 {
		t.Errorf("buildPackageSkeletons did %d external loads, want 0 (importer-free)", n)
	}
}
