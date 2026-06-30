package printer

import (
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/gsxhq/gsx/ast"
	"github.com/gsxhq/gsx/internal/cssfmt"
	"github.com/gsxhq/gsx/internal/jsfmt"
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
	err = Fprint(&b, f, 80)
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
				for i := range v.Stages {
					v.Stages[i].NamePos = 0
					v.Stages[i].ArgsPos = 0
				}
			case *ast.ExprAttr:
				v.ExprPos = 0
				for i := range v.Stages {
					v.Stages[i].NamePos = 0
					v.Stages[i].ArgsPos = 0
				}
			case *ast.Component:
				v.NamePos = 0
				v.ParamsPos = 0
				v.RecvPos = 0
			case *ast.Element:
				v.CloseNamePos = 0
			case *ast.ForMarkup:
				v.ClausePos = 0
			case *ast.IfMarkup:
				v.CondPos = 0
			case *ast.ValueIf:
				v.CondPos = 0
			case *ast.ValueArm:
				for i := range v.Stages {
					v.Stages[i].NamePos = 0
					v.Stages[i].ArgsPos = 0
				}
			case *ast.GoBlock:
				v.CodePos = 0
			case *ast.OrderedAttrsAttr:
				for i := range v.Pairs {
					ast.SetSpan(&v.Pairs[i], 0, 0)
				}
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
			if v.Parts[i].CF != nil {
				canonValueCF(v.Parts[i].CF)
				continue
			}
			v.Parts[i].Expr = fmtExpr(v.Parts[i].Expr)
			for j := range v.Parts[i].Stages {
				if v.Parts[i].Stages[j].HasArgs {
					v.Parts[i].Stages[j].Args = fmtArgs(v.Parts[i].Stages[j].Args)
				}
			}
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
	case *ast.OrderedAttrsAttr:
		for i := range v.Pairs {
			v.Pairs[i].Value = fmtExpr(v.Pairs[i].Value)
		}
	}
}

func canonValueCF(cf *ast.ValueCF) {
	if cf.If != nil {
		canonValueIf(cf.If)
	}
	if cf.Switch != nil {
		canonValueSwitch(cf.Switch)
	}
}

func canonValueIf(vi *ast.ValueIf) {
	vi.Cond = fmtExpr(vi.Cond)
	if vi.Then != nil {
		canonValueArm(vi.Then)
	}
	if vi.ElseIf != nil {
		canonValueIf(vi.ElseIf)
	}
	if vi.Else != nil {
		canonValueArm(vi.Else)
	}
}

func canonValueSwitch(vs *ast.ValueSwitch) {
	if vs.Tag != "" {
		vs.Tag = fmtExpr(vs.Tag)
	}
	for _, c := range vs.Cases {
		if !c.Default {
			c.List = fmtCaseList(c.List)
		}
		if c.Value != nil {
			canonValueArm(c.Value)
		}
	}
}

func canonValueArm(a *ast.ValueArm) {
	a.Expr = fmtExpr(a.Expr)
	for i := range a.Stages {
		if a.Stages[i].HasArgs {
			a.Stages[i].Args = fmtArgs(a.Stages[i].Args)
		}
	}
}

// canonEmbeddedBodies replaces every <style> AND <script> element's children
// with one synthetic Text holding a canonical signature of the body (the
// language's whitespace-agnostic token signature, with each hole sentinel mapped
// back to its rendered text). This makes the faithfulness comparison check
// token-equivalence + hole-sequence — the contract the re-indenter satisfies —
// rather than the byte-identity that re-indentation deliberately breaks.
func canonEmbeddedBodies(f *ast.File) {
	ast.Inspect(f, func(n ast.Node) bool {
		el, ok := n.(*ast.Element)
		if !ok {
			return true
		}
		switch {
		case strings.EqualFold(el.Tag, "style"):
			el.Children = []ast.Markup{&ast.Text{Value: embeddedSignature(el.Children, cssfmt.TokenSignature)}}
			return false
		case strings.EqualFold(el.Tag, "script"):
			el.Children = []ast.Markup{&ast.Text{Value: embeddedSignature(el.Children, jsfmt.TokenSignature)}}
			return false
		}
		return true
	})
}

// embeddedSignature builds the canonical signature: the body's placeholdered
// text (holes → a fixed sentinel) run through sig (a language TokenSignature),
// with each sentinel mapped back to its rendered hole. Both src and fmt(src)
// reduce to the same string iff their token streams and hole sequences match.
//
// The sentinel is "__gsxH" + index + "__" — a valid identifier in both CSS and
// JS tokenizers, so it survives both tokenizers verbatim and can be mapped back.
// (The brief's original "\x00H" sentinel is valid for CSS but causes a lex error
// in tdewolff's JS lexer, which rejects \x00 bytes; per the brief's note we
// switch to a sentinel both lexers accept.)
func embeddedSignature(nodes []ast.Markup, sig func([]byte) string) string {
	const sent = "__gsxH"
	var body strings.Builder
	var holes []string
	for _, n := range nodes {
		switch v := n.(type) {
		case *ast.Text:
			body.WriteString(v.Value)
		case *ast.Interp:
			body.WriteString(sent)
			body.WriteString(strconv.Itoa(len(holes)))
			body.WriteString("__")
			holes = append(holes, renderHole(v))
		}
	}
	s := sig([]byte(body.String()))
	for i, h := range holes {
		s = strings.ReplaceAll(s, sent+strconv.Itoa(i)+"__", h)
	}
	return s
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
	canonEmbeddedBodies(f)
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
