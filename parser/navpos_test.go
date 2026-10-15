package parser

import (
	"go/token"
	"strings"
	"testing"

	"github.com/gsxhq/gsx/ast"
)

// TestNavigationPositionsByteFaithful pins the invariant every LSP bridge
// relies on: for each recorded expression/clause position, the source bytes at
// that position spell exactly the stored (trimmed) text.
func TestNavigationPositionsByteFaithful(t *testing.T) {
	src := []byte(`package p

component C(user string, cond bool, n int, bag string) {
	<div
		class={
			user,
			"guard" : cond,
			if cond { user } else if !cond { "y" },
			switch n {
			case 1, 2: user
			default: "d"
			}
		}
		{ if cond {
			data-x={user}
		} else if !cond {
			data-y={user}
		} }
		{ bag... }
	>
		{ switch n {
		case 3:
			<b>x</b>
		} }
	</div>
}
`)
	fset := token.NewFileSet()
	f, err := ParseFile(fset, "c.gsx", src, 0)
	if err != nil {
		t.Fatal(err)
	}

	check := func(what string, pos token.Pos, text string) {
		t.Helper()
		if text == "" {
			return
		}
		if !pos.IsValid() {
			t.Errorf("%s: position not recorded for %q", what, text)
			return
		}
		off := fset.Position(pos).Offset
		if got := string(src[off : off+len(text)]); got != text {
			t.Errorf("%s: source at pos = %q, want %q", what, got, text)
		}
	}

	seen := map[string]int{}
	ast.Inspect(f, func(n ast.Node) bool {
		switch v := n.(type) {
		case *ast.ComposedPart:
			if v.CF == nil && v.LiteralSegments == nil {
				seen["classpart"]++
				check("ComposedPart.ExprPos", v.ExprPos, v.Expr)
				check("ComposedPart.CondPos", v.CondPos, v.Cond)
			}
		case *ast.ValueIf:
			seen["valueif"]++
			check("ValueIf.CondPos", v.CondPos, v.Cond)
		case *ast.ValueArm:
			seen["valuearm"]++
			check("ValueArm.ExprPos", v.ExprPos, v.Expr)
		case *ast.ValueSwitch:
			seen["valueswitch"]++
			check("ValueSwitch.TagPos", v.TagPos, v.Tag)
		case *ast.ValueSwitchCase:
			seen["valueswitchcase"]++
			check("ValueSwitchCase.ListPos", v.ListPos, v.List)
			if v.Default && v.ListPos.IsValid() {
				t.Error("default value-switch case has a ListPos")
			}
		case *ast.CondAttr:
			seen["condattr"]++
			check("CondAttr.CondPos", v.CondPos, v.Cond)
		case *ast.SpreadAttr:
			seen["spread"]++
			check("SpreadAttr.ExprPos", v.ExprPos, v.Expr)
		case *ast.SwitchMarkup:
			seen["switchmarkup"]++
			check("SwitchMarkup.TagPos", v.TagPos, v.Tag)
		case *ast.CaseClause:
			seen["caseclause"]++
			check("CaseClause.ListPos", v.ListPos, v.List)
		}
		return true
	})
	want := map[string]int{
		"classpart": 2, "valueif": 2, "valuearm": 4, "valueswitch": 1,
		"valueswitchcase": 2, "condattr": 2, "spread": 1, "switchmarkup": 1,
		"caseclause": 1,
	}
	for k, w := range want {
		if seen[k] != w {
			t.Errorf("parsed %d %s nodes, want %d", seen[k], k, w)
		}
	}
}

