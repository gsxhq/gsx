package parser

import (
	"go/token"
	"testing"

	"github.com/gsxhq/gsx/ast"
)

func parseValueSwitchAttr(t *testing.T, body string) *ast.ValueSwitch {
	t.Helper()
	src := "package p\ncomponent C(v int) {\n\t<div class={ switch v {\n" + body + "\n\t} }>x</div>\n}\n"
	f, err := ParseFile(token.NewFileSet(), "test.gsx", src, 0)
	if err != nil {
		t.Fatalf("parse value switch: %v", err)
	}
	var found *ast.ValueSwitch
	ast.Inspect(f, func(n ast.Node) bool {
		if s, ok := n.(*ast.ValueSwitch); ok {
			found = s
		}
		return true
	})
	if found == nil {
		t.Fatal("no value-form switch parsed")
	}
	return found
}

func TestValueSwitchParsesUnbracedCaseValues(t *testing.T) {
	s := parseValueSwitchAttr(t, `
	case 1:
		"green" |> upper
	case 2, 3:
		choose(map[int]string{2: "amber"}, v)
	case 4:
		func() string { switch v { case 4: return "nested" }; return "other" }()
	default:
		"gray"`)

	if len(s.Cases) != 4 {
		t.Fatalf("cases = %d, want 4", len(s.Cases))
	}
	if got := s.Cases[0].Value.Expr; got != `"green"` {
		t.Fatalf("case 1 expr = %q", got)
	}
	if len(s.Cases[0].Value.Stages) != 1 || s.Cases[0].Value.Stages[0].Name != "upper" {
		t.Fatalf("case 1 stages = %#v", s.Cases[0].Value.Stages)
	}
	if got := s.Cases[1].Value.Expr; got != `choose(map[int]string{2: "amber"}, v)` {
		t.Fatalf("case 2 expr = %q", got)
	}
	if got := s.Cases[2].Value.Expr; got != `func() string { switch v { case 4: return "nested" }; return "other" }()` {
		t.Fatalf("case 4 expr = %q", got)
	}
	if !s.Cases[3].Default || s.Cases[3].Value.Expr != `"gray"` {
		t.Fatalf("default = %#v", s.Cases[3])
	}
}

func TestValueSwitchAcceptsBracedCaseValue(t *testing.T) {
	s := parseValueSwitchAttr(t, `
	case 1:
		{ "green" |> upper }
	default:
		{ "gray" }`)

	if got := s.Cases[0].Value.Expr; got != `"green"` {
		t.Fatalf("case 1 expr = %q", got)
	}
	if len(s.Cases[0].Value.Stages) != 1 || s.Cases[0].Value.Stages[0].Name != "upper" {
		t.Fatalf("case 1 stages = %#v", s.Cases[0].Value.Stages)
	}
	if got := s.Cases[1].Value.Expr; got != `"gray"` {
		t.Fatalf("default expr = %q", got)
	}
}
