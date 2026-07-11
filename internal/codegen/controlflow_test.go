package codegen

import (
	"go/token"
	"os"
	"path/filepath"
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
	pf, np, ap, byo, err := componentPropFieldsFor(t.TempDir(), map[string]*gsxast.File{"p.gsx": file})
	if err != nil {
		t.Fatalf("propFields: %v", err)
	}
	skel, _, _, ctrlOff, _, _, err := buildSkeleton(file, funcTables{filters: table}, pf, np, ap, nil, nil, byo, nil, fset, nil, nil, nil, nil)
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

func TestModulePackageBuildsCtrlMap(t *testing.T) {
	root := t.TempDir()
	repoRoot, _ := filepath.Abs("../..")
	writeFile(t, root, "go.mod", "module example.com/app\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	pkgDir := filepath.Join(root, "page")
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, pkgDir, "page.gsx", "package page\n\ntype Props struct{ Posts []Post }\ntype Post struct{ Title string }\n\ncomponent P(props Props) {\n\t{ for _, post := range props.Posts {\n\t\t<li>{post.Title}</li>\n\t} }\n}\n")

	m, _ := Open(Options{ModuleRoot: root, ModulePath: "example.com/app", FilterPkgs: []string{StdImportPath}})
	pr, err := m.Package(pkgDir)
	if err != nil {
		t.Fatal(err)
	}
	var forM *gsxast.ForMarkup
	for _, gf := range pr.GSXFiles {
		gsxast.Inspect(gf, func(n gsxast.Node) bool {
			if f, ok := n.(*gsxast.ForMarkup); ok {
				forM = f
			}
			return true
		})
	}
	cr, ok := pr.CtrlMap[forM]
	if !ok || !cr.ClauseStart.IsValid() || cr.Node == nil {
		t.Fatalf("CtrlMap missing/invalid for ForMarkup: %+v ok=%v", cr, ok)
	}
	// ClauseStart maps (via //line) back to the .gsx clause.
	dp := pr.Fset.Position(cr.ClauseStart)
	if !strings.HasSuffix(dp.Filename, ".gsx") {
		t.Errorf("ClauseStart maps to %s, want a .gsx position", dp.Filename)
	}
}

// TestBuildSkeletonRecordsNavSpanOffsets verifies that every statement-position
// navigation span gets a ctrlOff entry keyed by its owning node, with the
// skeleton bytes at that offset byte-faithful to the clause text (the invariant
// the LSP's relative-offset bridge relies on): value-form if/else-if conds,
// value-form switch tag + case lists, in-tag conditional-attribute conds,
// class guard conds, and markup switch tag + case lists.
func TestBuildSkeletonRecordsNavSpanOffsets(t *testing.T) {
	src := "package v\n\ncomponent P(disabled bool, busy bool, n int) {\n\t<input\n\t\tclass={\n\t\t\t\"base\",\n\t\t\t\"guard\": busy,\n\t\t\tif disabled { \"off\" } else if busy { \"wait\" },\n\t\t\tswitch n {\n\t\t\tcase 1, n: \"one\"\n\t\t\tdefault: \"d\"\n\t\t\t}\n\t\t}\n\t\t{ if disabled {\n\t\t\tdata-x=\"1\"\n\t\t} }\n\t/>\n\t{ switch n {\n\tcase 2, n:\n\t\t<b>x</b>\n\t} }\n}\n"
	fset := token.NewFileSet()
	file, errs := gsxparser.ParseFileWithClassifier(fset, "p.gsx", []byte(src), 0, nil)
	if len(errs) > 0 {
		t.Fatalf("parse: %v", errs)
	}
	table, _ := loadFilterTable(t.TempDir())
	pf, np, ap, byo, err := componentPropFieldsFor(t.TempDir(), map[string]*gsxast.File{"p.gsx": file})
	if err != nil {
		t.Fatalf("propFields: %v", err)
	}
	skel, _, _, ctrlOff, _, _, err := buildSkeleton(file, funcTables{filters: table}, pf, np, ap, nil, nil, byo, nil, fset, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("buildSkeleton: %v", err)
	}
	type want struct {
		name string
		node gsxast.Node
		text string
	}
	var wants []want
	gsxast.Inspect(file, func(n gsxast.Node) bool {
		switch v := n.(type) {
		case *gsxast.ValueIf:
			wants = append(wants, want{"ValueIf cond", v, v.Cond})
		case *gsxast.ValueSwitch:
			wants = append(wants, want{"ValueSwitch tag", v, v.Tag})
		case *gsxast.ValueSwitchCase:
			if !v.Default {
				wants = append(wants, want{"ValueSwitchCase list", v, v.List})
			}
		case *gsxast.CondAttr:
			wants = append(wants, want{"CondAttr cond", v, v.Cond})
		case *gsxast.ClassPart:
			if v.Cond != "" {
				wants = append(wants, want{"ClassPart guard cond", v, v.Cond})
			}
		case *gsxast.SwitchMarkup:
			wants = append(wants, want{"SwitchMarkup tag", v, v.Tag})
		case *gsxast.CaseClause:
			if !v.Default {
				wants = append(wants, want{"CaseClause list", v, v.List})
			}
		}
		return true
	})
	if len(wants) != 8 {
		t.Fatalf("collected %d nav spans, want 8", len(wants))
	}
	for _, w := range wants {
		off, ok := ctrlOff[w.node]
		if !ok {
			t.Errorf("ctrlOff has no entry for %s %q", w.name, w.text)
			continue
		}
		if got := skel[off : off+len(w.text)]; got != w.text {
			t.Errorf("%s: skeleton at ctrlOff = %q, want %q (byte-faithful)", w.name, got, w.text)
		}
	}
}

// TestModulePackageBuildsCtrlMapValueIf verifies the Module path surfaces a
// CtrlMap entry for a value-form if condition, with ClauseStart mapping (via
// //line) back to the .gsx.
func TestModulePackageBuildsCtrlMapValueIf(t *testing.T) {
	root := t.TempDir()
	repoRoot, _ := filepath.Abs("../..")
	writeFile(t, root, "go.mod", "module example.com/app\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	pkgDir := filepath.Join(root, "page")
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, pkgDir, "page.gsx", "package page\n\ncomponent Box(disabled bool) {\n\t<input class={\n\t\t\"base\",\n\t\tif disabled { \"off\" }\n\t}/>\n}\n")

	m, _ := Open(Options{ModuleRoot: root, ModulePath: "example.com/app", FilterPkgs: []string{StdImportPath}})
	pr, err := m.Package(pkgDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(pr.Diags) > 0 {
		t.Fatalf("unexpected diagnostics: %+v", pr.Diags)
	}
	var vi *gsxast.ValueIf
	for _, gf := range pr.GSXFiles {
		gsxast.Inspect(gf, func(n gsxast.Node) bool {
			if v, ok := n.(*gsxast.ValueIf); ok {
				vi = v
			}
			return true
		})
	}
	if vi == nil {
		t.Fatal("no ValueIf parsed")
	}
	cr, ok := pr.CtrlMap[vi]
	if !ok || !cr.ClauseStart.IsValid() || cr.Node == nil {
		t.Fatalf("CtrlMap missing/invalid for ValueIf: %+v ok=%v", cr, ok)
	}
	dp := pr.Fset.Position(cr.ClauseStart)
	if !strings.HasSuffix(dp.Filename, ".gsx") {
		t.Errorf("ClauseStart maps to %s, want a .gsx position", dp.Filename)
	}
}

func TestConditionalBoolAttrCondKeepsLocalLive(t *testing.T) {
	root := t.TempDir()
	repoRoot, _ := filepath.Abs("../..")
	writeFile(t, root, "go.mod", "module example.com/app\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	writeFile(t, root, "page.gsx", "package app\n\ncomponent Check() {\n\t{{ boolean := true }}\n\t<input { if boolean { checked } } />\n}\n")

	res, err := GenerateDirs(root, []string{root}, Options{FilterPkgs: []string{StdImportPath}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if diags := res[root].Diags; len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %+v", diags)
	}
	var got string
	for _, src := range res[root].Files {
		got = string(src)
	}
	if !strings.Contains(got, "if boolean") {
		t.Fatalf("generated source missing conditional bool attr guard:\n%s", got)
	}
}
