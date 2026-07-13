package parser

import (
	"fmt"
	"go/scanner"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/gsxhq/gsx/internal/txtar"
)

// pipeSegments reconstructs splitPipe-style segments from a scanGoExpr result
// over the inner body src[from:close], using the recorded top-level pipe
// offsets. Returned segments are byte-identical to what splitPipe(inner) yields.
func pipeSegments(inner string, from int, pipes []int) []string {
	if len(pipes) == 0 {
		return []string{inner}
	}
	segs := make([]string, 0, len(pipes)+1)
	start := 0
	for _, p := range pipes {
		rel := p - from // offset of the '|' within inner
		segs = append(segs, inner[start:rel])
		start = rel + 2 // skip "|>"
	}
	return append(segs, inner[start:])
}

// relDelims maps absolute top-level delimiter offsets into inner-relative ones
// so they can be compared against composedDelims(inner).
func relDelims(offs []int, from int) []int {
	if len(offs) == 0 {
		return nil
	}
	out := make([]int, len(offs))
	for i, o := range offs {
		out[i] = o - from
	}
	return out
}

// tagOrBacktickFree reports whether s contains no '<' and no '`' — the same
// fast-path guard condition goDepth1End, splitPipe, and (with one more
// condition, see composedGuardPasses) composedDelims use in production
// (parser/boundary.go, parser/pipe.go) to decide whether their legacy,
// non-scanGoExpr byte/token walk is safe. Used below to classify a region as
// guard-passing (independently checkable against a frozen legacy reference,
// since production itself trusts the legacy walk there) or guard-failing
// (production delegates to scanGoExpr, so scanGoExpr is authoritative and a
// frozen-reference equality assertion is not guaranteed to hold — see
// TestScanGoExprCorpusDifferential's doc comment).
func tagOrBacktickFree(s string) bool {
	return strings.IndexByte(s, '<') < 0 && strings.IndexByte(s, '`') < 0
}

// composedGuardPasses is composedDelims's fast-path guard: tagOrBacktickFree
// plus the additional ":=" exclusion composedDelims needs (a value-form
// if/switch's `;`-separated init inside class=/style= — see
// TestScanGoExprValueFormInitDivergence).
func composedGuardPasses(s string) bool {
	return tagOrBacktickFree(s) && !strings.Contains(s, ":=")
}

// oldGoDepth1End is a FROZEN, test-only copy of goDepth1End's pre-Task-2 byte
// loop (parser/boundary.go, as it existed before commit 9b2e816). It must
// NEVER be changed to delegate to scanGoExpr — its entire purpose is to stay
// an independent reference implementation so the differential tests below can
// prove scanGoExpr agrees with something other than itself. It reuses the
// LIVE lexical helpers (skipGSXEmbeddedLiteral, skipQuotedOrComment, etc.)
// from boundary.go — none of which were touched by Task 2's reroute, but
// skipGSXEmbeddedLiteral itself was made hole-aware (and dquote-prefix-aware)
// by Task 3. That's deliberate, not a leak: those lexical primitives are
// shared, not frozen, so oldGoDepth1End and production scanGoExpr both pick
// up Task 3's fix identically, and the differential stays meaningful — only
// the STRUCTURAL decision (byte loop vs. delegate-to-scanGoExpr) is frozen
// here, not the escape/hole-scanning rules underneath it.
//
// It is exact only on "guard-passing" input (no '<', no '`' anywhere in
// src[from:], mirroring goDepth1End's real guard) — see
// TestScanGoExprCorpusDifferential's doc comment for the guard-failing
// patterns where this frozen loop is known to diverge from scanGoExpr.
func oldGoDepth1End(src string, from int) (int, bool) {
	depth := 1
	for i := from; i < len(src); {
		if end, ok := skipGSXEmbeddedLiteral(src, i); ok {
			i = end
			continue
		}
		if end, ok := skipQuotedOrComment(src, i); ok {
			i = end
			continue
		}
		switch src[i] {
		case '{', '(', '[':
			depth++
		case '}', ')', ']':
			depth--
			if depth == 0 && src[i] == '}' {
				return i, true
			}
		}
		i++
	}
	return 0, false
}

// oldComposedDelims is a FROZEN, test-only copy of composedDelims's
// pre-Task-2 byte loop. See oldGoDepth1End's doc comment: same independence
// requirement, same "exact only on guard-passing input" caveat (here,
// composedGuardPasses rather than tagOrBacktickFree, since composedDelims's
// real guard also excludes ":=").
func oldComposedDelims(src string) (commas, colons []int) {
	depth := 0
	for i := 0; i < len(src); {
		if end, ok := skipGSXEmbeddedLiteral(src, i); ok {
			i = end
			continue
		}
		if end, ok := skipQuotedOrComment(src, i); ok {
			i = end
			continue
		}
		switch src[i] {
		case '{', '(', '[':
			depth++
		case '}', ')', ']':
			depth--
		case ',':
			if depth == 0 {
				commas = append(commas, i)
			}
		case ':':
			if depth == 0 {
				colons = append(colons, i)
			}
		}
		i++
	}
	return commas, colons
}

