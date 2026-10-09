package codegen

import (
	"path/filepath"
	"slices"
	"testing"
)

// TestGenerationOrder pins that every dir follows the dirs it imports — through
// .gsx imports, companion .go imports, and Go-only intermediaries outside the
// input — that ties keep input order, that input spellings are returned as
// given, and that an import cycle terminates.
func TestGenerationOrder(t *testing.T) {
	t.Parallel()
	tmp := tempModule(t, "gsxorder")
	comp := func(pkg, imports, body string) string {
		return "package " + pkg + "\n\n" + imports + "component C() {\n\t" + body + "\n}\n"
	}
	// Each edge kind has its own dependency, listed after its importer:
	// a -> za via a .gsx import, b -> zb via the Go-only shim, c -> zc via its
	// companion .go file. d imports a and b; x and y import each other.
	dirZA := makeSubPkg(t, tmp, "za", comp("za", "", "<p>za</p>"))
	dirZB := makeSubPkg(t, tmp, "zb", comp("zb", "", "<p>zb</p>"))
	dirZC := makeSubPkg(t, tmp, "zc", comp("zc", "", "<p>zc</p>"))
	dirA := makeSubPkg(t, tmp, "a", comp("a", "import \"gsxorder/za\"\n\n", "<za.C />"))
	writeFile(t, filepath.Join(tmp, "shim"), "shim.go", "package shim\n\nimport \"gsxorder/zb\"\n\nvar C = zb.C\n")
	dirB := makeSubPkg(t, tmp, "b", comp("b", "import \"gsxorder/shim\"\n\n", "<shim.C />"))
	dirC := makeSubPkg(t, tmp, "c", comp("c", "", "<p>c</p>"))
	writeFile(t, dirC, "dep.go", "package c\n\nimport \"gsxorder/zc\"\n\nvar _ = zc.C\n")
	dirD := makeSubPkg(t, tmp, "d", comp("d", "import (\n\t\"gsxorder/a\"\n\t\"gsxorder/b\"\n)\n\n", "<a.C /><b.C />"))
	dirX := makeSubPkg(t, tmp, "x", comp("x", "import \"gsxorder/y\"\n\n", "<y.C />"))
	dirY := makeSubPkg(t, tmp, "y", comp("y", "import \"gsxorder/x\"\n\n", "<x.C />"))

	m, err := Open(Options{ModuleRoot: tmp, ModulePath: "gsxorder"})
	if err != nil {
		t.Fatal(err)
	}
	spelledD := dirA + string(filepath.Separator) + ".." + string(filepath.Separator) + "d"
	if spelledD == dirD {
		t.Fatal("fixture: spelledD must differ from the clean dir")
	}
	got := m.GenerationOrder([]string{spelledD, dirC, dirX, dirY, dirZA, dirZB, dirZC, dirA, dirB})
	want := []string{dirZA, dirA, dirZB, dirB, spelledD, dirZC, dirC, dirY, dirX}
	if !slices.Equal(got, want) {
		t.Fatalf("GenerationOrder:\n got %v\nwant %v", got, want)
	}
}
