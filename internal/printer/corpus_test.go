package printer

import (
	"encoding/json"
	goast "go/ast"
	goparser "go/parser"
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
	"github.com/gsxhq/gsx/internal/pretty"
	"github.com/gsxhq/gsx/internal/txtar"
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

func printSource(t *testing.T, src string) (string, error) {
	return printSourceAt(t, src, 80)
}

func printSourceAt(_ *testing.T, src string, width int) (string, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "x.gsx", src, 0)
	if err != nil {
		return "", err
	}

	var b strings.Builder
	err = Fprint(&b, f, width, pretty.DefaultTabWidth)
	return b.String(), err
}

// fmtCorpusInputs collects the input.gsx of every fmt-corpus case
// (internal/gsxfmt/testdata/cases). Those inputs are written to stress layout,
// so they exercise syntactic layouts the semantic corpus does not.
func fmtCorpusInputs(t *testing.T) []corpusSource {
	t.Helper()
	paths, err := filepath.Glob("../gsxfmt/testdata/cases/*.txtar")
	if err != nil {
		t.Fatal(err)
	}
	var out []corpusSource
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range txtar.Parse(data).Files {
			if f.Name == "input.gsx" {
				out = append(out, corpusSource{"fmt:" + filepath.Base(path), string(f.Data)})
			}
		}
	}
	if len(out) == 0 {
		t.Fatal("no fmt corpus inputs found")
	}
	return out
}

// TestCorpusIdempotence checks both corpora at default and narrow widths.
func TestCorpusIdempotence(t *testing.T) {
	srcs := append(corpusGsxSources(t), fmtCorpusInputs(t)...)
	for _, c := range srcs {
		// Only sources that PARSE originally are in scope (parser-error cases skip).
		fset := token.NewFileSet()
		if _, err := parser.ParseFile(fset, c.name, c.src, 0); err != nil {
			continue
		}
		for _, width := range []int{20, 30, 40, 50, 60, 80} {
			out1, err := printSourceAt(t, c.src, width)
			if err != nil {
				t.Errorf("%s@%d print: %v", c.name, width, err)
				continue
			}
			out2, err := printSourceAt(t, out1, width)
			if err != nil {
				t.Errorf("%s@%d REPARSE FAIL: %v\n%s", c.name, width, err, out1)
				continue
			}
			if out1 != out2 {
				t.Errorf("%s@%d NOT idempotent\n--- once ---\n%s\n--- twice ---\n%s", c.name, width, out1, out2)
			}
		}
	}
}

