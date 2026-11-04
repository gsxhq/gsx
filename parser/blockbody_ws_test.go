package parser

import (
	"reflect"
	"strings"
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
// inside a control-flow body survives as literal content.
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
// The result must be nil, not a non-nil empty slice: a trimmed-to-empty body has
// to be structurally identical to a literally-empty `{}` body (which parses to
// nil), or AST-faithfulness comparisons — e.g. fmt idempotence — see a spurious
// nil-vs-empty diff.
func TestBlockBodyWhitespaceOnlyIsEmpty(t *testing.T) {
	then := firstIfThen(t, `package p
component X() { <div>{ if true {    } }</div> }
`)
	if then != nil {
		t.Fatalf("Then = %#v, want nil (structurally identical to an empty body)", then)
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
// case body survives (fixed via parseCaseBody lookahead-restore). Default-only;
// see TestSwitchCaseBodyTextDoesNotSwallowLabel (markup_test.go) for the
// multi-arm text-before-label case, fixed 2026-07-25.
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

// TestTrimBodyLayout exercises the helper directly.
func TestTrimBodyLayout(t *testing.T) {
	span := &ast.Element{Tag: "span"}
	sig := func(nodes []ast.Markup) string {
		var b strings.Builder
		for _, n := range nodes {
			switch v := n.(type) {
			case *ast.Text:
				b.WriteString("T(" + v.Value + ")")
			case *ast.Element:
				b.WriteString("<" + v.Tag + ">")
			}
		}
		return b.String()
	}
	cases := []struct {
		name string
		in   []ast.Markup
		want string
	}{
		{"trim both inline", []ast.Markup{&ast.Text{Value: " "}, span, &ast.Text{Value: " "}}, "<span>"},
		{"keep interior", []ast.Markup{span, &ast.Text{Value: " - "}, span}, "<span>T( - )<span>"},
		{"trim leading only, keep core", []ast.Markup{&ast.Text{Value: "  x"}}, "T(x)"},
		{"HTML ASCII whitespace only -> empty", []ast.Markup{&ast.Text{Value: " \t\r\n\f"}}, ""},
		{"non-ASCII whitespace is content", []ast.Markup{&ast.Text{Value: " \u00a0 "}}, "T(\u00a0)"},
		{"whitespace only -> empty", []ast.Markup{&ast.Text{Value: "  \n "}}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			nodes, _ := trimBodyLayout(c.in)
			got := sig(nodes)
			if got != c.want {
				t.Fatalf("trimBodyLayout = %q, want %q", got, c.want)
			}
		})
	}
}

// All brace-delimited markup bodies trim syntax edges and retain interior text.
func TestHTMLBodyWhitespaceAndSpans(t *testing.T) {
	for _, tc := range []struct{ name, src string }{
		{"component", "package p\ncomponent X() { \t\nfirst <i/> \t\n <b/> last\t\n }"},
		{"control", "package p\ncomponent X() { { if true { \t\nfirst <i/> \t\n <b/> last\t\n } } }"},
		{"slot", "package p\ncomponent X() { <C slot={ \t\n<i/> \t\n <b/> last\t\n }/> }"},
		{"switch", "package p\ncomponent X() { { switch x { case 1: \t\nfirst <i/> \t\n <b/> last\t\n } } }"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := parseStringT(t, tc.src)
			var body []ast.Markup
			ast.Inspect(f, func(n ast.Node) bool {
				switch n := n.(type) {
				case *ast.Component:
					if tc.name == "component" {
						body = n.Body
					}
				case *ast.IfMarkup:
					if tc.name == "control" {
						body = n.Then
					}
				case *ast.MarkupAttr:
					if tc.name == "slot" {
						body = n.Value
					}
				case *ast.CaseClause:
					if tc.name == "switch" {
						body = n.Body
					}
				}
				return true
			})
			if len(body) == 0 {
				t.Fatal("empty body")
			}
			var texts []string
			for _, n := range body {
				if text, ok := n.(*ast.Text); ok {
					texts = append(texts, text.Value)
					// Parse uses a token file whose base is one; spans must follow trimming.
					got := tc.src[int(text.Pos())-1 : int(text.End())-1]
					if got != text.Value {
						t.Fatalf("span spells %q, Text.Value %q", got, text.Value)
					}
				}
			}
			want := []string{"first ", " \t\n ", " last"}
			if tc.name == "slot" {
				want = want[1:]
			}
			if strings.Join(texts, "|") != strings.Join(want, "|") {
				t.Fatalf("texts = %q, want %q", texts, want)
			}
		})
	}
}

func TestBodyLayoutRetainsSyntacticEdges(t *testing.T) {
	src := "package p\ncomponent C() {\n \t{ if ok {\r\n\t\t<b>x</b>\n \t} else { \f<i>y</i>\t } }\n\t{ for ok {\n\t<b>x</b>\n } }\n\t{ switch x { case 1:\n\t<b>x</b>\n \tdefault: <i>y</i>\n } }\n\t<Card slot={\n  <b>x</b>\n }/>\n }\n"
	f := parseStringT(t, src)
	var got []ast.BodyLayout
	ast.Inspect(f.Decls[0], func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.Component:
			got = append(got, n.Layout)
		case *ast.IfMarkup:
			got = append(got, n.ThenLayout, n.ElseLayout)
		case *ast.ForMarkup:
			got = append(got, n.Layout)
		case *ast.CaseClause:
			got = append(got, n.Layout)
		case *ast.MarkupAttr:
			got = append(got, n.Layout)
		}
		return true
	})
	want := []ast.BodyLayout{
		{Leading: "\n \t", Trailing: "\n "},
		{Leading: "\r\n\t\t", Trailing: "\n \t"},
		{Leading: " \f", Trailing: "\t "},
		{Leading: "\n\t", Trailing: "\n "},
		{Leading: "\n\t", Trailing: "\n \t"},
		{Leading: " ", Trailing: "\n "},
		{Leading: "\n  ", Trailing: "\n "},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("layouts = %#v, want %#v", got, want)
	}
	clone := ast.CloneFile(f)
	if !reflect.DeepEqual(f, clone) {
		t.Fatal("clone changed layout metadata")
	}
}