// oldSplitPipe is a FROZEN, test-only copy of splitPipe's pre-Task-2
// go/scanner-based walk (parser/pipe.go, as it existed before commit
// 9b2e816). Same independence requirement as oldGoDepth1End. It reuses
// splitPipeSegments — an unchanged, purely mechanical "offsets → segments"
// builder that does not itself scan or delegate to scanGoExpr — to build its
// result, exactly as the pre-Task-2 splitPipe did inline.
//
// Exact only on guard-passing input (tagOrBacktickFree) — see
// TestScanGoExprCorpusDifferential's doc comment.
func oldSplitPipe(src string) []string {
	if !strings.Contains(src, "|>") {
		return []string{src}
	}
	fset := token.NewFileSet()
	file := fset.AddFile("", fset.Base(), len(src))
	var s scanner.Scanner
	s.Init(file, []byte(src), nil, scanner.ScanComments)

	var splits []int // byte offset of each '|' that begins a top-level '|>'
	depth := 0
	prevTok := token.ILLEGAL
	prevOff := -1
	for {
		pos, tok, _ := s.Scan()
		if tok == token.EOF {
			break
		}
		off := file.Offset(pos)
		switch tok {
		case token.LPAREN, token.LBRACK, token.LBRACE:
			depth++
		case token.RPAREN, token.RBRACK, token.RBRACE:
			depth--
		case token.GTR:
			if depth == 0 && prevTok == token.OR && off == prevOff+1 {
				splits = append(splits, prevOff)
			}
		}
		prevTok = tok
		prevOff = off
	}
	return splitPipeSegments(src, splits)
}

func TestScanGoExprMatchesLegacy(t *testing.T) {
	cases := []string{
		`x }`,
		`f(a, b) }`,
		`Foo{A: 1, B: 2} }`,
		`"has } and |> inside" }`,
		"`raw @{x}` }",
		"js`a\\`b @{y}` }",
		`a < b }`,
		`<-ch }`,
		`items |> render |> join(",") }`,
		`m{"k": v} }`,
		`wrap(inner) }`,
		`x |> f |> g }`,
		`a, b: c, d }`,
		`css` + "`c:@{x}`" + ` }`,
		`(seed |> f)... }`,
	}
	// Compared against the FROZEN oldGoDepth1End/oldSplitPipe/oldComposedDelims
	// reference implementations, not the production goDepth1End/splitPipe/
	// composedDelims — those now delegate straight to scanGoExpr on
	// tag/backtick/':='-containing input (Task 2's reroute), which would make
	// several cases below (the backtick and '<' ones, deliberately included to
	// probe scanGoExpr's tag/backtick disambiguation) compare scanGoExpr to
	// itself. None of this table's cases hit the three constructs where the
	// frozen loops are known to diverge from scanGoExpr (a ':=' short-var-decl
	// init, a bare backtick raw string ending in a backslash right before its
	// close, or element text carrying Go-significant bytes — see
	// TestScanGoExprCorpusDifferential's doc comment), so blanket equality
	// against the frozen references is the correct check for every case here.
	for _, c := range cases {
		got := scanGoExpr(c, 0)

		wantClose, ok := oldGoDepth1End(c, 0)
		if !ok {
			wantClose = -1
		}
		if got.Close != wantClose {
			t.Errorf("Close(%q) = %d, want %d", c, got.Close, wantClose)
			continue
		}
		if got.Close < 0 {
			continue
		}
		inner := c[:got.Close]

		// Pipes: reconstruct segments and compare to the frozen splitPipe walk
		// over the inner.
		gotSegs := pipeSegments(inner, 0, got.Pipes)
		wantSegs := oldSplitPipe(inner)
		if !reflect.DeepEqual(gotSegs, wantSegs) {
			t.Errorf("pipe segments(%q) = %q, want %q", c, gotSegs, wantSegs)
		}

		// Commas/Colons vs the frozen composedDelims walk.
		wantCommas, wantColons := oldComposedDelims(inner)
		if !reflect.DeepEqual(relDelims(got.Commas, 0), wantCommas) {
			t.Errorf("commas(%q) = %v, want %v", c, relDelims(got.Commas, 0), wantCommas)
		}
		if !reflect.DeepEqual(relDelims(got.Colons, 0), wantColons) {
			t.Errorf("colons(%q) = %v, want %v", c, relDelims(got.Colons, 0), wantColons)
		}
	}
}