// zeroSpans resets every node's span to zero so two ASTs parsed from different
// text can be compared by content alone (positions necessarily differ).
// Position fields outside the embedded span (e.g. Interp.ExprPos) are also
// cleared so deep-equal is unaffected by source layout differences.
func zeroSpans(n ast.Node) {
	if f, ok := n.(*ast.File); ok {
		f.Layout = ast.SourceLayout{}
	}
	ast.Inspect(n, func(m ast.Node) bool {
		if m != nil {
			if _, ok := m.(ast.GoText); ok {
				// GoText is the one VALUE leaf Inspect yields (GoWithElements
				// holds its parts by value): SetSpan through an interface copy
				// can never reach the stored part, and now panics instead of
				// silently no-opping. Skipping keeps the long-standing
				// behavior explicit — GoText spans never differ here because
				// contentAST canonicalizes Go fragments on both sides.
				return true
			}
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
			case *ast.OrderedPair:
				v.KeyPos = 0
				v.KeyEnd = 0
			case *ast.Comment:
				v.Layout = ast.CommentLayout{}
			case *ast.CommentAttr:
				v.Layout = ast.CommentLayout{}
			case *ast.MarkupAttr:
				v.Layout = ast.BodyLayout{}
			case *ast.Component:
				v.Layout = ast.BodyLayout{}
				v.NamePos = 0
				v.ParamsPos = 0
				v.RecvPos = 0
				v.TypeParamsPos = 0
			case *ast.Element:
				v.CloseNamePos = 0
				v.TagPos = 0
				v.TypeArgsOpenPos = 0
				v.TypeArgsPos = 0
				v.TypeArgsClosePos = 0
				v.AttrsMultiline = false
			case *ast.SpreadAttr:
				v.ExprPos = 0
				for i := range v.Stages {
					v.Stages[i].NamePos = 0
					v.Stages[i].ArgsPos = 0
				}
			case *ast.ComposedPart:
				v.LiteralPos = 0
				v.LiteralEnd = 0
				v.ExprPos = 0
				v.CondPos = 0
				for i := range v.Stages {
					v.Stages[i].NamePos = 0
					v.Stages[i].ArgsPos = 0
				}
			case *ast.CondAttr:
				v.CondPos = 0
			case *ast.SwitchAttr:
				v.TagPos = 0
			case *ast.AttrCaseClause:
				v.ListPos = 0
			case *ast.ForMarkup:
				v.ClausePos = 0
				v.BodyMultiline = false
				v.Layout = ast.BodyLayout{}
			case *ast.IfMarkup:
				v.CondPos = 0
				v.ThenMultiline = false
				v.ThenLayout = ast.BodyLayout{}
				v.ElseLayout = ast.BodyLayout{}
				v.ElseMultiline = false
			case *ast.SwitchMarkup:
				v.TagPos = 0
			case *ast.CaseClause:
				v.ListPos = 0
				v.BodyMultiline = false
				v.Layout = ast.BodyLayout{}
			case *ast.ValueIf:
				v.CondPos = 0
			case *ast.ValueSwitch:
				v.TagPos = 0
			case *ast.ValueSwitchCase:
				v.ListPos = 0
			case *ast.ValueArm:
				v.SourceBraced = false
				v.LiteralPos = 0
				v.LiteralEnd = 0
				v.ExprPos = 0
				for i := range v.Stages {
					v.Stages[i].NamePos = 0
					v.Stages[i].ArgsPos = 0
				}
			case *ast.GoBlock:
				v.CodePos = 0
			case *ast.GoWithElements:
				// Parts holds ast.GoText BY VALUE (not *ast.GoText), so the
				// generic ast.SetSpan(m, 0, 0) dispatch above — which only
				// matches pointer node types — can never reach a GoText's span
				// through Inspect's per-leaf callback: Inspect passes a fresh
				// interface copy for each value-typed part, and mutating that
				// copy can't write back into v.Parts. Zero it here instead,
				// where v.Parts is the real slice (accessible because
				// GoWithElements itself is always a *ast.GoWithElements
				// pointer). *ast.Element parts need no special handling: they
				// recurse into the ordinary *ast.Element case below via
				// Inspect's own GoWithElements traversal.
				for i, part := range v.Parts {
					if gt, ok := part.(ast.GoText); ok {
						ast.SetSpan(&gt, 0, 0)
						v.Parts[i] = gt
					}
				}
			case *ast.EmbeddedAttr:
				for _, m := range v.Segments {
					zeroSpans(m)
				}
				for i := range v.Stages {
					v.Stages[i].NamePos = 0
					v.Stages[i].ArgsPos = 0
				}
			case *ast.EmbeddedInterp:
				for i := range v.Stages {
					v.Stages[i].NamePos = 0
					v.Stages[i].ArgsPos = 0
				}
			case *ast.OrderedAttrsAttr:
				for i := range v.Pairs {
					ast.SetSpan(&v.Pairs[i], 0, 0)
					v.Pairs[i].ValuePos = 0
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
		v.Src = fmtGoChunk(v.Src, 80, pretty.DefaultTabWidth)
	case *ast.GoWithElements:
		// A GoText part is an incomplete Go fragment that fmtGoChunk (go/format)
		// can't parse in isolation. The printer's goWithElements does not leave it
		// verbatim, though: it runs fmtGoExprParts (substitute a placeholder per
		// embedded value, go/format the now-complete Go, re-split at the
		// placeholders), so gofmt lays the GoText out — inserting, for instance, a
		// blank line between a `type` decl and a following element-bearing `var`.
		// The normalizer MUST apply the same pass, or the faithfulness comparison
		// reads that gofmt reflow as an AST change: normalize(S) would hold the
		// author's verbatim GoText while normalize(fmt(S)) holds gofmt's. When
		// go/format rejects the substituted source (Go the gsx parser accepted but
		// go/parser does not), the printer keeps the original parts, so we do too.
		formatted, shapes, ok := (&printer{width: 80, tabWidth: pretty.DefaultTabWidth}).fmtGoExprParts(v.Parts)
		if ok {
			v.Parts = formatted
		}
		// GoText is reduced exactly as the printer emits it, so the comparison
		// ignores the blank-line padding between this decl and its neighbors
		// (file's blank-line-separator logic re-derives it) and the decorative
		// parens around a value (codegen strips them; the printer re-derives
		// them). Embedded *ast.Element parts recurse into the ordinary
		// *ast.Element case below.
		_, wrapped := decorativeParens(v.Parts, shapes)
		last := len(v.Parts) - 1
		for i, part := range v.Parts {
			switch pt := part.(type) {
			case ast.GoText:
				pt.Src = goTextSrc(pt, i, last, wrapped)
				v.Parts[i] = pt
			case *ast.Element:
				canonGo(pt)
			}
		}
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
	case *ast.MarkerRegion:
		canonGoAttr(v.Name)
		for _, c := range v.Children {
			canonGo(c)
		}
	case *ast.Fragment:
		for _, c := range v.Children {
			canonGo(c)
		}
	case *ast.Interp:
		v.Expr = canonGoField(v.Expr, exprField, fmtExpr)
		for i := range v.Stages {
			if v.Stages[i].HasArgs {
				v.Stages[i].Args = fmtArgs(v.Stages[i].Args)
			}
		}
	case *ast.GoBlock:
		v.Code = canonLiteralGo(v.Code, goFieldForm{}, fmtStmts)
	case *ast.IfMarkup:
		v.Cond = canonGoField(v.Cond, ifField, fmtExpr)
		for _, m := range v.Then {
			canonGo(m)
		}
		for _, m := range v.Else {
			canonGo(m)
		}
	case *ast.ForMarkup:
		v.Clause = canonGoField(v.Clause, forField, fmtClause)
		for _, m := range v.Body {
			canonGo(m)
		}
	case *ast.SwitchMarkup:
		v.Tag = canonGoField(v.Tag, switchField, fmtExpr)
		for _, c := range v.Cases {
			if !c.Default {
				c.List = canonGoField(c.List, caseField, fmtCaseList)
			}
			for _, m := range c.Body {
				canonGo(m)
			}
		}
	}
}

// canonGoField canonicalizes Go independently of the markup printer. Embedded
// values become structural signatures containing exact literal Text values.
func canonGoField(src string, form goFieldForm, plain func(string) string) string {
	return canonLiteralGo(src, form, plain)
}

func canonLiteralGo(src string, form goFieldForm, plain func(string) string) string {
	parts, ok := splitGoParts(src)
	if !ok || len(parts) == 0 {
		return plain(src)
	}
	var signatures []string
	for _, part := range parts {
		if _, ok := part.(ast.GoText); ok {
			continue
		}
		canonGo(part)
		canonEmbeddedBodies(part)
		zeroSpans(part)
		data, err := json.Marshal(part)
		if err != nil {
			panic(err)
		}
		signatures = append(signatures, string(data))
	}
	prefix := "__gsxSignature"
	for strings.Contains(src, prefix) || strings.Contains(strings.Join(signatures, ""), prefix) {
		prefix += "z"
	}
	var code strings.Builder
	index := 0
	for _, part := range parts {
		if text, ok := part.(ast.GoText); ok {
			code.WriteString(text.Src)
			continue
		}
		code.WriteString(prefix + strconv.Itoa(index) + "__")
		index++
	}
	out := goStructureSignature(plain(code.String()), form)
	for i, sig := range signatures {
		out = strings.ReplaceAll(out, prefix+strconv.Itoa(i)+"__", sig)
	}
	return out
}

// goStructureSignature compares parsed Go structure, which omits cosmetic
// differences such as line breaks and optional trailing commas.
func goStructureSignature(src string, form goFieldForm) string {
	fset := token.NewFileSet()
	tree, err := goparser.ParseFile(fset, "", goBlockWrapperPrefix+form.open+src+form.close+goBlockWrapperSuffix, goparser.SkipObjectResolution)
	if err != nil {
		return src
	}
	var out strings.Builder
	err = goast.Fprint(&out, nil, tree, func(name string, value reflect.Value) bool {
		return value.Type() != reflect.TypeFor[token.Pos]() && name != "Obj" && name != "Scope" && name != "Unresolved"
	})
	if err != nil {
		panic(err)
	}
	return out.String()
}

func canonGoAttr(a ast.Attr) {
	switch v := a.(type) {
	case *ast.ExprAttr:
		v.Expr = canonGoField(v.Expr, exprField, fmtExpr)
		for i := range v.Stages {
			if v.Stages[i].HasArgs {
				v.Stages[i].Args = fmtArgs(v.Stages[i].Args)
			}
		}
	case *ast.SpreadAttr:
		v.Expr = fmtExpr(v.Expr)
	case *ast.ComposedAttr:
		for i := range v.Parts {
			if v.Parts[i].CF != nil {
				canonValueCF(v.Parts[i].CF)
				continue
			}
			v.Parts[i].Expr = canonGoField(v.Parts[i].Expr, exprField, fmtExpr)
			for j := range v.Parts[i].Stages {
				if v.Parts[i].Stages[j].HasArgs {
					v.Parts[i].Stages[j].Args = fmtArgs(v.Parts[i].Stages[j].Args)
				}
			}
			if v.Parts[i].Cond != "" {
				v.Parts[i].Cond = canonGoField(v.Parts[i].Cond, exprField, fmtExpr)
			}
		}
	case *ast.CondAttr:
		v.Cond = canonGoField(v.Cond, ifField, fmtExpr)
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
	case *ast.EmbeddedAttr:
		for _, m := range v.Segments {
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
	vi.Cond = canonGoField(vi.Cond, ifField, fmtExpr)
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
		vs.Tag = canonGoField(vs.Tag, switchField, fmtExpr)
	}
	for _, c := range vs.Cases {
		if !c.Default {
			c.List = canonGoField(c.List, caseField, fmtCaseList)
		}
		if c.Value != nil {
			canonValueArm(c.Value)
		}
	}
}

func canonValueArm(a *ast.ValueArm) {
	a.Expr = canonGoField(a.Expr, exprField, fmtExpr)
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
func canonEmbeddedBodies(f ast.Node) {
	ast.Inspect(f, func(n ast.Node) bool {
		if lit, ok := n.(*ast.EmbeddedInterp); ok {
			var sig func([]byte) string
			if lit.Lang == ast.EmbeddedJS {
				sig = jsfmt.TokenSignature
			}
			if lit.Lang == ast.EmbeddedCSS {
				sig = cssfmt.TokenSignature
			}
			if sig != nil {
				lit.Segments = []ast.Markup{&ast.Text{Value: embeddedSignature(lit.Segments, sig)}}
				return false
			}
		}
		if attr, ok := n.(*ast.EmbeddedAttr); ok {
			var sig func([]byte) string
			if attr.Lang == ast.EmbeddedJS {
				sig = jsfmt.TokenSignature
			}
			if attr.Lang == ast.EmbeddedCSS {
				sig = cssfmt.TokenSignature
			}
			if sig != nil {
				attr.Segments = []ast.Markup{&ast.Text{Value: embeddedSignature(attr.Segments, sig)}}
				return false
			}
		}
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

func contentAST(t *testing.T, src string) *ast.File {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "x.gsx", src, 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	canonGo(f)
	canonEmbeddedBodies(f)
	zeroSpans(f)
	return f
}

// TestCorpusFaithfulness compares exact markup structure and Text after
// canonicalizing only Go syntax and the separate JS/CSS token contracts.
func TestCorpusFaithfulness(t *testing.T) {
	for _, c := range append(corpusGsxSources(t), fmtCorpusInputs(t)...) {
		fset := token.NewFileSet()
		if _, err := parser.ParseFile(fset, c.name, c.src, 0); err != nil {
			continue // skip non-parseable (parser-error cases)
		}
		formatted, err := printSource(t, c.src)
		if err != nil {
			t.Errorf("%s print: %v", c.name, err)
			continue
		}
		want := contentAST(t, c.src)
		got := contentAST(t, formatted)
		if !reflect.DeepEqual(want, got) {
			t.Errorf("%s: fmt changed the content AST (not render-faithful)", c.name)
		}
	}
}
