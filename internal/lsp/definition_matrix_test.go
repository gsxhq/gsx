package lsp

import (
	"strings"
	"testing"

	gsxast "github.com/gsxhq/gsx/ast"
)

// matrixSrc exercises every Go-fragment cursor position the LSP navigates:
// interps, expr attrs, spread, ordered-attrs pair values, class plain parts,
// class guard conds (incl. on a css literal), value-form if conds and arms,
// value-form switch tag/case lists/arms, in-tag conditional-attribute conds
// (if and else-if), markup if/for/switch clauses, and {{ }} Go blocks.
const matrixSrc = `package page

import "github.com/gsxhq/gsx"

component Box(user string, cond bool, n int, m int) {
	{{ disabled := cond }}
	{{ bag := gsx.Attrs{{Key: "a", Value: "b"}} }}
	<div
		id={user}
		{ bag... }
		class={
			user,
			"guard": disabled,
			if disabled { user + "x" } else if cond { "y" },
			switch n {
			case m: user
			default: "d"
			}
		}
		style={
			css` + "`color: red`" + `: cond,
		}
		{ if disabled {
			data-x={user}
		} else if cond {
			data-y={user}
		} }
	>
		{ user }
		{ if disabled { <b>a</b> } }
		{ for i := 0; i < n; i++ { <i>{i}</i> } }
		{ switch n {
		case m:
			<u>c</u>
		} }
	</div>
	<Kid extra={{"data-o": user}}/>
}

component Kid(extra gsx.Attrs) {
	<span { extra... }>k</span>
}

component NavText(variant string) {
	<span class=` + "`badge-@{variant}`" + `>x</span>
}
`

// TestDefinitionMatrix asserts go-to-definition resolves the identifier under
// the cursor to its declaration for EVERY navigable Go-fragment position — the
// coverage matrix this feature set closed. Each case names a cursor (anchor
// substring + delta) and the expected declaration site (anchor + delta).
func TestDefinitionMatrix(t *testing.T) {
	src := matrixSrc
	pkg, path := analyzedLSPPackage(t, src)

	// Declaration anchors.
	paramUser := strings.Index(src, "user string")
	paramCond := strings.Index(src, "cond bool")
	paramN := strings.Index(src, "n int")
	paramM := strings.Index(src, "m int")
	localDisabled := strings.Index(src, "disabled := cond")
	localBag := strings.Index(src, "bag := gsx.Attrs")
	paramVariant := strings.Index(src, "variant string")

	lineCol := func(off int) (int, int) {
		return strings.Count(src[:off], "\n") + 1, off - strings.LastIndexByte(src[:off], '\n')
	}

	cases := []struct {
		name    string
		anchor  string
		delta   int
		declOff int
	}{
		{"interp text", "{ user }", 2, paramUser},
		{"expr-attr value", "id={user}", len("id={"), paramUser},
		{"spread expr", "{ bag... }", 2, localBag},
		{"ordered pair value", `extra={{"data-o": user}}`, len(`extra={{"data-o": `), paramUser},
		{"class plain part expr", "user,", 0, paramUser},
		{"class guard cond", `"guard": disabled`, len(`"guard": `), localDisabled},
		{"css literal guard cond", "`: cond,", len("`: "), paramCond},
		{"value-if cond", "if disabled { user", len("if "), localDisabled},
		{"value-else-if cond", "else if cond {", len("else if "), paramCond},
		{"value-if arm expr", `{ user + "x" }`, 2, paramUser},
		{"value-switch tag", "switch n {\n\t\t\tcase", len("switch "), paramN},
		{"value-switch case list", "case m: user", len("case "), paramM},
		{"value-switch arm", "case m: user", len("case m: "), paramUser},
		{"cond-attr cond", "{ if disabled {\n\t\t\tdata-x", len("{ if "), localDisabled},
		{"cond-attr else-if cond", "else if cond {\n\t\t\tdata-y", len("else if "), paramCond},
		{"cond-attr nested expr-attr", "data-x={user}", len("data-x={"), paramUser},
		{"if-markup cond", "{ if disabled { <b>", len("{ if "), localDisabled},
		{"for-markup clause", "i < n; i++", len("i < "), paramN},
		{"switch-markup tag", "{ switch n {", len("{ switch "), paramN},
		{"switch-markup case list", "case m:\n", len("case "), paramM},
		{"goblock code", "disabled := cond", len("disabled := "), paramCond},
		{"embedded-text hole interp", "@{variant}", len("@{"), paramVariant},
	}
	for _, tc := range cases {
		idx := strings.Index(src, tc.anchor)
		if idx < 0 {
			t.Errorf("%s: cursor anchor %q not found", tc.name, tc.anchor)
			continue
		}
		if tc.declOff < 0 {
			t.Errorf("%s: declaration anchor not found", tc.name)
			continue
		}
		dp, ok := exprDefinitionAt(pkg, path, idx+tc.delta)
		if !ok {
			t.Errorf("%s: no definition resolved", tc.name)
			continue
		}
		wantLine, wantCol := lineCol(tc.declOff)
		if !strings.HasSuffix(dp.Filename, ".gsx") || dp.Line != wantLine || dp.Column != wantCol {
			t.Errorf("%s: definition at %s:%d:%d, want .gsx %d:%d", tc.name, dp.Filename, dp.Line, dp.Column, wantLine, wantCol)
		}
	}
}