func TestScanGoExprBacktickSpan(t *testing.T) {
	// A BARE backtick is a plain Go raw string — interpolation is opt-in behind a
	// js`/css`/f` prefix — so scanGoExpr records NO gsx span for it and lets
	// go/scanner's raw-string tokenization stand. (A `\` before the close is a
	// literal backslash in a Go raw string; the string ends at the next `.)
	//   src = `hello` }   →  0:` … 6:` 7:sp 8:}
	{
		const src = "`hello` }"
		got := scanGoExpr(src, 0)
		if len(got.Backticks) != 0 {
			t.Fatalf("bare backtick: got %d gsx spans, want 0 (plain Go raw string)", len(got.Backticks))
		}
		if got.Close != 8 {
			t.Fatalf("bare backtick: Close = %d, want 8", got.Close)
		}
	}

	// A js`/css` PREFIXED literal IS a gsx escape-aware span: go/scanner would end
	// the raw string at the escaped backtick, so scanGoExpr takes over the WHOLE
	// gsx literal (through the gsx-escaped closing `), and the span covers the
	// prefix.
	const jssrc = "js`save(\\`hi @{n}\\`)` }"
	// 0:j 1:s 2:` ... escaped backticks inside ... closing ` then space then }
	g2 := scanGoExpr(jssrc, 0)
	if len(g2.Backticks) != 1 {
		t.Fatalf("js literal: got %d backtick spans, want 1", len(g2.Backticks))
	}
	if g2.Backticks[0][0] != 0 {
		t.Errorf("js literal span start = %d, want 0 (includes js prefix)", g2.Backticks[0][0])
	}
	// The close is the final '}'. Compared against the FROZEN oldGoDepth1End,
	// not production goDepth1End — jssrc contains a backtick, so production
	// goDepth1End now delegates straight to scanGoExpr on this input (Task 2),
	// which would compare scanGoExpr to itself.
	if wantClose, ok := oldGoDepth1End(jssrc, 0); ok {
		// oldGoDepth1End is gsx-escape-aware for js`/css` via
		// skipGSXEmbeddedLiteral (an unchanged helper both scanners share), so
		// both must agree here.
		if g2.Close != wantClose {
			t.Errorf("js literal Close = %d, want %d", g2.Close, wantClose)
		}
	}
}

// TestScanGoExprValueFormInitDivergence documents and tests a divergence that
// existed between composedDelims (legacy, byte-based) and scanGoExpr
// (token-based) on a value-form `if`/`switch` with a `;`-separated init
// inside class={...}/style={...} — e.g.
//
//	<span class={ if x := f(); x { "a" } }>y</span>
//
// splitComposed (attrs.go) feeds the value-form segment's source to
// composedDelims looking for a depth-0 ':' guard colon. A pure byte scan
// with no token awareness can't distinguish the ':' of a `:=` DEFINE from a
// `: cond` guard colon: it would record the ':=' colon, and splitComposed
// would report a spurious "value-form if in class/style takes no `: cond`
// guard" error. This IS a real production path — composedDelims's only
// production source of a `:=` byte is exactly this construct (a value-form
// CF init), and before Task 2 it errored on it, always. (The claim that
// composedDelims "never sees a `:=` in production" — in an earlier draft of
// this package's docs — was false; this test is what corrects it.)
//
// Task 2 fixed this AT THE SOURCE: composedDelims's own fast-path guard
// (boundary.go) additionally checks for a ":=" substring and, when present,
// delegates to scanGoExpr instead of running its byte loop — so
// composedDelims(inner) no longer reproduces the bug either; it now agrees
// with scanGoExpr by construction for any ":="-containing input. scanGoExpr
// tokenizes with go/scanner, so ':=' lexes as a single DEFINE token and is
// correctly never recorded as a Colon. The result: the construct above went
// from "always rejected" to "accepted" — an approved, corpus-pinned behavior
// change (see internal/corpus/testdata/cases/goexpr-valueform-init/).
func TestScanGoExprValueFormInitDivergence(t *testing.T) {
	const inner = ` if x := f(); x { "a" } `
	defineColon := strings.Index(inner, ":=")
	if defineColon < 0 {
		t.Fatalf("test bug: %q has no \":=\"", inner)
	}

	// Post-Task-2, composedDelims(inner) itself routes ":="-containing input
	// through scanGoExpr (see composedDelims's guard), so it must agree with
	// a direct scanGoExpr call: no colon recorded for the ':=', not the
	// spurious one the legacy byte loop used to produce.
	gotCommas, gotColons := composedDelims(inner)
	if len(gotColons) != 0 {
		t.Fatalf("composedDelims(%q) colons = %v, want none — Task 2's composedDelims guard must route ':='-containing input to scanGoExpr, not the legacy byte loop, so it no longer spuriously records the ':=' colon at offset %d",
			inner, gotColons, defineColon)
	}
	if len(gotCommas) != 0 {
		t.Fatalf("composedDelims(%q) commas = %v, want none", inner, gotCommas)
	}

	got := scanGoExpr(inner+"}", 0)
	if got.Close != len(inner) {
		t.Fatalf("scanGoExpr(%q).Close = %d, want %d (the appended outer '}')", inner, got.Close, len(inner))
	}
	if len(got.Colons) != 0 {
		t.Errorf("scanGoExpr(%q).Colons = %v, want none — ':=' tokenizes as a single DEFINE token, not a Colon",
			inner, got.Colons)
	}
	if !reflect.DeepEqual(relDelims(got.Commas, 0), gotCommas) {
		t.Errorf("scanGoExpr(%q).Commas = %v, want %v (agrees with composedDelims)",
			inner, relDelims(got.Commas, 0), gotCommas)
	}

	// Guard-rejection survival: a REAL `: cond` guard colon (one that follows
	// the value-form's closing `}`, as in `if cond { "a" } : bad`, not one
	// embedded inside a `:=` init) must still be recorded by both scanners —
	// this input has no ":=", so composedDelims still takes its legacy byte
	// path, and must still agree with scanGoExpr.
	const guardInner = ` if cond { "a" } : bad `
	wantGuardCommas, wantGuardColons := composedDelims(guardInner)
	if len(wantGuardColons) != 1 {
		t.Fatalf("composedDelims(%q) colons = %v, want exactly one (the real guard colon) — guard setup assumption broken",
			guardInner, wantGuardColons)
	}
	gotGuard := scanGoExpr(guardInner+"}", 0)
	if gotGuard.Close != len(guardInner) {
		t.Fatalf("scanGoExpr(%q).Close = %d, want %d", guardInner, gotGuard.Close, len(guardInner))
	}
	if !reflect.DeepEqual(relDelims(gotGuard.Colons, 0), wantGuardColons) {
		t.Errorf("scanGoExpr(%q).Colons = %v, want %v — a real guard colon after the value-form body must still be seen (composedDelims and scanGoExpr AGREE here; only the ':=' case above previously diverged)",
			guardInner, relDelims(gotGuard.Colons, 0), wantGuardColons)
	}
	if !reflect.DeepEqual(relDelims(gotGuard.Commas, 0), wantGuardCommas) {
		t.Errorf("scanGoExpr(%q).Commas = %v, want %v", guardInner, relDelims(gotGuard.Commas, 0), wantGuardCommas)
	}
}

