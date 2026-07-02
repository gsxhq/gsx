package codegen

import (
	"slices"
	"testing"
)

func TestGsxHoistedImportPaths(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a.gsx", "package p\n\nimport (\n\t\"example.com/app/ui\"\n\tw \"example.com/app/widgets\"\n)\n\ncomponent A() {\n\t<ui.X/>\n\t<w.Y/>\n}\n")
	writeFile(t, dir, "broken.gsx", "package p\n\ncomponent B( {\n")
	got := GsxHoistedImportPaths(dir)
	want := []string{"example.com/app/ui", "example.com/app/widgets"}
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Errorf("GsxHoistedImportPaths = %v; want %v", got, want)
	}
}

func TestGsxHoistedImportPathsNoImports(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a.gsx", "package p\n\ncomponent A() {\n\t<p/>\n}\n")
	got := GsxHoistedImportPaths(dir)
	if len(got) != 0 {
		t.Errorf("GsxHoistedImportPaths = %v; want empty", got)
	}
}
