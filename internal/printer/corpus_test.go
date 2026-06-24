package printer

import (
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/gsxhq/gsx/ast"
	"github.com/gsxhq/gsx/internal/txtar"
	"github.com/gsxhq/gsx/internal/wsnorm"
	"github.com/gsxhq/gsx/parser"
)

// corpusSource is one gsx source file extracted from the txtar corpus.
type corpusSource struct {
	name string // "<case-path>:<file>" label for test output
	src  string
}

// corpusGsxSources collects every *.gsx source embedded in the txtar corpus
// (internal/corpus/testdata/cases/**/*.txtar). That corpus is the maintained
// source of truth — every case parses, codegens, and renders in internal/corpus,
// so it cannot harbor stale syntax — which makes it the right round-trip input for
// the formatter (replacing the old hand-kept examples/ folder). Non-parseable
// sources (intentional parser-error cases) are skipped by each test, as before.
func corpusGsxSources(t *testing.T) []corpusSource {
	t.Helper()
	const root = "../corpus/testdata/cases"
	var out []corpusSource
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".txtar") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		for _, f := range txtar.Parse(data).Files {
			if strings.HasSuffix(f.Name, ".gsx") {
				out = append(out, corpusSource{rel + ":" + f.Name, string(f.Data)})
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) == 0 {
		t.Fatal("no corpus .gsx sources found under " + root)
	}
	return out
}

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
	for _, c := range corpusGsxSources(t) {
		// Only sources that PARSE originally are in scope (parser-error cases skip).
		fset := token.NewFileSet()
		if _, err := parser.ParseFile(fset, c.name, c.src, 0); err != nil {
			continue
		}
		out1, err := normPrint(t, c.src)
		if err != nil {
			t.Errorf("%s print: %v", c.name, err)
			continue
		}
		out2, err := normPrint(t, out1)
		if err != nil {
			t.Errorf("%s REPARSE FAIL: %v\n%s", c.name, err, out1)
			continue
		}
		if out1 != out2 {
			t.Errorf("%s NOT idempotent", c.name)
		}
	}
}

// zeroSpans resets every node's span to zero so two ASTs parsed from different
// text can be compared by content alone (positions necessarily differ).
// Position fields outside the embedded span (e.g. Interp.ExprPos) are also
// cleared so deep-equal is unaffected by source layout differences.
func zeroSpans(n ast.Node) {
	ast.Inspect(n, func(m ast.Node) bool {
		if m != nil {
			ast.SetSpan(m, 0, 0)
			// Position fields outside the embedded span (set by the parser for
			// codegen //line columns and the LSP) must also be zeroed so the
			// faithfulness comparison ignores source layout.
			switch v := m.(type) {
			case *ast.Interp:
				v.ExprPos = 0
			case *ast.ExprAttr:
				v.ExprPos = 0
			case *ast.Component:
				v.ParamsPos = 0
			}
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
	for _, c := range corpusGsxSources(t) {
		fset := token.NewFileSet()
		if _, err := parser.ParseFile(fset, c.name, c.src, 0); err != nil {
			continue // skip non-parseable (parser-error cases)
		}
		formatted, err := normPrint(t, c.src)
		if err != nil {
			t.Errorf("%s print: %v", c.name, err)
			continue
		}
		want := normalizedAST(t, c.src)
		got := normalizedAST(t, formatted)
		if !reflect.DeepEqual(want, got) {
			t.Errorf("%s: fmt changed the normalized AST (not render-faithful)", c.name)
		}
	}
}
