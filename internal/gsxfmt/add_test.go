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
