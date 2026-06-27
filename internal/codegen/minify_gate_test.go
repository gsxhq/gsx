package codegen

import (
	"path/filepath"
	"strings"
	"testing"
)

// styleSrc is a component with a <style> whose body the safe minifier would
// collapse ("1px  2px" → "1px 2px"); with minify OFF the double space survives.
const styleSrc = "package x\n\ncomponent Page() {\n\t<style>\n\t\t.card { margin: 1px  2px; }\n\t</style>\n}\n"

func genStyle(t *testing.T, cssMinify bool) string {
	t.Helper()
	dir := t.TempDir()
	repoRoot, _ := filepath.Abs("../..")
	writeFile(t, dir, "go.mod",
		"module example.com/x\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	writeFile(t, dir, "page.gsx", styleSrc)
	out, err := GeneratePackagesWithFilters(dir, []string{dir}, nil, nil, nil, nil, nil, nil, cssMinify, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	pr := out[dir]
	if pr == nil || len(pr.Files) == 0 {
		t.Fatal("no generated output")
	}
	var sb strings.Builder
	for _, b := range pr.Files {
		sb.Write(b)
	}
	return sb.String()
}

func TestMinifyGate_CSS(t *testing.T) {
	t.Parallel()
	on := genStyle(t, true)
	if !strings.Contains(on, "1px 2px") || strings.Contains(on, "1px  2px") {
		t.Fatalf("cssMinify=true should minify; got:\n%s", on)
	}
	off := genStyle(t, false)
	if !strings.Contains(off, "1px  2px") {
		t.Fatalf("cssMinify=false should preserve double space; got:\n%s", off)
	}
}