// TestSpreadPipelineParenUnwrapExprPos pins that a spread pipeline's seed and
// stages carry the positions of their own source bytes in both the bare and
// the canonical parenthesized form (whose unwrapped text is a slice of the
// source), so codegen anchors and LSP cursor mapping land on them.
func TestSpreadPipelineParenUnwrapExprPos(t *testing.T) {
	for _, body := range []string{
		"{ (bag |> trim(\"x\"))... }",
		"{ ( bag\n\t\t|> trim(\"x\") )... }",
		"{ bag |> trim(\"x\")... }",
		"{ bag... }",
	} {
		src := []byte("package p\n\ncomponent C(bag string) {\n\t<div " + body + ">x</div>\n}\n")
		fset := token.NewFileSet()
		f, err := ParseFile(fset, "c.gsx", src, 0)
		if err != nil {
			t.Fatal(err)
		}
		var sa *ast.SpreadAttr
		ast.Inspect(f, func(n ast.Node) bool {
			if s, ok := n.(*ast.SpreadAttr); ok {
				sa = s
			}
			return true
		})
		if sa == nil {
			t.Fatalf("%s: no SpreadAttr parsed", body)
		}
		at := func(pos token.Pos, want, what string) {
			t.Helper()
			if !pos.IsValid() {
				t.Errorf("%s: %s has no position", body, what)
				return
			}
			if off := fset.Position(pos).Offset; !strings.HasPrefix(string(src[off:]), want) {
				t.Errorf("%s: %s position points at %q, want %q", body, what, string(src[off:min(off+len(want), len(src))]), want)
			}
		}
		at(sa.ExprPos, strings.TrimSpace(sa.Expr), "seed")
		if strings.Contains(body, "trim") {
			if len(sa.Stages) != 1 {
				t.Fatalf("%s: got %d stages, want 1", body, len(sa.Stages))
			}
			at(sa.Stages[0].NamePos, "trim", "stage name")
			at(sa.Stages[0].ArgsPos, `"x"`, "stage args")
		}
	}
}

func TestComposedPartExpressionSpan(t *testing.T) {
	src := []byte(`package p

component C(cond bool) {
	<div
		class={
			"plain",
			button.Role() : cond,
			makeClass(
				"é",
			) |> upper,
			other() |> upper : cond,
		}
		style={
			css` + "`" + `color: red` + "`" + `,
			if cond { "display:block" } else { "display:none" },
		}
	></div>
}
`)
	fset := token.NewFileSet()
	f, err := ParseFile(fset, "span.gsx", src, 0)
	if err != nil {
		t.Fatal(err)
	}

	seen := map[string]bool{}
	ast.Inspect(f, func(n ast.Node) bool {
		part, ok := n.(*ast.ComposedPart)
		if !ok {
			return true
		}
		if part.LiteralSegments != nil {
			seen["css"] = true
			if got := part.ExprEnd(); got != token.NoPos {
				t.Errorf("CSS ExprEnd() = %d, want NoPos", got)
			}
			return true
		}
		if part.CF != nil {
			seen["value-form"] = true
			if got := part.ExprEnd(); got != token.NoPos {
				t.Errorf("value-form ExprEnd() = %d, want NoPos", got)
			}
			return true
		}

		start := fset.Position(part.ExprPos).Offset
		end := fset.Position(part.ExprEnd()).Offset
		if got := string(src[start:end]); got != part.Expr {
			t.Errorf("expression span = %q, want %q", got, part.Expr)
		}

		switch {
		case part.Cond != "" && len(part.Stages) > 0:
			seen["guarded-pipeline"] = true
		case part.Cond != "":
			seen["guarded"] = true
		case len(part.Stages) > 0:
			seen["pipeline"] = true
		default:
			seen["plain"] = true
		}
		if (part.Cond != "" || len(part.Stages) > 0) && part.End() <= part.ExprEnd() {
			t.Errorf("whole-node End() = %d, want beyond ExprEnd() = %d for Expr %q", part.End(), part.ExprEnd(), part.Expr)
		}
		if strings.Contains(part.Expr, "\n") {
			seen["multiline"] = true
		}
		if strings.Contains(part.Expr, "é") {
			seen["utf8"] = true
		}
		return true
	})

	for _, kind := range []string{
		"plain",
		"guarded",
		"pipeline",
		"guarded-pipeline",
		"multiline",
		"utf8",
		"css",
		"value-form",
	} {
		if !seen[kind] {
			t.Errorf("did not inspect a %s ComposedPart", kind)
		}
	}
}
