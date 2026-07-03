package parser

import (
	"go/token"
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
		case *ast.ClassPart:
			if v.CF == nil && v.CSSSegments == nil {
				seen["classpart"]++
				check("ClassPart.ExprPos", v.ExprPos, v.Expr)
				check("ClassPart.CondPos", v.CondPos, v.Cond)
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

// TestSpreadPipelineParenUnwrapExprPos pins that a parenthesized spread
// pipeline — whose seed is rewritten away from the source bytes — records
// NoPos rather than a misaligned position, while a bare piped spread keeps the
// seed position.
func TestSpreadPipelineParenUnwrapExprPos(t *testing.T) {
	parse := func(body string) *ast.SpreadAttr {
		t.Helper()
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
			t.Fatal("no SpreadAttr parsed")
		}
		return sa
	}

	if sa := parse("{ (bag |> trim)... }"); len(sa.Stages) == 0 {
		t.Error("parenthesized spread pipeline lost its stages")
	} else if sa.ExprPos.IsValid() {
		t.Error("paren-unwrapped spread pipeline has an ExprPos; seed no longer matches source bytes")
	}
	if sa := parse("{ bag... }"); !sa.ExprPos.IsValid() {
		t.Error("plain spread has no ExprPos")
	}
}
