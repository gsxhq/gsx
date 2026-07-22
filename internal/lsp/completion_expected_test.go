package lsp

import (
	"go/ast"
	"go/token"
	"go/types"
	"strings"
	"testing"

	gsxast "github.com/gsxhq/gsx/ast"
	gsxparser "github.com/gsxhq/gsx/parser"
)

// TestTypeMatches pins the match predicate: direct assignability, a func
// candidate whose single result is assignable (calling it satisfies the
// position), and the negative cases (mismatched value, multi-result func, nil).
func TestTypeMatches(t *testing.T) {
	str := types.Typ[types.String]
	i := types.Typ[types.Int]
	strFunc := types.NewSignatureType(nil, nil, nil, types.NewTuple(),
		types.NewTuple(types.NewVar(token.NoPos, nil, "", str)), false)
	twoResFunc := types.NewSignatureType(nil, nil, nil, types.NewTuple(),
		types.NewTuple(types.NewVar(token.NoPos, nil, "", str), types.NewVar(token.NoPos, nil, "", i)), false)

	cases := []struct {
		name           string
		cand, expected types.Type
		want           bool
	}{
		{"string-to-string", str, str, true},
		{"int-to-string", i, str, false},
		{"func-result-matches", strFunc, str, true},
		{"func-result-mismatch", strFunc, i, false},
		{"multi-result-func-no-match", twoResFunc, str, false},
		{"nil-candidate", nil, str, false},
		{"nil-expected", str, nil, false},
	}
	for _, c := range cases {
		if got := typeMatches(c.cand, c.expected); got != c.want {
			t.Errorf("%s: typeMatches = %v, want %v", c.name, got, c.want)
		}
	}
}

// TestParamTypeAt covers positional and variadic parameter resolution: an
// in-range index returns its own type, the variadic tail returns the element
// type, and a non-variadic overflow returns nil.
func TestParamTypeAt(t *testing.T) {
	str := types.Typ[types.String]
	i := types.Typ[types.Int]
	// (s string, nums ...int)
	variadic := types.NewSignatureType(nil, nil, nil, types.NewTuple(
		types.NewVar(token.NoPos, nil, "s", str),
		types.NewVar(token.NoPos, nil, "nums", types.NewSlice(i)),
	), nil, true)
	// (a string, b int)
	fixed := types.NewSignatureType(nil, nil, nil, types.NewTuple(
		types.NewVar(token.NoPos, nil, "a", str),
		types.NewVar(token.NoPos, nil, "b", i),
	), nil, false)

	if got := paramTypeAt(variadic, 0); got != str {
		t.Errorf("variadic idx 0 = %v, want string", got)
	}
	if got := paramTypeAt(variadic, 1); got != i {
		t.Errorf("variadic idx 1 (tail) = %v, want int (element type)", got)
	}
	if got := paramTypeAt(variadic, 5); got != i {
		t.Errorf("variadic idx 5 (tail) = %v, want int (element type)", got)
	}
	if got := paramTypeAt(fixed, 1); got != i {
		t.Errorf("fixed idx 1 = %v, want int", got)
	}
	if got := paramTypeAt(fixed, 2); got != nil {
		t.Errorf("fixed overflow idx 2 = %v, want nil", got)
	}
}

// TestInnerCallArgExpectedType checks the inner call-arg derivation over a real
// type-checked package: a cursor inside `f(▮)` yields f's first parameter type,
// and a cursor inside the second argument yields the second parameter type.
func TestInnerCallArgExpectedType(t *testing.T) {
	src := `package p

func f(s string, n int) {}

var _ = f("a", 3)
`
	pkg, tf := buildSyntheticPackage(t, src)

	// Cursor on the first argument literal "a".
	firstOff := strings.Index(src, `f("a"`) + len(`f("`)
	got := innerCallArgExpectedType(pkg.Info, callRootAt(t, pkg), tf.Pos(firstOff))
	if got == nil || got.String() != "string" {
		t.Errorf("arg 0 expected type = %v, want string", got)
	}

	// Cursor on the second argument literal 3.
	secondOff := strings.Index(src, `"a", 3`) + len(`"a", `)
	got = innerCallArgExpectedType(pkg.Info, callRootAt(t, pkg), tf.Pos(secondOff))
	if got == nil || got.String() != "int" {
		t.Errorf("arg 1 expected type = %v, want int", got)
	}
}

