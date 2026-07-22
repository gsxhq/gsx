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
	got := componentAttrExpectedType(pkg, exprStartOff)
	if got == nil || got.String() != "string" {
		t.Errorf("componentAttrExpectedType = %v, want string", got)
	}
	// A non-matching offset derives nothing.
	if got := componentAttrExpectedType(pkg, exprStartOff+999); got != nil {
		t.Errorf("componentAttrExpectedType at wrong offset = %v, want nil", got)
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
	items := goCompletionItems(pkg, scope, nil, pos, false, str, src, off, off, encUTF8)
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
	plain := goCompletionItems(pkg, scope, nil, pos, false, nil, src, off, off, encUTF8)
	for _, it := range plain {
		if it.Label == "sLocal" && it.SortText != "05sLocal" {
			t.Errorf("no-expected sLocal SortText = %q, want byte-identical \"05sLocal\"", it.SortText)
		}
	}
}
