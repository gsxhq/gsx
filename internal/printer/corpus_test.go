package printer

import (
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/gsxhq/gsx/ast"
	"github.com/gsxhq/gsx/internal/wsnorm"
	"github.com/gsxhq/gsx/parser"
)

func normPrint(t *testing.T, src string) (string, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "x.gsx", src, 0)
	if err != nil {
		return "", err
	}
	wsnorm.Normalize(f)
	var b strings.Builder
	err = Fprint(&b, f)
	return b.String(), err
}
func TestCorpusIdempotence(t *testing.T) {
	files, _ := filepath.Glob("../../examples/*.gsx")
	for _, fn := range files {
		data, _ := os.ReadFile(fn)
		// Only files that PARSE originally are in scope (02 is a known parse-fail).
		fset := token.NewFileSet()
		if _, err := parser.ParseFile(fset, fn, string(data), 0); err != nil {
			t.Logf("%s: skipped (does not parse: %v)", filepath.Base(fn), err)
			continue
		}
		out1, err := normPrint(t, string(data))
		if err != nil {
			t.Errorf("%s print: %v", fn, err)
			continue
		}
		out2, err := normPrint(t, out1)
		if err != nil {
			t.Errorf("%s REPARSE FAIL: %v\n%s", filepath.Base(fn), err, out1)
			continue
		}
		if out1 != out2 {
			t.Errorf("%s NOT idempotent", filepath.Base(fn))
		}
	}
}

// zeroSpans resets every node's span to zero so two ASTs parsed from different
// text can be compared by content alone (positions necessarily differ).
func zeroSpans(n ast.Node) {
	ast.Inspect(n, func(m ast.Node) bool {
		if m != nil {
			ast.SetSpan(m, 0, 0)
		}
		return true
	})
}

// canonGo canonicalizes every Go-fragment string field in the AST using the
// printer's own fmt* helpers, so the faithfulness comparison ignores gofmt-only
// differences (the formatter is allowed to canonicalize Go fragments) and tests
// exactly that the MARKUP STRUCTURE and TEXT content are preserved.
func canonGo(n ast.Node) {
	switch v := n.(type) {
	case *ast.File:
		for _, d := range v.Decls {
			canonGo(d)
		}
	case *ast.GoChunk:
		v.Src = fmtGoChunk(v.Src)
	case *ast.Component:
		v.Recv = fmtRecv(v.Recv)
		v.Params = fmtParams(v.Params)
		for _, m := range v.Body {
			canonGo(m)
		}
	case *ast.Element:
		for _, a := range v.Attrs {
			canonGoAttr(a)
		}
		for _, c := range v.Children {
			canonGo(c)
		}
	case *ast.Fragment:
		for _, c := range v.Children {
			canonGo(c)
		}
	case *ast.Interp:
		v.Expr = fmtExpr(v.Expr)
		for i := range v.Stages {
			if v.Stages[i].HasArgs {
				v.Stages[i].Args = fmtArgs(v.Stages[i].Args)
			}
		}
	case *ast.GoBlock:
		v.Code = fmtStmts(v.Code)
	case *ast.IfMarkup:
		v.Cond = fmtExpr(v.Cond)
		for _, m := range v.Then {
			canonGo(m)
		}
		for _, m := range v.Else {
			canonGo(m)
		}
	case *ast.ForMarkup:
		v.Clause = fmtClause(v.Clause)
		for _, m := range v.Body {
			canonGo(m)
		}
	case *ast.SwitchMarkup:
		v.Tag = fmtExpr(v.Tag)
		for _, c := range v.Cases {
			if !c.Default {
				c.List = fmtCaseList(c.List)
			}
			for _, m := range c.Body {
				canonGo(m)
			}
		}
	}
}

func canonGoAttr(a ast.Attr) {
	switch v := a.(type) {
	case *ast.ExprAttr:
		v.Expr = fmtExpr(v.Expr)
		for i := range v.Stages {
			if v.Stages[i].HasArgs {
				v.Stages[i].Args = fmtArgs(v.Stages[i].Args)
			}
		}
	case *ast.SpreadAttr:
		v.Expr = fmtExpr(v.Expr)
	case *ast.ClassAttr:
		for i := range v.Parts {
			v.Parts[i].Expr = fmtExpr(v.Parts[i].Expr)
			if v.Parts[i].Cond != "" {
				v.Parts[i].Cond = fmtExpr(v.Parts[i].Cond)
			}
		}
	case *ast.CondAttr:
		v.Cond = fmtExpr(v.Cond)
		for _, t := range v.Then {
			canonGoAttr(t)
		}
		for _, e := range v.Else {
			canonGoAttr(e)
		}
	case *ast.MarkupAttr:
		for _, m := range v.Value {
			canonGo(m)
		}
	}
}

func normalizedAST(t *testing.T, src string) *ast.File {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "x.gsx", src, 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	wsnorm.Normalize(f)
	canonGo(f)
	zeroSpans(f)
	return f
}

// TestCorpusFaithfulness is the strong contract: Normalize(parse(fmt(S))) must
// equal Normalize(parse(S)) by content for every parseable corpus file. fmt only
// adds collapsible cosmetic whitespace.
func TestCorpusFaithfulness(t *testing.T) {
	files, _ := filepath.Glob("../../examples/*.gsx")
	for _, fn := range files {
		data, _ := os.ReadFile(fn)
		fset := token.NewFileSet()
		if _, err := parser.ParseFile(fset, fn, string(data), 0); err != nil {
			continue // skip non-parseable (e.g. 02)
		}
		formatted, err := normPrint(t, string(data))
		if err != nil {
			t.Errorf("%s print: %v", filepath.Base(fn), err)
			continue
		}
		want := normalizedAST(t, string(data))
		got := normalizedAST(t, formatted)
		if !reflect.DeepEqual(want, got) {
			t.Errorf("%s: fmt changed the normalized AST (not render-faithful)", filepath.Base(fn))
		}
	}
}