// TestInnerCallArgSelectorReceiverIrrelevant pins selector-receiver
// irrelevance: a cursor on the RECEIVER of `x.M()` is before the call's `(` and
// so derives no expected type.
func TestInnerCallArgSelectorReceiverIrrelevant(t *testing.T) {
	src := `package p

type T struct{}

func (T) M(s string) {}

var v T
var _ = v.M("a")
`
	pkg, tf := buildSyntheticPackage(t, src)
	// Cursor on the receiver `v` in `v.M(...)`.
	recvOff := strings.Index(src, "v.M(") + 1 // on `v`, before the dot
	if got := innerCallArgExpectedType(pkg.Info, callRootAt(t, pkg), tf.Pos(recvOff)); got != nil {
		t.Errorf("receiver cursor derived expected type %v, want nil", got)
	}
}

// TestInnerCallArgExpectedTypeConversionExcluded is the regression test for
// the reviewed type-conversion misclassification: T(x) where T's underlying
// type is itself a function signature (the http.HandlerFunc idiom) must NOT
// be treated as a call. Before the fix, the structural
// Underlying()-is-*Signature check alone couldn't distinguish a conversion's
// callee from a real call's, so `Handler(mk(▮))` derived Handler's own first
// parameter type (int) instead of correctly deriving nothing.
func TestInnerCallArgExpectedTypeConversionExcluded(t *testing.T) {
	src := `package p

type Handler func(int) string

func mk(n int) Handler { return nil }

var h = Handler(mk(3))
`
	pkg, tf := buildSyntheticPackage(t, src)
	// Cursor inside mk's argument list — mk is a genuine call, so this must
	// still resolve to mk's own parameter type (int), unaffected by the fix.
	mkOff := strings.Index(src, "mk(3)") + len("mk(")
	got := innerCallArgExpectedType(pkg.Info, callConversionRootAt(t, pkg), tf.Pos(mkOff))
	if got == nil || got.String() != "int" {
		t.Errorf("plain call mk(▮) expected type = %v, want int (unaffected by the conversion fix)", got)
	}

	// Cursor inside the OUTER conversion Handler(...)'s argument list, but
	// past mk(3)'s own closing paren would be ambiguous; instead assert the
	// conversion itself, isolated, derives no expected type.
	convSrc := `package p

type Handler func(int) string

func mk() Handler { return nil }

var h = Handler(mk())
`
	pkg2, tf2 := buildSyntheticPackage(t, convSrc)
	convOff := strings.Index(convSrc, "Handler(mk())") + len("Handler(")
	if got := innerCallArgExpectedType(pkg2.Info, callConversionRootAt(t, pkg2), tf2.Pos(convOff)); got != nil {
		t.Errorf("conversion Handler(▮) expected type = %v, want nil (conversions are excluded)", got)
	}
}

// callConversionRootAt returns the outermost *ast.CallExpr recorded in
// pkg.Info.Types — the bridged hole root that ast.Inspect walks to find the
// innermost enclosing call. Fixtures above have exactly one top-level
// expression statement (`var h = ...`), so the outermost CallExpr by Lparen
// position is the correct root to hand innerCallArgExpectedType (mirroring
// how the real bridge hands the whole hole expression, not a pre-selected
// inner call).
func callConversionRootAt(t *testing.T, pkg *Package) ast.Expr {
	t.Helper()
	var outer *ast.CallExpr
	for expr := range pkg.Info.Types {
		call, ok := expr.(*ast.CallExpr)
		if !ok {
			continue
		}
		if outer == nil || call.Lparen < outer.Lparen {
			outer = call
		}
	}
	if outer == nil {
		t.Fatal("no CallExpr in Info.Types")
	}
	return outer
}

// TestTypeMatchesNAryFuncResult pins the n-ary func-result-match design
// decision explicitly (previously only a niladic func was covered): a func
// candidate's own arity is irrelevant to the match — only its single result's
// assignability matters, mirroring IDEs suggesting e.g. strconv.Itoa at an
// int-argument, string-expected position.
func TestTypeMatchesNAryFuncResult(t *testing.T) {
	str := types.Typ[types.String]
	i := types.Typ[types.Int]
	b := types.Typ[types.Bool]
	// func(int, bool) string — n-ary (arity 2), single string result.
	nAryFunc := types.NewSignatureType(nil, nil, nil, types.NewTuple(
		types.NewVar(token.NoPos, nil, "", i),
		types.NewVar(token.NoPos, nil, "", b),
	), types.NewTuple(types.NewVar(token.NoPos, nil, "", str)), false)

	if !typeMatches(nAryFunc, str) {
		t.Errorf("n-ary func(int, bool) string must match string-expected position (arity irrelevant to result-match)")
	}
	if typeMatches(nAryFunc, i) {
		t.Errorf("n-ary func(int, bool) string must NOT match int-expected position")
	}
}

