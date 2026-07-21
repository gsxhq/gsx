package lsp

import (
	"go/token"
	"strings"
	"testing"
)

// TestClassifyCompletionContext exercises every completionContextKind over §-marked
// fixtures. The § marks the cursor and is stripped before repair+classification.
// Control-flow fixtures use the REAL gsx syntax (`{ for … { … } }`), not the
// brief's guessed `{for}…{/for}` — verified against examples/50-loops.txtar.
func TestClassifyCompletionContext(t *testing.T) {
	cases := []struct {
		name, src string
		want      completionContextKind
	}{
		{"interp ident", "package p\n\ncomponent C(x string) {\n\t<div>{ x§ }</div>\n}\n", ctxGoExpr},
		{"interp trailing dot", "package p\n\ncomponent C(u U) {\n\t<div>{ u.§ }</div>\n}\n", ctxGoExpr},
		{"pipe stage", "package p\n\ncomponent C(x string) {\n\t<div>{ x |> up§ }</div>\n}\n", ctxPipeStage},
		{"pipe stage empty", "package p\n\ncomponent C(x string) {\n\t<div>{ x |> § }</div>\n}\n", ctxPipeStage},
		{"tag", "package p\n\ncomponent C() {\n\t<div><Ca§</div>\n}\n", ctxTag},
		{"html tag", "package p\n\ncomponent C() {\n\t<di§\n}\n", ctxTag},
		{"attr name", "package p\n\ncomponent C() {\n\t<div cl§\n}\n", ctxAttrName},
		{"attr value", "package p\n\ncomponent C() {\n\t<input type=\"§\"/>\n}\n", ctxAttrValue},
		{"attr value phantom", "package p\n\ncomponent C() {\n\t<div class=§\n}\n", ctxAttrValue},
		{"expr attr is go", "package p\n\ncomponent C(x string) {\n\t<div class={ x§ }/>\n}\n", ctxGoExpr},
		{"goblock", "package p\n\ncomponent C() {\n\t{{ x§ := 1 }}\n\t<div>{ x }</div>\n}\n", ctxGoExpr},
		{"gochunk", "package p\n\nfunc helper() string { return x§ }\n\ncomponent C() {\n\t<div/>\n}\n", ctxGoExpr},
		{"for clause", "package p\n\ncomponent C(xs []string) {\n\t{ for _, v := range x§s {\n\t\t<div>{ v }</div>\n\t} }\n}\n", ctxGoExpr},
		{"sig params", "package p\n\ncomponent C(u Us§) {\n\t<div/>\n}\n", ctxSigType},
		{"markup text none", "package p\n\ncomponent C() {\n\t<div>hel§lo</div>\n}\n", ctxNone},
		{"dotted tag qualifier", "package p\n\nimport \"example.com/app/ui\"\n\ncomponent C() {\n\t<ui.§/>\n}\n", ctxTag},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			off := strings.Index(tc.src, "§")
			text := strings.Replace(tc.src, "§", "", 1)
			r := repairAtCursor(token.NewFileSet(), "/tmp/x.gsx", text, off)
			if r.parsed == nil {
				t.Fatal("fixture did not parse/repair")
			}
			got := classifyCompletionContext(r, "/tmp/x.gsx", off)
			if got.kind != tc.want {
				t.Fatalf("kind = %v, want %v", got.kind, tc.want)
			}
		})
	}
}

// TestClassifyCompletionContextFields checks the auxiliary fields handlers consume.
func TestClassifyCompletionContextFields(t *testing.T) {
	classify := func(src string) (completionContext, int) {
		off := strings.Index(src, "§")
		text := strings.Replace(src, "§", "", 1)
		r := repairAtCursor(token.NewFileSet(), "/tmp/x.gsx", text, off)
		if r.parsed == nil {
			t.Fatalf("fixture did not parse/repair: %q", text)
		}
		return classifyCompletionContext(r, "/tmp/x.gsx", off), off
	}

	t.Run("dotted tag qualifier is pkg", func(t *testing.T) {
		got, _ := classify("package p\n\nimport \"example.com/app/ui\"\n\ncomponent C() {\n\t<ui.§/>\n}\n")
		if got.kind != ctxTag {
			t.Fatalf("kind = %v, want ctxTag", got.kind)
		}
		if got.qualifier != "ui" {
			t.Fatalf("qualifier = %q, want %q", got.qualifier, "ui")
		}
	})

	t.Run("plain tag has empty qualifier", func(t *testing.T) {
		got, _ := classify("package p\n\ncomponent C() {\n\t<div><Ca§</div>\n}\n")
		if got.qualifier != "" {
			t.Fatalf("qualifier = %q, want empty", got.qualifier)
		}
		if got.element == nil || got.element.Tag != "Ca" {
			t.Fatalf("element = %+v, want tag Ca", got.element)
		}
	})

	t.Run("attr value phantom flag set", func(t *testing.T) {
		got, _ := classify("package p\n\ncomponent C() {\n\t<div class=§\n}\n")
		if got.kind != ctxAttrValue {
			t.Fatalf("kind = %v, want ctxAttrValue", got.kind)
		}
		if !got.phantom {
			t.Fatal("phantom = false, want true (injected empty value)")
		}
		if got.attr == nil {
			t.Fatal("attr = nil, want the phantom StaticAttr")
		}
		if got.element == nil || got.element.Tag != "div" {
			t.Fatalf("element = %+v, want tag div", got.element)
		}
	})

	t.Run("authored attr value not phantom", func(t *testing.T) {
		got, _ := classify("package p\n\ncomponent C() {\n\t<input type=\"§\"/>\n}\n")
		if got.kind != ctxAttrValue {
			t.Fatalf("kind = %v, want ctxAttrValue", got.kind)
		}
		if got.phantom {
			t.Fatal("phantom = true, want false (real quotes typed)")
		}
		if got.attr == nil {
			t.Fatal("attr = nil, want the StaticAttr")
		}
	})

	t.Run("pipe stage phantom flag set", func(t *testing.T) {
		got, _ := classify("package p\n\ncomponent C(x string) {\n\t<div>{ x |> § }</div>\n}\n")
		if got.kind != ctxPipeStage {
			t.Fatalf("kind = %v, want ctxPipeStage", got.kind)
		}
		if !got.phantom {
			t.Fatal("phantom = false, want true (injected _ stage)")
		}
	})

	t.Run("pipe stage half-typed not phantom", func(t *testing.T) {
		got, _ := classify("package p\n\ncomponent C(x string) {\n\t<div>{ x |> up§ }</div>\n}\n")
		if got.phantom {
			t.Fatal("phantom = true, want false (real stage text)")
		}
	})

	t.Run("unrepairable is ctxNone", func(t *testing.T) {
		text := "package p\n\ncomponent C() {\n\t<<<%%\n}\n"
		r := repairAtCursor(token.NewFileSet(), "/tmp/x.gsx", text, len("package p\n\ncomponent C() {\n\t<"))
		if r.parsed != nil {
			t.Skip("fixture unexpectedly repaired")
		}
		got := classifyCompletionContext(r, "/tmp/x.gsx", 0)
		if got.kind != ctxNone {
			t.Fatalf("kind = %v, want ctxNone for nil parse", got.kind)
		}
	})
}
