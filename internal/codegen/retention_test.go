package codegen

import (
	goast "go/ast"
	"path/filepath"
	"testing"

	gsxast "github.com/gsxhq/gsx/ast"
)

// TestRetentionPopulated: the package result carries Fset/Info and an ExprMap
// whose entry for the `{ title }` interp is the skeleton ident `title`.
func TestRetentionPopulated(t *testing.T) {
	dir := t.TempDir()
	repoRoot, _ := filepath.Abs("../..")
	writeFile(t, dir, "go.mod",
		"module example.com/x\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	writeFile(t, dir, "card.gsx",
		"package x\n\ncomponent Card(title string) {\n\t<div>{ title }</div>\n}\n")

	out, err := GeneratePackagesWithFilters(dir, []string{dir}, nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	pr := out[dir]
	if pr == nil {
		t.Fatalf("no result for %s", dir)
	}
	if pr.Fset == nil || pr.GSXFset == nil || pr.Info == nil {
		t.Fatalf("retention not populated: Fset=%v GSXFset=%v Info=%v", pr.Fset, pr.GSXFset, pr.Info)
	}
	if len(pr.GSXFiles) == 0 {
		t.Fatalf("GSXFiles empty")
	}
	// Find the Interp node and check its skeleton expr is ident "title".
	var interp *gsxast.Interp
	for _, f := range pr.GSXFiles {
		gsxast.Inspect(f, func(n gsxast.Node) bool {
			if in, ok := n.(*gsxast.Interp); ok {
				interp = in
				return false
			}
			return true
		})
	}
	if interp == nil {
		t.Fatal("no Interp in GSXFiles")
	}
	se := pr.ExprMap[interp]
	if se == nil {
		t.Fatalf("ExprMap has no entry for the interp node")
	}
	id, ok := se.(*goast.Ident)
	if !ok || id.Name != "title" {
		t.Fatalf("skeleton expr = %#v, want ident `title`", se)
	}
}