// TestScanGoExprOpaqueSpanTableCases adds deterministic, hand-written cases
// for the two opaque-span kinds scanGoExpr resumes past: an operand-position
// element/tag and a backtick literal. The corpus differential
// (TestScanGoExprCorpusDifferential) only smoke-counts these spans when it
// happens to find them (46 marks / 9 backticks) — it never asserts that their
// interior is actually ignored. These cases plant delimiter-shaped bytes
// (`>`, `,`, `:`, `}`, `|>`) INSIDE each opaque span and assert Close/Pipes/
// Commas/Colons never see them.
func TestScanGoExprOpaqueSpanTableCases(t *testing.T) {
	t.Run("tag interior", func(t *testing.T) {
		// tag1's quoted attribute holds a '>' and its text content holds a
		// ',' and a ':'; tag2's quoted attribute holds a '}'. The only
		// top-level comma is the one BETWEEN the two elements.
		const src = `<a href="x>y">a,b:c</a>, <br title="z}w"/>}`
		got := scanGoExpr(src, 0)

		if got.Close != len(src)-1 {
			t.Fatalf("Close = %d, want %d (the trailing '}')", got.Close, len(src)-1)
		}
		wantMark0 := strings.Index(src, "<a")
		wantMark1 := strings.Index(src, "<br")
		if len(got.Marks) != 2 || got.Marks[0].Off != wantMark0 || got.Marks[1].Off != wantMark1 {
			t.Fatalf("Marks = %v, want [{%d} {%d}] (one per element)", got.Marks, wantMark0, wantMark1)
		}
		wantComma := strings.Index(src, "</a>,") + len("</a>")
		if len(got.Commas) != 1 || got.Commas[0] != wantComma {
			t.Errorf("Commas = %v, want exactly [%d] (the comma between the elements) — the ',' inside tag1's text content must be invisible",
				got.Commas, wantComma)
		}
		if len(got.Colons) != 0 {
			t.Errorf("Colons = %v, want none — the ':' in tag1's text content is inside the opaque element span", got.Colons)
		}
	})

	backtickCase := func(name, src string) func(t *testing.T) {
		return func(t *testing.T) {
			got := scanGoExpr(src, 0)
			if got.Close != len(src)-1 {
				t.Fatalf("Close = %d, want %d (the trailing '}')", got.Close, len(src)-1)
			}
			wantSpan := [2]int{0, strings.LastIndex(src, "`") + 1}
			if len(got.Backticks) != 1 || got.Backticks[0] != wantSpan {
				t.Fatalf("Backticks = %v, want one %v covering the whole %s literal (incl. any lang prefix)", got.Backticks, wantSpan, name)
			}
			if len(got.Pipes) != 0 {
				t.Errorf("Pipes = %v, want none — the '|>' is inside the %s literal", got.Pipes, name)
			}
			if len(got.Commas) != 0 {
				t.Errorf("Commas = %v, want none — the ',' is inside the %s literal", got.Commas, name)
			}
		}
	}
	t.Run("bare backtick interior", func(t *testing.T) {
		// A bare backtick is a plain Go raw string, NOT a gsx span — but its
		// interior stays opaque anyway: go/scanner tokenizes the whole `…` as one
		// STRING, so the interior '}','|>',',' never reach the depth/pipe/comma
		// scans.
		const src = "`a}b|>c,d` }"
		got := scanGoExpr(src, 0)
		if got.Close != len(src)-1 {
			t.Fatalf("Close = %d, want %d (the trailing '}')", got.Close, len(src)-1)
		}
		if len(got.Backticks) != 0 {
			t.Errorf("Backticks = %v, want none (bare backtick is a Go raw string, not a gsx span)", got.Backticks)
		}
		if len(got.Pipes) != 0 || len(got.Commas) != 0 {
			t.Errorf("Pipes=%v Commas=%v, want none — interior is inside the Go raw string", got.Pipes, got.Commas)
		}
	})
	t.Run("js backtick interior", backtickCase("js`", "js`e}f|>g,h` }"))
	t.Run("css backtick interior", backtickCase("css`", "css`e}f|>g,h` }"))

	// The `"`-delimited escape-hatch forms (f"/js"/css") are taken over the same
	// way as the backtick forms: scanGoExpr must NOT trust go/scanner's STRING
	// tokenization (a `"` inside a hole, or a `\@{` invalid escape, would desync
	// it) and instead records a gsx span with the escape-aware `"`-end. A free
	// backtick inside the `"` literal (below) is literal content, not a Go raw
	// string, so its interior '}','|>',',' stay opaque.
	dquoteCase := func(name, src string) func(t *testing.T) {
		return func(t *testing.T) {
			got := scanGoExpr(src, 0)
			if got.Close != len(src)-1 {
				t.Fatalf("Close = %d, want %d (the trailing '}')", got.Close, len(src)-1)
			}
			wantSpan := [2]int{0, strings.LastIndex(src, `"`) + 1}
			if len(got.Backticks) != 1 || got.Backticks[0] != wantSpan {
				t.Fatalf("Backticks = %v, want one %v covering the whole %s literal (incl. lang prefix)", got.Backticks, wantSpan, name)
			}
			if len(got.Pipes) != 0 {
				t.Errorf("Pipes = %v, want none — the '|>' is inside the %s literal", got.Pipes, name)
			}
			if len(got.Commas) != 0 {
				t.Errorf("Commas = %v, want none — the ',' is inside the %s literal", got.Commas, name)
			}
		}
	}
	// Content carries a literal backtick (` "`+"`"+`z" `) to prove it is free
	// inside a `"` literal.
	t.Run("f dquote interior", dquoteCase(`f"`, "f\"e}f|>g,h"+"`"+"z\" }"))
	t.Run("js dquote interior", dquoteCase(`js"`, "js\"e}f|>g,h"+"`"+"z\" }"))
	t.Run("css dquote interior", dquoteCase(`css"`, "css\"e}f|>g,h"+"`"+"z\" }"))
	t.Run("bare dquote interior", func(t *testing.T) {
		// A bare `"…"` is a plain Go string, NOT a gsx span (interpolation is
		// opt-in behind the f/js/css prefix); its interior stays opaque via
		// go/scanner's own STRING tokenization.
		const src = `"a}b|>c,d" }`
		got := scanGoExpr(src, 0)
		if got.Close != len(src)-1 {
			t.Fatalf("Close = %d, want %d (the trailing '}')", got.Close, len(src)-1)
		}
		if len(got.Backticks) != 0 {
			t.Errorf("Backticks = %v, want none (bare \"…\" is a Go string, not a gsx span)", got.Backticks)
		}
		if len(got.Pipes) != 0 || len(got.Commas) != 0 {
			t.Errorf("Pipes=%v Commas=%v, want none — interior is inside the Go string", got.Pipes, got.Commas)
		}
	})
}

