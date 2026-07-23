package lsp

import (
	"go/token"
	"strings"
	"testing"
)

func TestRepairAtCursor(t *testing.T) {
	// The § marker is the cursor; it is removed before calling.
	cases := []struct {
		name, src, wantPatch string
		wantParsed           bool
	}{
		{"valid buffer", "package p\n\ncomponent C() {\n\t<div>{ x§ }</div>\n}\n", "", true},
		{"trailing dot parses as gsx", "package p\n\ncomponent C() {\n\t<div>{ user.§ }</div>\n}\n", "", true},
		{"empty pipe stage", "package p\n\ncomponent C() {\n\t<div>{ x |> § }</div>\n}\n", "_", true},
		{"half-typed component tag", "package p\n\ncomponent C() {\n\t<div><Ca§</div>\n}\n", "/>", true},
		{"half-typed attr name", "package p\n\ncomponent C() {\n\t<div cl§\n}\n", "/>", true},
		{"bare tag trigger", "package p\n\ncomponent C() {\n\t<§\n}\n", "_/>", true},
		// Observed: unlike a bare `<`, the parser accepts a qualified tag with
		// a trailing dot and no member token (`<icon./>` parses clean, same as
		// the standalone "trailing dot parses as gsx" Go-expr case above), so
		// the plain `/>` patch already heals it and wins before `_/>` is ever
		// tried — the healed Tag stays "icon." (no injected "_").
		{"qualified tag trailing dot", "package p\n\ncomponent C() {\n\t<icon.§\n}\n", "/>", true},
		{"unclosed attr string", "package p\n\ncomponent C() {\n\t<div class=\"x§\n}\n", "\"/>", true},
		// Observed: `class=` demands a value; only `""/>` (an empty quoted
		// value + self-close) heals it with zero parser errors. The brief's
		// hypothesized `=""/>` was malformed (produced `class==""`); a bareword
		// value (`x/>`) is also rejected by gsx.
		{"dangling equals", "package p\n\ncomponent C() {\n\t<div class=§\n}\n", "\"\"/>", true},
		{"unclosed expr attr", "package p\n\ncomponent C() {\n\t<div class={x§\n}\n", "}/>", true},
		// Unclosed body interpolations: the no-autopair typing flow (no
		// closing `}` at all yet). "}" closes the brace; the trailing-dot
		// selector (an incomplete but valid Go expression to the gsx grammar)
		// and the bare/prefixed identifier both parse clean once the brace
		// closes — same shape as the already-closed cases above, just without
		// their trailing " }".
		{"unclosed member trailing dot", "package p\n\ncomponent C() {\n\t<div>{ strconv.§\n</div>\n}\n", "}", true},
		{"unclosed member trailing dot (local)", "package p\n\ncomponent C() {\n\t<div>{ user.§\n</div>\n}\n", "}", true},
		{"unclosed plain ident prefix", "package p\n\ncomponent C() {\n\t<div>{ us§\n</div>\n}\n", "}", true},
		// Unclosed empty pipe stage: a lone "}" right after `|> ` is rejected
		// as an empty pipeline stage, so the placeholder-identifier "_}" patch
		// is required (mirrors the closed-buffer "_" case above).
		{"unclosed empty pipe stage", "package p\n\ncomponent C() {\n\t<div>{ x |> §\n</div>\n}\n", "_}", true},
		// Unclosed `{{ }}` GoBlocks (no autopaired closing brace): the
		// statement-position sibling of the body-interp closers above. "}"
		// and "_}" never win here — a lone `}` (single brace) is never
		// enough to close a doubled `{{`, it only unbalances further — so
		// "_}}" is the first of the list that closes the block at all. It
		// wins over a hypothetical bare "}}" for every shape (not just
		// this one) because parseGoBlock's pre-check is a brace/string
		// balance scan, never a Go-syntax check — see the "_}}" doc
		// comment on completionPatches above for the full argument.
		{"unclosed GoBlock ident suffix", "package p\n\ncomponent C() {\n\t{{ user := Get§\n}\n", "_}}", true},
		// The bare-declaration shape: nothing follows `:=` at all, so
		// unlike the ident-suffix case above, the placeholder is load-
		// bearing for the EMBEDDED Go to parse, not just cosmetic — `x :=
		// }}` (bare "}}", hypothetically) is a Go syntax error (short var
		// decl with no RHS), which "_}}"'s placeholder avoids.
		{"unclosed GoBlock bare RHS", "package p\n\ncomponent C() {\n\t{{ x := §\n}\n", "_}}", true},
		{"unclosed GoBlock member dot", "package p\n\ncomponent C() {\n\t{{ x.§\n}\n", "_}}", true},
		{"unrepairable", "package p\n\ncomponent C() {\n\t<§<<%%\n}\n", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			off := strings.Index(tc.src, "§")
			text := strings.Replace(tc.src, "§", "", 1)
			r := repairAtCursor(token.NewFileSet(), "/tmp/x.gsx", text, off)
			if r.patch != tc.wantPatch {
				t.Fatalf("patch = %q, want %q", r.patch, tc.wantPatch)
			}
			if (r.parsed != nil) != tc.wantParsed {
				t.Fatalf("parsed = %v, want %v", r.parsed != nil, tc.wantParsed)
			}
		})
	}
}
