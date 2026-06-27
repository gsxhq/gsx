package codegen

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestCrossIndex: a component Card declared in card.gsx, called from main.go and
// used as <Card/> in page.gsx, is indexed with its .gsx Decl and both refs.
func TestCrossIndex(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	repoRoot, _ := filepath.Abs("../..")
	writeFile(t, dir, "go.mod",
		"module example.com/x\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	writeFile(t, dir, "card.gsx",
		"package x\n\ncomponent Card(title string) {\n\t<div>{ title }</div>\n}\n")
	writeFile(t, dir, "page.gsx",
		"package x\n\ncomponent Page() {\n\t<main><Card title=\"hi\"/></main>\n}\n")
	writeFile(t, dir, "main.go",
		"package x\n\nvar _ = Card\n")

	out, err := GeneratePackagesWithFilters(dir, []string{dir}, nil, nil, nil, nil, nil, nil, true, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	pr := out[dir]
	if pr == nil {
		t.Fatalf("no result for %s", dir)
	}
	cr, ok := pr.CrossIndex[".Card"]
	if !ok {
		t.Fatalf("CrossIndex missing .Card; keys=%v", keysOfCross(pr.CrossIndex))
	}
	if !strings.HasSuffix(cr.Decl.Filename, "card.gsx") {
		t.Fatalf("Decl filename = %q, want card.gsx", cr.Decl.Filename)
	}
	var goRef, gsxRef bool
	for _, r := range cr.Refs {
		if strings.HasSuffix(r.Filename, "main.go") {
			goRef = true
		}
		if strings.HasSuffix(r.Filename, "page.gsx") {
			gsxRef = true
		}
	}
	if !goRef {
		t.Errorf("no main.go reference in Refs: %+v", cr.Refs)
	}
	if !gsxRef {
		t.Errorf("no page.gsx (<Card/>) reference in Refs: %+v", cr.Refs)
	}
}

func keysOfCross(m map[string]CrossRef) []string {
	var k []string
	for s := range m {
		k = append(k, s)
	}
	return k
}