// TestScanGoExprCorpusDifferential is the risk-gate proof: for every
// interpolation source region the parser actually scans across the whole
// txtar corpus, scanGoExpr must agree byte-for-byte with the pre-Task-2
// legacy scanners on every region where equality is actually guaranteed to
// hold — i.e. every region where production's OWN fast-path guard (see
// goDepth1End/composedDelims/splitPipe's doc comments in boundary.go/pipe.go)
// would have trusted the legacy byte/token walk instead of delegating to
// scanGoExpr.
//
// Why not compare against production goDepth1End/splitPipe/composedDelims
// directly, as this test did before Task 2's reroute landed: those three
// functions now DELEGATE straight to scanGoExpr whenever a region contains
// '<', '`', or (composedDelims only) ':=' — exactly the regions this
// differential most needs to check, since that's where scanGoExpr's
// element/backtick-aware behavior actually differs from a plain byte walk.
// Comparing scanGoExpr's output to a function that internally calls
// scanGoExpr on that same input is a tautology: it always passes, and proves
// nothing. This was caught in review — see task-2-report.md.
//
// Fix: this differential instead compares scanGoExpr to oldGoDepth1End /
// oldSplitPipe / oldComposedDelims — FROZEN, test-only copies of the
// pre-Task-2 byte/token loops (above) that never delegate to scanGoExpr — and
// ONLY on regions where the corresponding production guard passes
// (tagOrBacktickFree / composedGuardPasses). That is the exact condition
// under which production itself considers the legacy walk trustworthy, so
// it's also the exact condition under which comparing to the frozen legacy
// walk is a meaningful, guaranteed-to-hold check.
//
// Guard-FAILING regions (containing '<', '`', or ':=') are deliberately NOT
// asserted against the frozen loops here — not because they're unimportant,
// but because the frozen loops are demonstrably WRONG on three specific
// guard-failing patterns, so a blanket equality assertion there would be
// either accidentally true (most guard-failing regions, e.g. a bare '<'
// comparison operator, don't actually hit a divergent pattern) or actively
// wrong (a real divergence) — and this loop can't tell which without
// re-implementing the divergence detection itself:
//  1. A ':=' short-var-decl init inside class=/style= (composedDelims's
//     byte loop can't distinguish a DEFINE token's ':' from a real ': cond'
//     guard colon) — covered by TestScanGoExprValueFormInitDivergence.
//  2. A bare (non-js/css) backtick raw string that itself ends in a
//     backslash immediately before the byte that would close it —
//     oldGoDepth1End's naive raw-string skip (Go semantics: no escaping)
//     diverges from scanGoExpr's uniform, gsx-escape-aware backtick handling
//     — see task-2-report.md's "Unanticipated but pre-flagged regression"
//     and parser/embedded_text_test.go's *Unterminated tests.
//  3. Operand-position element text carrying Go-significant bytes ('}', ')',
//     ',', ':') inside a quoted attribute value or text content —
//     oldGoDepth1End/oldComposedDelims have no concept of an element span and
//     would count those bytes; scanGoExpr correctly treats them as opaque —
//     covered by TestScanGoExprOpaqueSpanTableCases.
//
// Those three patterns are exhaustively pinned by their own deterministic
// tests instead, plus end-to-end corpus rendering (internal/corpus). This
// loop logs guard-passing vs. guard-failing counts (closeGuardPass etc.
// below) and fails outright if guard-passing coverage ever drops to zero —
// that would mean the frozen-loop comparison silently stopped checking
// anything.
//
// Extraction method (unchanged from before this fix): the corpus inputs are
// parsed with the real parser while two test-only choke-point observers
// record the exact regions each legacy scanner is asked to delimit:
//   - scanRegionObserver records every (src, from) passed to goDepth1End — the
//     single point under goExprEnd/goStagesEnd hit for every body/attr `{ … }`,
//     ordered-attrs `{{ … }}`, control-flow header, comment, spread, and
//     embedded-literal pipeline (including recursive calls inside element
//     spans).
//   - composedRegionObserver records every inner passed to composedDelims — the
//     class/style ordered-attr splitter.
func TestScanGoExprCorpusDifferential(t *testing.T) {
	root := filepath.Join("..", "internal", "corpus", "testdata", "cases")
	var paths []string
	err := filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() && strings.HasSuffix(p, ".txtar") {
			paths = append(paths, p)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk corpus: %v", err)
	}
	if len(paths) == 0 {
		t.Fatalf("no .txtar cases found under %s", root)
	}

	type region struct {
		src  string
		from int
	}

	closeSites := 0        // goDepth1End regions observed
	closeGuardPass := 0    // ...of those, guard-passing (checked against oldGoDepth1End)
	pipeSites := 0         // regions with a close, eligible for a Pipes comparison
	pipeGuardPass := 0     // ...of those, guard-passing (checked against oldSplitPipe)
	composedSites := 0     // composedDelims inputs observed
	composedGuardPass := 0 // ...of those, guard-passing (checked against oldComposedDelims)
	markCount := 0         // operand-position element marks encountered (opaque-span path)
	backtickCount := 0     // backtick literal spans encountered (opaque-span path)
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("read %s: %v", p, err)
		}
		arc := txtar.Parse(data)
		var input string
		for _, f := range arc.Files {
			if f.Name == "input.gsx" {
				input = string(f.Data)
				break
			}
		}
		if input == "" {
			continue
		}

		// Capture the exact regions each legacy scanner is handed.
		var regions []region
		var composed []string
		scanRegionObserver = func(src string, from int) {
			regions = append(regions, region{src: src, from: from})
		}
		composedRegionObserver = func(src string) {
			composed = append(composed, src)
		}
		fset := token.NewFileSet()
		_, _ = ParseFileWithClassifier(fset, "input.gsx", input, 0, nil)
		scanRegionObserver = nil
		composedRegionObserver = nil

		// Close (vs oldGoDepth1End, guard-passing regions only) and Pipes (vs
		// oldSplitPipe, guard-passing regions only).
		for _, r := range regions {
			closeSites++
			got := scanGoExpr(r.src, r.from)
			markCount += len(got.Marks)
			backtickCount += len(got.Backticks)

			if tagOrBacktickFree(r.src[r.from:]) {
				closeGuardPass++
				wantClose, ok := oldGoDepth1End(r.src, r.from)
				if !ok {
					wantClose = -1
				}
				if got.Close != wantClose {
					t.Errorf("%s: Close(from=%d) = %d, want %d\n  region=%s",
						p, r.from, got.Close, wantClose, snippet(r.src, r.from))
					continue
				}
			}
			if got.Close < 0 {
				continue // unterminated region; nothing further to compare
			}
			pipeSites++
			inner := r.src[r.from:got.Close]

			if tagOrBacktickFree(inner) {
				pipeGuardPass++
				gotSegs := pipeSegments(inner, r.from, got.Pipes)
				wantSegs := oldSplitPipe(inner)
				if !reflect.DeepEqual(gotSegs, wantSegs) {
					t.Errorf("%s: pipe segments(from=%d) = %q, want %q",
						p, r.from, gotSegs, wantSegs)
				}
			}
		}

		// Commas/Colons (vs oldComposedDelims, guard-passing inputs only) on
		// composedDelims's real inputs. scanGoExpr wants a `{ … }` framing, so
		// run it over the inner with a synthetic closing brace appended;
		// delimiter offsets are then directly inner-relative.
		for _, inner := range composed {
			composedSites++
			got := scanGoExpr(inner+"}", 0)
			if got.Close != len(inner) {
				t.Errorf("%s: composed Close = %d, want %d\n  inner=%q",
					p, got.Close, len(inner), inner)
				continue
			}
			if !composedGuardPasses(inner) {
				// Guard-failing (':=' and/or '<'/'`'): oldComposedDelims is
				// known-divergent on a ':=' init — see
				// TestScanGoExprValueFormInitDivergence. Not asserted here.
				continue
			}
			composedGuardPass++
			wantCommas, wantColons := oldComposedDelims(inner)
			if !reflect.DeepEqual(relDelims(got.Commas, 0), wantCommas) {
				t.Errorf("%s: composed commas = %v, want %v\n  inner=%q",
					p, relDelims(got.Commas, 0), wantCommas, inner)
			}
			if !reflect.DeepEqual(relDelims(got.Colons, 0), wantColons) {
				t.Errorf("%s: composed colons = %v, want %v\n  inner=%q",
					p, relDelims(got.Colons, 0), wantColons, inner)
			}
		}
	}

	t.Logf("corpus differential: %d input.gsx files; %d goDepth1End regions (%d guard-passing vs oldGoDepth1End), %d with a close (%d guard-passing vs oldSplitPipe), %d composedDelims inputs (%d guard-passing vs oldComposedDelims); opaque spans exercised: %d element marks, %d backtick literals",
		len(paths), closeSites, closeGuardPass, pipeSites, pipeGuardPass, composedSites, composedGuardPass, markCount, backtickCount)
	if closeSites == 0 {
		t.Fatal("no interpolation regions captured — observer never fired")
	}
	if closeGuardPass == 0 {
		t.Fatal("no guard-passing goDepth1End regions found — the independent oldGoDepth1End check never ran")
	}
	if markCount == 0 || backtickCount == 0 {
		t.Fatal("no element marks / backtick spans captured — opaque-span coverage lost")
	}
}

