package parser

import (
	"testing"

	"github.com/gsxhq/gsx/ast"
)

// firstIfThen parses src and returns the Then body of the first IfMarkup found
// anywhere in the last-declared component's body.
func firstIfThen(t *testing.T, src string) []ast.Markup {
	t.Helper()
	f := parseStringT(t, src)
	comp := f.Decls[len(f.Decls)-1].(*ast.Component)
	var found *ast.IfMarkup
	var walk func(nodes []ast.Markup)
	walk = func(nodes []ast.Markup) {
		for _, n := range nodes {
			switch v := n.(type) {
			case *ast.IfMarkup:
				if found == nil {
					found = v
				}
			case *ast.Element:
				walk(v.Children)
			}
		}
	}
	walk(comp.Body)
	if found == nil {
		t.Fatal("no IfMarkup found")
	}
	return found.Then
}

// TestBlockBodyPreservesInteriorWhitespace: the space run between two holes
// inside a control-flow body survives to wsnorm (this is the bug fix).
func TestBlockBodyPreservesInteriorWhitespace(t *testing.T) {
	then := firstIfThen(t, `package p
component X(a, b string) { <div>{ if true { {a} - {b} } }</div> }
`)
	if len(then) != 3 {
		t.Fatalf("Then has %d nodes, want 3: %#v", len(then), then)
	}
	txt, ok := then[1].(*ast.Text)
	if !ok || txt.Value != " - " {
		t.Fatalf("interior text = %#v, want *ast.Text %q", then[1], " - ")
	}
}

// TestBlockBodyTrimsInlineEdges: inline whitespace immediately inside the braces
// around a lone element is trimmed.
func TestBlockBodyTrimsInlineEdges(t *testing.T) {
	then := firstIfThen(t, `package p
component X() { <div>{ if true { <span/> } }</div> }
`)
	if len(then) != 1 {
		t.Fatalf("Then has %d nodes, want 1 (edges trimmed): %#v", len(then), then)
	}
	if _, ok := then[0].(*ast.Element); !ok {
		t.Fatalf("Then[0] = %#v, want *ast.Element", then[0])
	}
}

// TestBlockBodyWhitespaceOnlyIsEmpty: a whitespace-only body trims to nothing.
func TestBlockBodyWhitespaceOnlyIsEmpty(t *testing.T) {
	then := firstIfThen(t, `package p
component X() { <div>{ if true {    } }</div> }
`)
	if len(then) != 0 {
		t.Fatalf("Then has %d nodes, want 0: %#v", len(then), then)
	}
}

// TestBlockBodyEdgeTrimKeepsInteriorLeadingSpace: trailing edge trimmed, but the
// interior space after the hole is kept.
func TestBlockBodyEdgeTrimKeepsInteriorLeadingSpace(t *testing.T) {
	then := firstIfThen(t, `package p
component X(a string) { <div>{ if true { {a} - } }</div> }
`)
	if len(then) != 2 {
		t.Fatalf("Then has %d nodes, want 2: %#v", len(then), then)
	}
	txt, ok := then[1].(*ast.Text)
	if !ok || txt.Value != " -" {
		t.Fatalf("interior text = %#v, want *ast.Text %q (trailing edge trimmed)", then[1], " -")
	}
}

// firstSwitchCaseBody returns the Body of the first case clause of the first
// SwitchMarkup found in the last-declared component.
func firstSwitchCaseBody(t *testing.T, src string) []ast.Markup {
	t.Helper()
	f := parseStringT(t, src)
	comp := f.Decls[len(f.Decls)-1].(*ast.Component)
	var found *ast.SwitchMarkup
	var walk func(nodes []ast.Markup)
	walk = func(nodes []ast.Markup) {
		for _, n := range nodes {
			switch v := n.(type) {
			case *ast.SwitchMarkup:
				if found == nil {
					found = v
				}
			case *ast.Element:
				walk(v.Children)
			}
		}
	}
	walk(comp.Body)
	if found == nil || len(found.Cases) == 0 {
		t.Fatal("no SwitchMarkup/case found")
	}
	return found.Cases[0].Body
}

// TestSwitchCaseBodyPreservesInteriorWhitespace: interior whitespace in a switch
// case body survives (fixed via parseCaseBody lookahead-restore). Default-only
// avoids the pre-existing keyword-swallow limitation.
func TestSwitchCaseBodyPreservesInteriorWhitespace(t *testing.T) {
	body := firstSwitchCaseBody(t, `package p
component X(k string) { <div>{ switch k {
	default:
		hi - {k} - bye
	} }</div> }
`)
	if len(body) != 3 {
		t.Fatalf("case body has %d nodes, want 3: %#v", len(body), body)
	}
	lead, ok := body[0].(*ast.Text)
	if !ok || lead.Value != "hi - " {
		t.Fatalf("body[0] = %#v, want *ast.Text %q", body[0], "hi - ")
	}
	trail, ok := body[2].(*ast.Text)
	if !ok || trail.Value != " - bye" {
		t.Fatalf("body[2] = %#v, want *ast.Text %q (interior space kept, edge trimmed)", body[2], " - bye")
	}
}

// TestTrimBodyEdges exercises the helper directly.
func TestTrimBodyEdges(t *testing.T) {
	span := &ast.Element{Tag: "span"}
	sig := func(nodes []ast.Markup) string {
		s := ""
		for _, n := range nodes {
			switch v := n.(type) {
			case *ast.Text:
				s += "T(" + v.Value + ")"
			case *ast.Element:
				s += "<" + v.Tag + ">"
			}
		}
		return s
	}
	cases := []struct {
		name string
		in   []ast.Markup
		want string
	}{
		{"trim both inline", []ast.Markup{&ast.Text{Value: " "}, span, &ast.Text{Value: " "}}, "<span>"},
		{"keep interior", []ast.Markup{span, &ast.Text{Value: " - "}, span}, "<span>T( - )<span>"},
		{"trim leading only, keep core", []ast.Markup{&ast.Text{Value: "  x"}}, "T(x)"},
		{"whitespace only -> empty", []ast.Markup{&ast.Text{Value: "  \n "}}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := sig(trimBodyEdges(c.in))
			if got != c.want {
				t.Fatalf("trimBodyEdges = %q, want %q", got, c.want)
			}
		})
	}
}