// callRootAt returns the *ast.CallExpr recorded in pkg.Info.Types (there is
// exactly one in the fixtures above) as the bridged hole root.
func callRootAt(t *testing.T, pkg *Package) ast.Expr {
	t.Helper()
	for expr := range pkg.Info.Types {
		if call, ok := expr.(*ast.CallExpr); ok {
			return call
		}
	}
	t.Fatal("no CallExpr in Info.Types")
	return nil
}

// TestComponentAttrExpectedType checks that a cursor in a component attr value
// hole `title={ ▮ }` derives the bound parameter's declared type from the
// ComponentCalls fact.
func TestComponentAttrExpectedType(t *testing.T) {
	src := "package page\n\ncomponent Home() {\n\t<Card title={x}/>\n}\n"
	fset := token.NewFileSet()
	f, err := gsxparser.ParseFile(fset, "page.gsx", []byte(src), 0)
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}
	var el *gsxast.Element
	var ea *gsxast.ExprAttr
	gsxast.Inspect(f, func(n gsxast.Node) bool {
		switch v := n.(type) {
		case *gsxast.Element:
			el = v
		case *gsxast.ExprAttr:
			ea = v
		}
		return true
	})
	if el == nil || ea == nil {
		t.Fatalf("element/ExprAttr not found (el=%v ea=%v)", el, ea)
	}

	titleVar := types.NewVar(token.NoPos, nil, "title", types.Typ[types.String])
	pkg := &Package{
		GSXFset: fset,
		Files:   map[string]*gsxast.File{"page.gsx": f},
		Info:    &types.Info{},
		ComponentCalls: map[*gsxast.Element]ComponentCallFact{
			el: {Params: map[gsxast.Attr]ComponentParamFact{
				ea: {Name: "title", Var: titleVar},
			}},
		},
	}
	exprStartOff := fset.Position(ea.ExprPos).Offset
	got := componentAttrExpectedType(pkg, exprStartOff, "page.gsx")
	if got == nil || got.String() != "string" {
		t.Errorf("componentAttrExpectedType = %v, want string", got)
	}
	// A non-matching offset derives nothing.
	if got := componentAttrExpectedType(pkg, exprStartOff+999, "page.gsx"); got != nil {
		t.Errorf("componentAttrExpectedType at wrong offset = %v, want nil", got)
	}
	// A matching offset but wrong file derives nothing either.
	if got := componentAttrExpectedType(pkg, exprStartOff, "other.gsx"); got != nil {
		t.Errorf("componentAttrExpectedType at right offset, wrong file = %v, want nil", got)
	}
}

// TestComponentAttrExpectedTypeCrossFileOffsetCollision is the regression test
// for the reviewed cross-file offset-collision bug: two sibling .gsx files in
// the same package (ComponentCalls is package-wide, not per-file) each have a
// component attr value hole whose in-file byte offset coincides. Before the
// fix, componentAttrExpectedType matched by offset alone, so which file's
// bound-parameter type came back depended on Go's randomized map iteration
// order over ComponentCalls — nondeterministically wrong on some runs. With
// the file-identity check, the match must be deterministic and correct for
// both files on every run (run with -count=10 to catch map-order flakes).
func TestComponentAttrExpectedTypeCrossFileOffsetCollision(t *testing.T) {
	// Identical prefix length up to the hole in both files so the ExprAttr's
	// value-expression start offset coincides across files: same tag (`<Card `,
	// 6 bytes) followed by an equal-length attr name (`title=` / `xtitl=`, both
	// 6 bytes).
	srcA := "package page\n\ncomponent A() {\n\t<Card title={x}/>\n}\n"
	srcB := "package page\n\ncomponent B() {\n\t<Card xtitl={x}/>\n}\n"

	fset := token.NewFileSet()
	fA, err := gsxparser.ParseFile(fset, "a.gsx", []byte(srcA), 0)
	if err != nil {
		t.Fatalf("ParseFile a.gsx: %v", err)
	}
	fB, err := gsxparser.ParseFile(fset, "b.gsx", []byte(srcB), 0)
	if err != nil {
		t.Fatalf("ParseFile b.gsx: %v", err)
	}

	findAttr := func(f *gsxast.File) (*gsxast.Element, *gsxast.ExprAttr) {
		var el *gsxast.Element
		var ea *gsxast.ExprAttr
		gsxast.Inspect(f, func(n gsxast.Node) bool {
			switch v := n.(type) {
			case *gsxast.Element:
				el = v
			case *gsxast.ExprAttr:
				ea = v
			}
			return true
		})
		return el, ea
	}
	elA, eaA := findAttr(fA)
	elB, eaB := findAttr(fB)
	if elA == nil || eaA == nil || elB == nil || eaB == nil {
		t.Fatalf("element/ExprAttr not found (elA=%v eaA=%v elB=%v eaB=%v)", elA, eaA, elB, eaB)
	}
	offA := fset.Position(eaA.ExprPos).Offset
	offB := fset.Position(eaB.ExprPos).Offset
	if offA != offB {
		t.Fatalf("fixture invariant broken: offA=%d offB=%d must coincide", offA, offB)
	}

	strVar := types.NewVar(token.NoPos, nil, "title", types.Typ[types.String])
	intVar := types.NewVar(token.NoPos, nil, "xtitl", types.Typ[types.Int])
	pkg := &Package{
		GSXFset: fset,
		Files:   map[string]*gsxast.File{"a.gsx": fA, "b.gsx": fB},
		Info:    &types.Info{},
		ComponentCalls: map[*gsxast.Element]ComponentCallFact{
			elA: {Params: map[gsxast.Attr]ComponentParamFact{eaA: {Name: "title", Var: strVar}}},
			elB: {Params: map[gsxast.Attr]ComponentParamFact{eaB: {Name: "xtitl", Var: intVar}}},
		},
	}

	for i := range 50 {
		gotA := componentAttrExpectedType(pkg, offA, "a.gsx")
		if gotA == nil || gotA.String() != "string" {
			t.Fatalf("iteration %d: file a.gsx at offset %d = %v, want string", i, offA, gotA)
		}
		gotB := componentAttrExpectedType(pkg, offB, "b.gsx")
		if gotB == nil || gotB.String() != "int" {
			t.Fatalf("iteration %d: file b.gsx at offset %d = %v, want int", i, offB, gotB)
		}
	}
}

