package gsxfmt

import (
	"strings"
	"testing"
)

func addFmt(t *testing.T, src string, add ...ImportRef) string {
	t.Helper()
	out, err := FormatWith("x.gsx", []byte(src), FormatOptions{Width: 80, Reorder: true, Add: add})
	if err != nil {
		t.Fatalf("FormatWith: %v", err)
	}
	return string(out)
}

// TestAddImportToFileWithNoImports: the motivating case — a .gsx with no import
// block at all. The import must land BEFORE the first Go declaration.
func TestAddImportToFileWithNoImports(t *testing.T) {
	src := "package x\n\nvar hello = \"hi\"\n\ncomponent C() {\n\t<p>{ fmt.Sprint(hello) }</p>\n}\n"
	got := addFmt(t, src, ImportRef{Path: "fmt"})
	if !strings.Contains(got, "import \"fmt\"") {
		t.Fatalf("import not added:\n%s", got)
	}
	if strings.Index(got, "import") > strings.Index(got, "var hello") {
		t.Fatalf("import must precede the var decl:\n%s", got)
	}
}

// TestAddImportIntoExistingBlock: inserts and the reorder pass groups it.
func TestAddImportIntoExistingBlock(t *testing.T) {
	src := "package x\n\nimport \"strings\"\n\ncomponent C() {\n\t<p>{ strings.ToUpper(fmt.Sprint(1)) }</p>\n}\n"
	got := addFmt(t, src, ImportRef{Path: "fmt"})
	if !strings.Contains(got, "\"fmt\"") || !strings.Contains(got, "\"strings\"") {
		t.Fatalf("want both imports:\n%s", got)
	}
	if n := strings.Count(got, "import"); n != 1 {
		t.Fatalf("want one merged import block, got %d:\n%s", n, got)
	}
}

// TestAddThirdPartyOpensItsOwnGroup: astutil puts it in-group; reorderImports
// must then split std from third-party.
func TestAddThirdPartyOpensItsOwnGroup(t *testing.T) {
	src := "package x\n\nimport \"fmt\"\n\ncomponent C() {\n\t<p>{ fmt.Sprint(gsx.Attr{}) }</p>\n}\n"
	got := addFmt(t, src, ImportRef{Path: "github.com/gsxhq/gsx"})
	fmtAt := strings.Index(got, "\"fmt\"")
	gsxAt := strings.Index(got, "\"github.com/gsxhq/gsx\"")
	if fmtAt < 0 || gsxAt < 0 || fmtAt > gsxAt {
		t.Fatalf("std must precede third-party:\n%s", got)
	}
	if !strings.Contains(got[fmtAt:gsxAt], "\n\n") {
		t.Fatalf("want a blank line between std and third-party groups:\n%s", got)
	}
}

// TestAddDuplicateIsNoOp: adding an import already present changes nothing.
func TestAddDuplicateIsNoOp(t *testing.T) {
	src := "package x\n\nimport \"fmt\"\n\ncomponent C() {\n\t<p>{ fmt.Sprint(1) }</p>\n}\n"
	want := addFmt(t, src)                        // no adds
	got := addFmt(t, src, ImportRef{Path: "fmt"}) // add an existing one
	if got != want {
		t.Fatalf("duplicate add changed the file:\n%s\n---\n%s", got, want)
	}
}

// TestAddAliasedImport.
func TestAddAliasedImport(t *testing.T) {
	src := "package x\n\ncomponent C() {\n\t<p>{ sx.ToUpper(\"x\") }</p>\n}\n"
	got := addFmt(t, src, ImportRef{Name: "sx", Path: "strings"})
	if !strings.Contains(got, "sx \"strings\"") {
		t.Fatalf("aliased import not added:\n%s", got)
	}
}

// TestAddWhenNoGoChunkExists: every decl is a component / element literal, so a
// GoChunk must be synthesized to hold the import.
func TestAddWhenNoGoChunkExists(t *testing.T) {
	src := "package x\n\ncomponent C() {\n\t<p>{ fmt.Sprint(1) }</p>\n}\n"
	got := addFmt(t, src, ImportRef{Path: "fmt"})
	if !strings.Contains(got, "import \"fmt\"") {
		t.Fatalf("import not added to a chunk-less file:\n%s", got)
	}
	if strings.Index(got, "import") > strings.Index(got, "component C") {
		t.Fatalf("import must precede the component:\n%s", got)
	}
}

