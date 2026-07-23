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
		// Empty-element body: the cursor sits AFTER the open tag's `>`, inside a
		// childless body — must be ctxNone, not an attribute-name popup (the
		// openTagEnd bound; regression for the End()-fallback over-match).
		{"empty element body none", "package p\n\ncomponent C() {\n\t<div>§</div>\n}\n", ctxNone},
		// The whitespace attr-name rule still fires with the cursor tucked against
		// the closing bracket, whether the element has children, is self-closing,
		// or has an empty body.
		{"attr name before close with child", "package p\n\ncomponent C() {\n\t<div §>x</div>\n}\n", ctxAttrName},
		{"attr name before selfclose", "package p\n\ncomponent C() {\n\t<div §/>\n}\n", ctxAttrName},
		{"attr name before close empty body", "package p\n\ncomponent C() {\n\t<div §></div>\n}\n", ctxAttrName},
		// A `>` inside a quoted attribute value does not end the tag, so a cursor
		// past it is still an attribute-name position.
		{"attr name after quoted gt", "package p\n\ncomponent C() {\n\t<div title=\"a>b\" §/>\n}\n", ctxAttrName},
		// Unclosed body interpolations (no autopaired closing `}`): healed via
		// the new "}"/"_}" repair patches, these classify exactly like their
		// already-closed counterparts above.
		{"unclosed interp trailing dot", "package p\n\ncomponent C(u U) {\n\t<div>{ u.§\n</div>\n}\n", ctxGoExpr},
		{"unclosed interp plain ident", "package p\n\ncomponent C(x string) {\n\t<div>{ x§\n</div>\n}\n", ctxGoExpr},
		{"unclosed pipe stage empty", "package p\n\ncomponent C(x string) {\n\t<div>{ x |> §\n</div>\n}\n", ctxPipeStage},
		// Unclosed `{{ }}` GoBlocks (no autopaired closing brace, healed via the
		// new "_}}" repair patch): a statement-position sibling of the unclosed
		// interp cases above, still classifying ctxGoExpr through the CtrlMap/
		// GoBlock nodeNavSpans span (completion_context.go's Rule 1+2 loop), same
		// as the already-closed "goblock" case.
		{"unclosed goblock ident suffix", "package p\n\ncomponent C() {\n\t{{ user := Get§\n}\n", ctxGoExpr},
		{"unclosed goblock bare rhs", "package p\n\ncomponent C() {\n\t{{ x := §\n}\n", ctxGoExpr},
		{"unclosed goblock member dot", "package p\n\ncomponent C() {\n\t{{ x.§\n}\n", ctxGoExpr},
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

	t.Run("unclosed pipe stage phantom flag set", func(t *testing.T) {
		// No autopaired closing `}` — healed via the "_}" repair patch, not
		// the closed-buffer "_" patch, but the injected `_` is just as much a
		// repair token as the closed case above.
		got, _ := classify("package p\n\ncomponent C(x string) {\n\t<div>{ x |> §\n</div>\n}\n")
		if got.kind != ctxPipeStage {
			t.Fatalf("kind = %v, want ctxPipeStage", got.kind)
		}
		if !got.phantom {
			t.Fatal("phantom = false, want true (injected _ stage via \"_}\" patch)")
		}
	})

	t.Run("pipe stage half-typed not phantom", func(t *testing.T) {
		got, _ := classify("package p\n\ncomponent C(x string) {\n\t<div>{ x |> up§ }</div>\n}\n")
		if got.phantom {
			t.Fatal("phantom = true, want false (real stage text)")
		}
	})

	t.Run("bare tag phantom flag set", func(t *testing.T) {
		// A completely untyped `<` heals via the last-tried "_/>" patch
		// (completion_repair.go); the injected "_" standing in for the
		// tag name is not authored, so phantom must be true.
		got, _ := classify("package p\n\ncomponent C() {\n\t<§\n}\n")
		if got.kind != ctxTag {
			t.Fatalf("kind = %v, want ctxTag", got.kind)
		}
		if !got.phantom {
			t.Fatal("phantom = false, want true (injected _ tag name)")
		}
		if got.qualifier != "" {
			t.Fatalf("qualifier = %q, want empty", got.qualifier)
		}
	})

	t.Run("qualified tag trailing dot via repair", func(t *testing.T) {
		// `<icon.` with nothing typed after the dot has no self-close either,
		// so it needs repair too — but unlike a bare `<`, the parser accepts a
		// qualified tag with a trailing dot and no member token, so the plain
		// "/>" patch heals it (see TestRepairAtCursor/qualified_tag_trailing_dot)
		// and the healed Tag stays "icon." verbatim — not phantom. The empty
		// member token after the dot must still classify as ctxTag with
		// qualifier "icon".
		got, _ := classify("package p\n\ncomponent C() {\n\t<icon.§\n}\n")
		if got.kind != ctxTag {
			t.Fatalf("kind = %v, want ctxTag", got.kind)
		}
		if got.qualifier != "icon" {
			t.Fatalf("qualifier = %q, want %q", got.qualifier, "icon")
		}
		if got.phantom {
			t.Fatal("phantom = true, want false (\"/>\" heals it — no injected tag/member name)")
		}
		if got.element == nil || got.element.Tag != "icon." {
			t.Fatalf("element = %+v, want tag \"icon.\"", got.element)
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

// TestOpenTagEndSkipsComments pins skipBraced's comment handling: a `}` or
// `>` inside a `//` or `/* */` comment nested in an ExprAttr's {expr} value
// must not be mistaken for the value's closing brace or the open tag's
// closing '>'. Without comment-aware skipping, the comment's own '}' closes
// the brace early and a stray '>' still inside the comment text then
// terminates the tag scan early too — both cases below regress to that wrong,
// earlier '>' if skipBraced stops recognizing comments.
func TestOpenTagEndSkipsComments(t *testing.T) {
	cases := []struct {
		name, src string
	}{
		{"block comment with brace and angle bracket", `<div title={ /* } > */ x }>`},
		{"line comment with brace and angle bracket", "<div title={ // } >\n\tx }>"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src := []byte(tc.src)
			want := strings.LastIndexByte(tc.src, '>')
			if want < 0 {
				t.Fatal("fixture has no '>'")
			}
			if got := openTagEnd(src, 0); got != want {
				t.Fatalf("openTagEnd = %d, want %d (the tag's real closing '>'); src=%q", got, want, tc.src)
			}
		})
	}
}