// snippet returns a short, printable view of a scanned region for error output.
func snippet(src string, from int) string {
	end := min(from+40, len(src))
	return fmt.Sprintf("%q", src[from:end])
}

// TestEmbeddedLiteralEndHoleAware pins skipGSXEmbeddedLiteral's (and, via it,
// embeddedLiteralEndHoleAware's) hole-aware behavior: a nested prefixed
// literal, or a plain Go raw string, or a further nested hole inside a `@{ }`
// hole must not be mistaken for the OUTER literal's own closing delimiter.
// This is the Task 3 fix — pre-fix, skipGSXEmbeddedLiteral scanned flat to
// the next unescaped delim byte, so a nested literal's opening backtick
// inside a hole terminated the outer literal early.
func TestEmbeddedLiteralEndHoleAware(t *testing.T) {
	cases := []struct {
		name string
		src  string // scan starts at the literal's own opening prefix (offset 0)
		want string // the full literal span the scan should cover, from src start
	}{
		{
			name: "nested backtick literal inside a hole must not terminate the outer",
			src:  "f`a @{ string(js`f(@{who})`) }` + x",
			want: "f`a @{ string(js`f(@{who})`) }`",
		},
		{
			name: "depth 2 with an inner hole",
			src:  "f`a @{ f`b @{who}` } c`",
			want: "f`a @{ f`b @{who}` } c`",
		},
		{
			name: "plain Go raw string inside a hole",
			src:  "f`a @{ len(`raw`) }`",
			want: "f`a @{ len(`raw`) }`",
		},
		{
			name: "dquote outer with backtick inner",
			src:  `f"a @{ string(js` + "`f()`" + `) }" + x`,
			want: `f"a @{ string(js` + "`f()`" + `) }"`,
		},
		{
			// Three levels deep: f` holds a hole whose Go expression is
			// itself an f` literal, whose own hole is a js` literal, whose
			// own hole is a plain identifier. Every level's delimiter must
			// resolve to its OWN matching close, not an inner sibling's.
			name: "depth 3 nesting",
			src:  "f`a@{f`b@{js`c@{d}`}`}` + x",
			want: "f`a@{f`b@{js`c@{d}`}`}`",
		},
		{
			// `\@{` is gsx's escaped-hole convention (see
			// embeddedAtBraceEscaped/parseEmbeddedSegments): a backslash
			// immediately before '@{' makes it literal text, not a hole
			// open. Diverges from a naive (non-escape-aware) hole scan: the
			// text after the escaped `@{` has no closing '}' before the
			// real closing backtick, so a scan that (wrongly) tried to
			// treat it as a hole would fail to find a matching '}' and fall
			// back to consuming the whole rest of src — this case proves
			// the escape check is load-bearing, not just cosmetic.
			name: "escaped @{ is literal text, not a hole",
			src:  "f`a \\@{ not a hole` + x",
			want: "f`a \\@{ not a hole`",
		},
		{
			// Unterminated nested literal (EOF reached mid-literal, inside
			// an unterminated hole): must gracefully consume to EOF rather
			// than hang or panic. Matches the pre-existing convention (see
			// the old flat embeddedLiteralEnd) that "no closing delim
			// found" still reports ok=true with end=len(src).
			name: "unterminated inner literal inside an unterminated hole",
			src:  "f`a @{ js`no close",
			want: "f`a @{ js`no close",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			end, ok := skipGSXEmbeddedLiteral(c.src, 0)
			if !ok {
				t.Fatalf("skipGSXEmbeddedLiteral(%q) ok = false, want true", c.src)
			}
			got := c.src[:min(end, len(c.src))]
			if got != c.want {
				t.Errorf("skipGSXEmbeddedLiteral(%q) = %q, want %q", c.src, got, c.want)
			}
		})
	}
}

