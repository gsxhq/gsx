package codegen

import (
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gsxhq/gsx/internal/attrclass"
	"github.com/gsxhq/gsx/internal/diag"
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

// anyErrorDiagCodegen reports whether diags contains an error-severity
// diagnostic that is NOT a clean "imported and not used" error.
//
// This is deliberately NOT a blind mirror of gen's anyErrorDiag (severity ==
// Error, full stop): an unused import is ITSELF surfaced as a go/types
// "<path>" imported and not used error (verified empirically — see
// module_importer.go's analyze loop, which adds every type error to the bag
// as diag.Error with no special-casing for this message). detectUnusedImports
// (results.go) treats that EXACT class of error as the safe, expected case —
// it returns the populated map when EVERY type error matches "imported and
// not used"; it returns nil (removes nothing) only when some OTHER error is
// present. The old type-check-based analyzeUnusedImports (pre-Task-1, see
// commit 56c4787) read pr.UnusedImports directly with no Diags gate at all,
// confirming this: the "unused import" diagnostic was never meant to block
// removal. So the oracle's documented divergence is specifically "some
// unrelated (non-unused-import) error exists, ⇒ oracle removes nothing" —
// gating on ANY error diagnostic (including the unused-import ones the two
// detectors are supposed to agree on) would skip the interp/allunused cases
// this test exists to exercise. Matching on the message substring mirrors
// detectUnusedImports' own criterion exactly (results.go:120).
func anyErrorDiagCodegen(diags []diag.Diagnostic) bool {
	for _, d := range diags {
		if d.Severity == diag.Error && !strings.Contains(d.Message, "imported and not used") {
			return true
		}
	}
	return false
}

// impKey is the (Name,Path) identity of one UnusedImport, order-independent.
type impKey struct{ name, path string }

// keyedByAbsPath re-keys a per-.gsx-path UnusedImport map by absolute path (so
// syn's keys, already abs since openTestModule's dir is a t.TempDir(), compare
// equal to the oracle's keys, which come from the gsx fset's recorded
// filename — abs in practice here too, but normalized defensively since the
// brief calls out //line-mapped paths as the general case), collapsing each
// file's imports into a set of impKey.
func keyedByAbsPath(t *testing.T, m map[string][]UnusedImport) map[string]map[impKey]bool {
	t.Helper()
	out := map[string]map[impKey]bool{}
	for path, imps := range m {
		abs, err := filepath.Abs(path)
		if err != nil {
			t.Fatalf("filepath.Abs(%q): %v", path, err)
		}
		set := out[abs]
		if set == nil {
			set = map[impKey]bool{}
			out[abs] = set
		}
		for _, u := range imps {
			set[impKey{u.Name, u.Path}] = true
		}
	}
	return out
}

// assertSameRemovalSet asserts syn and oracle report the identical set of
// (Name,Path) unused imports per file, order-independent. dir is included in
// failure messages only.
func assertSameRemovalSet(t *testing.T, dir string, syn, oracle map[string][]UnusedImport) {
	t.Helper()
	synSet := keyedByAbsPath(t, syn)
	oracleSet := keyedByAbsPath(t, oracle)
	allFiles := map[string]bool{}
	for f := range synSet {
		allFiles[f] = true
	}
	for f := range oracleSet {
		allFiles[f] = true
	}
	for f := range allFiles {
		a, b := synSet[f], oracleSet[f]
		if len(a) != len(b) || !supersetOf(a, b) {
			t.Errorf("dir %s, file %s: syntactic=%v oracle=%v (removal sets differ)", dir, f, a, b)
			continue
		}
	}
}

// supersetOf reports whether every key in b is present in a (used alongside a
// length check in assertSameRemovalSet to establish set equality).
func supersetOf(a, b map[impKey]bool) bool {
	for k := range b {
		if !a[k] {
			return false
		}
	}
	return true
}

// TestSyntacticMatchesTypecheckOracle proves the syntactic detector
// (Module.UnusedImports) agrees with the type-check detector (Module.Package's
// pr.UnusedImports, the pre-existing oracle) on packages that type-check
// cleanly (no unrelated errors). Each case is its own module (openTestModule),
// so per-case files never collide.
func TestSyntacticMatchesTypecheckOracle(t *testing.T) {
	cases := map[string]map[string]string{
		// strings is referenced via a plain interpolation call; bytes is never
		// referenced and must be reported unused by both detectors.
		"interp": {
			"a.gsx": "package testmod\n\nimport (\n\t\"strings\"\n\t\"bytes\"\n)\n\ncomponent A() {\n\t<p>{strings.ToUpper(\"x\")}</p>\n}\n",
		},
		// strings is referenced only from inside an expr attribute (name={expr}),
		// not a text interpolation — a different skeleton context.
		"attr": {
			"b.gsx": "package testmod\n\nimport \"strings\"\n\ncomponent B() {\n\t<p id={strings.ToLower(\"X\")}>hi</p>\n}\n",
		},
		// strings is referenced only from inside a pipe stage's ARGS (a raw string
		// spliced verbatim into the lowered call, per lowerPipe) — proving the
		// skeleton captures identifier usage buried in pipeline argument text, not
		// just the stage name/seed. truncate(s string, n int) is the std filter.
		"pipeline": {
			"f.gsx": "package testmod\n\nimport \"strings\"\n\ncomponent F(s string) {\n\t<p>{ s |> truncate(strings.Count(s, \"a\")) }</p>\n}\n",
		},
		// two imports, neither referenced anywhere: both detectors must remove both.
		"allunused": {
			"d.gsx": "package testmod\n\nimport (\n\t\"strings\"\n\t\"bytes\"\n)\n\ncomponent D() {\n\t<p>hi</p>\n}\n",
		},
		// an aliased import (import sx "strings") used via its alias, not the
		// package's real name.
		"aliased": {
			"e.gsx": "package testmod\n\nimport sx \"strings\"\n\ncomponent E() {\n\t<p>{sx.ToUpper(\"x\")}</p>\n}\n",
		},
		// tag-position import usage: main.gsx imports a sibling gsx package (foo)
		// and uses it only as a component tag (<foo.Box/>), never as a plain Go
		// selector expression — proving tag usage counts as a reference and the
		// import must be KEPT (an all-context-blind base-name scan would still see
		// "foo" referenced here since the tag identifier IS "foo", but this pins
		// the cross-package-component case explicitly, matching the sibling
		// module_test.go/depfacts_test.go pattern for real cross-package tags).
		"tag": {
			"foo/box.gsx": "package foo\n\ncomponent Box() {\n\t<div>box</div>\n}\n",
			"main.gsx":    "package testmod\n\nimport \"testmod/foo\"\n\ncomponent Main() {\n\t<foo.Box/>\n}\n",
		},
	}
	for name, files := range cases {
		t.Run(name, func(t *testing.T) {
			dir, m := openTestModule(t, files)
			syn, err := m.UnusedImports(dir)
			if err != nil {
				t.Fatal(err)
			}
			pr, err := m.Package(dir)
			if err != nil {
				t.Fatal(err)
			}
			if anyErrorDiagCodegen(pr.Diags) {
				t.Skipf("package has an unrelated type error; oracle removes nothing by design (documented divergence); diags=%+v", pr.Diags)
			}
			assertSameRemovalSet(t, dir, syn, pr.UnusedImports)
		})
	}
}