// TestHoverObjectMatrix asserts the ctrl-span hover bridge resolves the object
// (and its highlight span) for identifiers in CtrlMap-bridged positions — the
// spans hover previously ignored entirely.
func TestHoverObjectMatrix(t *testing.T) {
	src := matrixSrc
	pkg, path := analyzedLSPPackage(t, src)

	cases := []struct {
		name   string
		anchor string
		delta  int
		ident  string
	}{
		{"cond-attr cond", "{ if disabled {\n\t\t\tdata-x", len("{ if "), "disabled"},
		{"class guard cond", `"guard": disabled`, len(`"guard": `), "disabled"},
		{"value-if cond", "if disabled { user", len("if "), "disabled"},
		{"value-switch tag", "switch n {\n\t\t\tcase", len("switch "), "n"},
		{"if-markup cond", "{ if disabled { <b>", len("{ if "), "disabled"},
		{"for-markup clause", "i < n; i++", len("i < "), "n"},
	}
	for _, tc := range cases {
		idx := strings.Index(src, tc.anchor)
		if idx < 0 {
			t.Errorf("%s: cursor anchor %q not found", tc.name, tc.anchor)
			continue
		}
		off := idx + tc.delta
		node, exprPos := exprNodeAtOffset(pkg, path, off)
		if node == nil {
			t.Errorf("%s: no node recognized", tc.name)
			continue
		}
		if !isCtrlSpan(node, exprPos) {
			t.Errorf("%s: matched span of %T is not a ctrl span", tc.name, node)
			continue
		}
		obj, idStart, idLen, ok := ctrlObjectAt(pkg, node, exprPos, off)
		if !ok {
			t.Errorf("%s: hover object did not resolve", tc.name)
			continue
		}
		if obj.Name() != tc.ident {
			t.Errorf("%s: hover object = %q, want %q", tc.name, obj.Name(), tc.ident)
		}
		if got := src[idStart : idStart+idLen]; got != tc.ident {
			t.Errorf("%s: hover range = %q, want %q", tc.name, got, tc.ident)
		}
	}
}

// TestPipedClassPartCondStillCtrl pins the dispatch-order rule: a ClassPart
// whose EXPR carries a pipeline still resolves its `: cond` guard through the
// CtrlMap bridge (the pipeline path must not swallow the cond cursor). The
// GUARDED part's piped seed itself returns no definition — conditional part
// exprs carry no type harvest, so ExprMap has no entry to walk (a known
// limitation) — while an UNCONDITIONAL piped part's seed resolves through
// pipedTarget.
func TestPipedClassPartCondStillCtrl(t *testing.T) {
	src := "package page\n\ncomponent Box(user string, cond bool) {\n\t<div class={\n\t\tuser |> trim,\n\t\tuser |> upper: cond,\n\t}>x</div>\n}\n"
	pkg, path := analyzedLSPPackage(t, src)
	line := func(off int) int { return strings.Count(src[:off], "\n") + 1 }

	condOff := strings.Index(src, ": cond,") + len(": ")
	node, exprPos := exprNodeAtOffset(pkg, path, condOff)
	cp, ok := node.(*gsxast.ClassPart)
	if !ok {
		t.Fatalf("cond cursor matched %T, want *ClassPart", node)
	}
	if len(cp.Stages) == 0 {
		t.Fatal("test part lost its pipeline")
	}
	if !isCtrlSpan(node, exprPos) {
		t.Fatal("piped part's cond span not classified as ctrl")
	}
	dp, ok := exprDefinitionAt(pkg, path, condOff)
	if !ok {
		t.Fatal("no definition for piped part's guard cond")
	}
	if want := line(strings.Index(src, "cond bool")); dp.Line != want {
		t.Errorf("guard cond definition at line %d, want %d", dp.Line, want)
	}

	// Guarded piped seed: no ExprMap entry → graceful no-result, never a
	// misaligned jump.
	if dp, ok := exprDefinitionAt(pkg, path, strings.Index(src, "user |> upper")); ok {
		t.Errorf("guarded piped seed unexpectedly resolved to %d:%d", dp.Line, dp.Column)
	}

	// Unconditional piped seed resolves via pipedTarget.
	dp, ok = exprDefinitionAt(pkg, path, strings.Index(src, "user |> trim"))
	if !ok {
		t.Fatal("no definition for unconditional piped part's seed")
	}
	if want := line(strings.Index(src, "user string")); dp.Line != want {
		t.Errorf("piped seed definition at line %d, want %d", dp.Line, want)
	}
}