// TestSkipGSXEmbeddedLiteralDquotePrefixes pins the second half of the Task 3
// fix: skipGSXEmbeddedLiteral previously only recognized the three backtick
// prefixes (js`/css`/f`) and returned (0, false) — "not a gsx literal here" —
// for the `"`-delimited escape-hatch forms (js"/css"/f"), even though they are
// parsed identically by parseEmbeddedAttrLiteral. Every caller of
// skipGSXEmbeddedLiteral must now treat a dquote-prefixed literal as an
// opaque gsx span too.
func TestSkipGSXEmbeddedLiteralDquotePrefixes(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want string
	}{
		{"f dquote", `f"a @{b}" + x`, `f"a @{b}"`},
		{"js dquote", `js"a @{b}" + x`, `js"a @{b}"`},
		{"css dquote", `css"a @{b}" + x`, `css"a @{b}"`},
		// A bare identifier ending in "js"/"css"/"f" immediately before a
		// '"' must NOT be mistaken for the prefix (hasIdentBoundary), same
		// rule as the backtick forms.
		{"not a prefix (ident boundary)", `xjs"plain go string"`, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			end, ok := skipGSXEmbeddedLiteral(c.src, 0)
			if c.want == "" {
				if ok {
					t.Fatalf("skipGSXEmbeddedLiteral(%q) = %d, true; want ok=false (not a gsx literal)", c.src, end)
				}
				return
			}
			if !ok {
				t.Fatalf("skipGSXEmbeddedLiteral(%q) ok = false, want true", c.src)
			}
			got := c.src[:min(end, len(c.src))]
			if got != c.want {
				t.Errorf("skipGSXEmbeddedLiteral(%q) = %q, want %q", c.src, got, c.want)
			}
		})
	}
}

