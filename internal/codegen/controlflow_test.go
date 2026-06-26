package codegen

import (
	"go/token"
	"strings"
	"testing"

	gsxast "github.com/gsxhq/gsx/ast"
	gsxparser "github.com/gsxhq/gsx/parser"
)

func TestBuildSkeletonRecordsCtrlOffsets(t *testing.T) {
	src := "package v\n\ncomponent P(props Props) {\n\t{ for _, post := range props.Posts {\n\t\t<li>{post.Title}</li>\n\t} }\n}\n"
	fset := token.NewFileSet()
	file, errs := gsxparser.ParseFileWithClassifier(fset, "p.gsx", []byte(src), 0, nil)
	if len(errs) > 0 {
		t.Fatalf("parse: %v", errs)
	}
	// minimal props/byo for buildSkeleton; an empty table/maps is fine for a no-import component.
	table, _ := loadFilterTable(t.TempDir())
	pf, np, byo, err := componentPropFieldsFor(t.TempDir(), map[string]*gsxast.File{"p.gsx": file})
	if err != nil {
		t.Fatalf("propFields: %v", err)
	}
	skel, _, _, ctrlOff, err := buildSkeleton(file, table, pf, np, byo, nil, fset)
	if err != nil {
		t.Fatalf("buildSkeleton: %v", err)
	}
	// Find the ForMarkup node and assert its recorded offset lands on the clause text.
	var forM *gsxast.ForMarkup
	gsxast.Inspect(file, func(n gsxast.Node) bool {
		if f, ok := n.(*gsxast.ForMarkup); ok {
			forM = f
		}
		return true
	})
	off, ok := ctrlOff[forM]
	if !ok {
		t.Fatalf("ctrlOff has no entry for the ForMarkup")
	}
	if got := skel[off : off+len(forM.Clause)]; got != forM.Clause {
		t.Errorf("skeleton at ctrlOff = %q, want clause %q (byte-faithful)", got, forM.Clause)
	}
	// A compensated //line precedes the `for` in the skeleton.
	forIdx := strings.Index(skel, "for "+forM.Clause)
	if forIdx < 0 {
		t.Fatalf("skeleton missing `for <clause>`")
	}
	pre := skel[:forIdx]
	if li := strings.LastIndex(pre, "//line "); li < 0 || strings.Contains(pre[li:], "\n}") {
		t.Errorf("expected a //line directive immediately before the for clause")
	}
}