// TestAddPreservesGoBuild: the synthetic-package-clause hazard. go/printer hoists
// //go:build above the clause; stripping by line index would shear it.
func TestAddPreservesGoBuild(t *testing.T) {
	src := "package x\n\n//go:build linux\nimport \"strings\"\n\ncomponent C() {\n\t<p>{ strings.ToUpper(fmt.Sprint(1)) }</p>\n}\n"
	got := addFmt(t, src, ImportRef{Path: "fmt"})
	if !strings.Contains(got, "//go:build linux") {
		t.Fatalf("build tag lost:\n%s", got)
	}
	for _, bad := range []string{"_gsxp", "_gsxfmt", "package _"} {
		if strings.Contains(got, bad) {
			t.Fatalf("leaked %q:\n%s", bad, got)
		}
	}
}

// TestAddAndRemoveInOneEdit: adds and removes compose.
func TestAddAndRemoveInOneEdit(t *testing.T) {
	src := "package x\n\nimport \"bytes\"\n\ncomponent C() {\n\t<p>{ fmt.Sprint(1) }</p>\n}\n"
	out, err := FormatWith("x.gsx", []byte(src), FormatOptions{
		Width: 80, Reorder: true,
		Unused: []ImportRef{{Path: "bytes"}},
		Add:    []ImportRef{{Path: "fmt"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := string(out)
	if strings.Contains(got, "\"bytes\"") {
		t.Fatalf("unused import not removed:\n%s", got)
	}
	if !strings.Contains(got, "\"fmt\"") {
		t.Fatalf("missing import not added:\n%s", got)
	}
}

// TestAddIsIdempotent.
func TestAddIsIdempotent(t *testing.T) {
	src := "package x\n\nvar hello = \"hi\"\n\ncomponent C() {\n\t<p>{ fmt.Sprint(hello) }</p>\n}\n"
	once := addFmt(t, src, ImportRef{Path: "fmt"})
	twice := addFmt(t, once, ImportRef{Path: "fmt"})
	if once != twice {
		t.Fatalf("not idempotent:\n%s\n---\n%s", once, twice)
	}
}

// TestNoAddIsUnchanged: an empty Add must not perturb output.
func TestNoAddIsUnchanged(t *testing.T) {
	src := "package x\n\nimport \"fmt\"\n\ncomponent C() {\n\t<p>{ fmt.Sprint(1) }</p>\n}\n"
	want, err := FormatWith("x.gsx", []byte(src), FormatOptions{Width: 80, Reorder: true})
	if err != nil {
		t.Fatal(err)
	}
	got := addFmt(t, src)
	if got != string(want) {
		t.Fatalf("empty Add changed output")
	}
}

// assertAddMatchesCanonical pins the invariant this file exists to protect:
// adding an import to srcWithout must produce BYTE-IDENTICAL output to plainly
// formatting srcWith — a file that already has the same import(s) written out
// by hand. This is the only crisp, non-guessable definition of "added an
// import correctly": it doesn't require knowing in advance where a blank line
// belongs, only that Add(x) and Format(x-with-import-already-there) agree.
func assertAddMatchesCanonical(t *testing.T, srcWithout, srcWith string, refs ...ImportRef) {
	t.Helper()
	want, err := FormatWith("x.gsx", []byte(srcWith), FormatOptions{Width: 80, Reorder: true})
	if err != nil {
		t.Fatalf("FormatWith(srcWith): %v", err)
	}
	got, err := FormatWith("x.gsx", []byte(srcWithout), FormatOptions{Width: 80, Reorder: true, Add: refs})
	if err != nil {
		t.Fatalf("FormatWith(srcWithout, Add): %v", err)
	}
	if string(got) != string(want) {
		t.Fatalf("Add did not match canonical form:\n got=%q\nwant=%q", got, want)
	}
}

// addInvariantShapes are the decl shapes that can follow (or, for the
// chunkless case, BE) the chunk that receives the added import. Each name
// documents which stage of the file the target chunk sits in front of.
var addInvariantShapes = []struct {
	name       string
	srcWithout string
	srcWith    string
	refs       []ImportRef
}{
	{
		// The failing case (see task description): with no import chunk at
		// all, `var hello` has nothing to force it into its own GoChunk, so it
		// merges into the SAME GoWithElements decl as `var xx = <p>…`. Adding
		// an import here must synthesize a leading GoChunk that still reads as
		// "a blank line follows" to the printer.
		name:       "chunk followed by GoWithElements",
		srcWithout: "package main\n\nvar hello = \"hi\"\n\nvar xx = <p>{ fmt.Sprintf(hello) }</p>\n",
		srcWith:    "package main\n\nimport \"fmt\"\n\nvar hello = \"hi\"\n\nvar xx = <p>{ fmt.Sprintf(hello) }</p>\n",
		refs:       []ImportRef{{Path: "fmt"}},
	},
	{
		// `func Describe` is plain Go with no gsx elements, so it forms its own
		// real GoChunk even with no import present — importTargetChunk reuses
		// that chunk rather than synthesizing one.
		name:       "chunk followed by a plain func",
		srcWithout: "package main\n\nfunc Describe() string {\n\treturn \"hi\"\n}\n",
		srcWith:    "package main\n\nimport \"fmt\"\n\nfunc Describe() string {\n\treturn \"hi\"\n}\n",
		refs:       []ImportRef{{Path: "fmt"}},
	},
	{
		// `var hello` precedes a Component (a distinct decl type from
		// GoWithElements), so it again forms its own real, pre-existing
		// GoChunk — same reuse path as the plain-func shape above.
		name:       "chunk followed by a component",
		srcWithout: "package main\n\nvar hello = \"hi\"\n\ncomponent C() {\n\t<p>{ fmt.Sprint(hello) }</p>\n}\n",
		srcWith:    "package main\n\nimport \"fmt\"\n\nvar hello = \"hi\"\n\ncomponent C() {\n\t<p>{ fmt.Sprint(hello) }</p>\n}\n",
		refs:       []ImportRef{{Path: "fmt"}},
	},
	{
		// No GoChunk exists anywhere in the file: the only decl is a
		// Component. importTargetChunk must synthesize one from scratch.
		name:       "file whose only decl is a component",
		srcWithout: "package x\n\ncomponent C() {\n\t<p>{ fmt.Sprint(1) }</p>\n}\n",
		srcWith:    "package x\n\nimport \"fmt\"\n\ncomponent C() {\n\t<p>{ fmt.Sprint(1) }</p>\n}\n",
		refs:       []ImportRef{{Path: "fmt"}},
	},
	{
		// An import block already exists; Add merges into it and Reorder
		// groups/sorts the result.
		name:       "file with an existing import block",
		srcWithout: "package x\n\nimport \"strings\"\n\ncomponent C() {\n\t<p>{ strings.ToUpper(fmt.Sprint(1)) }</p>\n}\n",
		srcWith:    "package x\n\nimport (\n\t\"fmt\"\n\t\"strings\"\n)\n\ncomponent C() {\n\t<p>{ strings.ToUpper(fmt.Sprint(1)) }</p>\n}\n",
		refs:       []ImportRef{{Path: "fmt"}},
	},
}

// TestAddMatchesCanonicalForm applies the invariant to every shape in
// addInvariantShapes. The first entry is the exact regression this fix
// addresses; the rest confirm the fix didn't have to special-case that shape
// and the previously-working shapes stayed correct.
func TestAddMatchesCanonicalForm(t *testing.T) {
	for _, tc := range addInvariantShapes {
		t.Run(tc.name, func(t *testing.T) {
			assertAddMatchesCanonical(t, tc.srcWithout, tc.srcWith, tc.refs...)
		})
	}
}

// TestAddResultIsFixedPoint: for every shape, the output of an Add must be a
// fixed point of a plain FormatWith (no Add) — i.e. it must never need a
// second `gsx fmt` pass to reach its final form. This is exactly the property
// that was violated by the bug: the buggy output printed a "no blank line"
// GoChunk, which plain re-formatting left untouched forever.
func TestAddResultIsFixedPoint(t *testing.T) {
	for _, tc := range addInvariantShapes {
		t.Run(tc.name, func(t *testing.T) {
			once := addFmt(t, tc.srcWithout, tc.refs...)
			twice, err := FormatWith("x.gsx", []byte(once), FormatOptions{Width: 80, Reorder: true})
			if err != nil {
				t.Fatalf("FormatWith(once): %v", err)
			}
			if once != string(twice) {
				t.Fatalf("Add's output is not a fixed point:\nonce =%q\ntwice=%q", once, twice)
			}
		})
	}
}