// TestEmbeddedLiteralEndHoleAwareTermination is a fuzz-ish termination proof:
// a set of maximally-malformed inputs (unterminated holes, unterminated
// nested literals, holes nested at EOF) that could plausibly desync the
// mutual recursion between embeddedLiteralEndHoleAware and skipGSXEmbeddedLiteral
// if it were unsound. Every case must return promptly (the test's own
// execution completing is the proof — a real non-termination bug would hang
// the whole `go test` run) with end always in [0, len(src)].
func TestEmbeddedLiteralEndHoleAwareTermination(t *testing.T) {
	cases := []string{
		"f`",
		"f`@{",
		"f`@{js`",
		"f`@{js`@{",
		"f`@{js`@{css`",
		"f`@{js`@{css`@{f`@{js`@{css`@{f`",
		"f`a@{f`b@{f`c@{f`d@{f`e@{",
		"f`\\@{\\@{\\@{",
		`f"@{`,
		`f"@{js"@{css"@{`,
		"f`@{ ( ( ( ( (",
		"f`@{ } } } } }`",
	}
	for _, src := range cases {
		end, ok := skipGSXEmbeddedLiteral(src, 0)
		if !ok {
			continue // not recognized as a gsx literal at all; fine
		}
		if end < 0 || end > len(src) {
			t.Errorf("skipGSXEmbeddedLiteral(%q) = %d, out of range [0,%d]", src, end, len(src))
		}
	}
}