// TestGoCompletionItemsExpectedRanking is the core ranking assertion: with a
// string expected type, a string local sorts ahead of an int local (both stay
// in tierLocal), and both outrank a matching package-scope string (locality
// dominates match). With no expected type the SortText is byte-identical to the
// tier-only form.
func TestGoCompletionItemsExpectedRanking(t *testing.T) {
	src := `package p

var pkgStr = "z"

func f() {
	sLocal := "a"
	nLocal := 3
	_ = sLocal
	_ = nLocal
	println(sLocal, nLocal)
}
`
	pkg, tf := buildSyntheticPackage(t, src)
	// Cursor at the println line — all three names (sLocal, nLocal, pkgStr) visible.
	off := strings.Index(src, "println(")
	pos := tf.Pos(off)
	scope := innermostScopeAt(pkg, pos)

	str := types.Typ[types.String]
	items := goCompletionItems(pkg, scope, nil, pos, false, str, src, off, off, encUTF8, "")
	sort := map[string]string{}
	for _, it := range items {
		sort[it.Label] = it.SortText
	}
	sStr, nStr, pStr := sort["sLocal"], sort["nLocal"], sort["pkgStr"]
	if sStr == "" || nStr == "" || pStr == "" {
		t.Fatalf("missing candidates: sLocal=%q nLocal=%q pkgStr=%q", sStr, nStr, pStr)
	}
	// Both locals stay in tierLocal (prefix "05"); the matching string local
	// carries the '0' match digit, the int local the '1'.
	if !strings.HasPrefix(sStr, "050") {
		t.Errorf("sLocal SortText = %q, want tierLocal matched prefix \"050\"", sStr)
	}
	if !strings.HasPrefix(nStr, "051") {
		t.Errorf("nLocal SortText = %q, want tierLocal unmatched prefix \"051\"", nStr)
	}
	if sStr >= nStr {
		t.Errorf("matched string local %q must sort before unmatched int local %q", sStr, nStr)
	}
	// Locality dominates: the unmatched int LOCAL still sorts before the matched
	// package-scope string (tierPackage "30").
	if nStr >= pStr {
		t.Errorf("int local %q must sort before matched package-scope string %q (locality dominates)", nStr, pStr)
	}
	if !strings.HasPrefix(pStr, "300") {
		t.Errorf("pkgStr SortText = %q, want tierPackage matched prefix \"300\"", pStr)
	}

	// No-expected-type: byte-identical to the historical tier-only form.
	plain := goCompletionItems(pkg, scope, nil, pos, false, nil, src, off, off, encUTF8, "")
	for _, it := range plain {
		if it.Label == "sLocal" && it.SortText != "05sLocal" {
			t.Errorf("no-expected sLocal SortText = %q, want byte-identical \"05sLocal\"", it.SortText)
		}
	}
}
