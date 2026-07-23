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
