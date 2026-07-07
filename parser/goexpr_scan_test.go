package parser

import (
	"fmt"
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
	for _, c := range cases {
		got := scanGoExpr(c, 0)

		wantClose, ok := goDepth1End(c, 0)
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

		// Pipes: reconstruct segments and compare to splitPipe over the inner.
		gotSegs := pipeSegments(inner, 0, got.Pipes)
		wantSegs := splitPipe(inner)
		if !reflect.DeepEqual(gotSegs, wantSegs) {
			t.Errorf("pipe segments(%q) = %q, want %q", c, gotSegs, wantSegs)
		}

		// Commas/Colons vs composedDelims.
		wantCommas, wantColons := composedDelims(inner)
		if !reflect.DeepEqual(relDelims(got.Commas, 0), wantCommas) {
			t.Errorf("commas(%q) = %v, want %v", c, relDelims(got.Commas, 0), wantCommas)
		}
		if !reflect.DeepEqual(relDelims(got.Colons, 0), wantColons) {
			t.Errorf("colons(%q) = %v, want %v", c, relDelims(got.Colons, 0), wantColons)
		}
	}
}

func TestScanGoExprBacktickSpan(t *testing.T) {
	// A gsx-escaped backtick: go/scanner would end the raw string at the escaped
	// backtick, but scanGoExpr must treat the WHOLE gsx literal (through the
	// gsx-escaped closing `) as one opaque span.
	//
	// src = `a\`b @{x}` }  (Go source below). Byte layout:
	//   0:`  1:a  2:\  3:`  4:b  5:sp  6:@  7:{  8:x  9:}  10:`  11:sp  12:}
	const src = "`a\\`b @{x}` }"
	got := scanGoExpr(src, 0)

	wantSpan := [2]int{0, 11} // opening ` at 0, one past the closing ` at 10
	if len(got.Backticks) != 1 || got.Backticks[0] != wantSpan {
		t.Fatalf("backtick span = %v, want one %v", got.Backticks, wantSpan)
	}
	if got.Close != 12 { // the '}' after the literal
		t.Fatalf("Close = %d, want 12", got.Close)
	}

	// js`/css` prefix inclusion: the span must cover the prefix.
	const jssrc = "js`save(\\`hi @{n}\\`)` }"
	// 0:j 1:s 2:` ... escaped backticks inside ... closing ` then space then }
	g2 := scanGoExpr(jssrc, 0)
	if len(g2.Backticks) != 1 {
		t.Fatalf("js literal: got %d backtick spans, want 1", len(g2.Backticks))
	}
	if g2.Backticks[0][0] != 0 {
		t.Errorf("js literal span start = %d, want 0 (includes js prefix)", g2.Backticks[0][0])
	}
	// The close is the final '}'.
	if wantClose, ok := goDepth1End(jssrc, 0); ok {
		// goDepth1End is gsx-escape-aware for js`/css` via skipGSXEmbeddedLiteral,
		// so both must agree here.
		if g2.Close != wantClose {
			t.Errorf("js literal Close = %d, want %d", g2.Close, wantClose)
		}
	}
}

// TestScanGoExprValueFormInitDivergence documents and tests an intentional,
// benign divergence between composedDelims (legacy, byte-based) and
// scanGoExpr (token-based) on a value-form `if`/`switch` with a `;`-separated
// init inside class={...}/style={...} — e.g.
//
//	<span class={ if x := f(); x { "a" } }>y</span>
//
// splitComposed (attrs.go) feeds the value-form segment's source to
// composedDelims looking for a depth-0 ':' guard colon. composedDelims is a
// raw byte scan with no token awareness, so it can't distinguish the ':' of a
// `:=` DEFINE from a `: cond` guard colon: it records the ':=' colon, and
// splitComposed reports a spurious "value-form if in class/style takes no
// `: cond` guard" error. This IS a real production path — composedDelims's
// only production source of a `:=` byte is exactly this construct (a
// value-form CF init), and today it errors on it, always. (The claim that
// composedDelims "never sees a `:=` in production" — in an earlier draft of
// this package's docs — was false; this test is what corrects it.)
//
// scanGoExpr tokenizes with go/scanner, so ':=' lexes as a single DEFINE
// token and is correctly never recorded as a Colon. scanGoExpr is the
// CORRECT party; composedDelims is the buggy one. The divergence is
// intentional and benign — spurious-error becomes accept — but it IS a
// user-visible behavior change: Task 2's reroute of composedDelims's callers
// onto scanGoExpr silently flips this construct from "always rejected" to
// "accepted", and must pin the newly-accepted behavior with a corpus case.
func TestScanGoExprValueFormInitDivergence(t *testing.T) {
	const inner = ` if x := f(); x { "a" } `

	wantCommas, wantColons := composedDelims(inner)
	defineColon := strings.Index(inner, ":=")
	if defineColon < 0 {
		t.Fatalf("test bug: %q has no \":=\"", inner)
	}
	if len(wantColons) != 1 || wantColons[0] != defineColon {
		t.Fatalf("composedDelims(%q) colons = %v, want exactly [%d] (the spurious ':' of ':=') — divergence setup assumption broken",
			inner, wantColons, defineColon)
	}
	if len(wantCommas) != 0 {
		t.Fatalf("composedDelims(%q) commas = %v, want none", inner, wantCommas)
	}

	got := scanGoExpr(inner+"}", 0)
	if got.Close != len(inner) {
		t.Fatalf("scanGoExpr(%q).Close = %d, want %d (the appended outer '}')", inner, got.Close, len(inner))
	}
	if len(got.Colons) != 0 {
		t.Errorf("scanGoExpr(%q).Colons = %v, want none — ':=' tokenizes as a single DEFINE token, not a Colon; this is the divergence under test: scanGoExpr correctly does NOT see a guard colon here, where composedDelims spuriously does",
			inner, got.Colons)
	}
	if !reflect.DeepEqual(relDelims(got.Commas, 0), wantCommas) {
		t.Errorf("scanGoExpr(%q).Commas = %v, want %v (agrees with composedDelims here — only Colons diverge)",
			inner, relDelims(got.Commas, 0), wantCommas)
	}

	// Guard-rejection survival: a REAL `: cond` guard colon (one that follows
	// the value-form's closing `}`, as in `if cond { "a" } : bad`, not one
	// embedded inside a `:=` init) must still be recorded by scanGoExpr, so
	// Task 2's reroute doesn't also lose the legitimate rejection alongside
	// the spurious one above.
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
		t.Errorf("scanGoExpr(%q).Colons = %v, want %v — a real guard colon after the value-form body must still be seen (composedDelims and scanGoExpr AGREE here; only the ':=' case above diverges)",
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
	t.Run("bare backtick interior", backtickCase("bare backtick", "`a}b|>c,d` }"))
	t.Run("js backtick interior", backtickCase("js`", "js`e}f|>g,h` }"))
	t.Run("css backtick interior", backtickCase("css`", "css`e}f|>g,h` }"))
}

// TestScanGoExprCorpusDifferential is the risk-gate proof: for every
// interpolation source region the parser actually scans across the whole txtar
// corpus, scanGoExpr must agree byte-for-byte with the legacy scanners
// (goDepth1End for Close, splitPipe for Pipes, composedDelims for Commas/Colons).
//
// Extraction method: the corpus inputs are parsed with the real parser while
// two test-only choke-point observers record the exact regions each legacy
// scanner is asked to delimit:
//   - scanRegionObserver records every (src, from) passed to goDepth1End — the
//     single point under goExprEnd/goStagesEnd hit for every body/attr `{ … }`,
//     ordered-attrs `{{ … }}`, control-flow header, comment, spread, and
//     embedded-literal pipeline (including recursive calls inside element
//     spans). Close (vs goDepth1End) and Pipes (vs splitPipe) are compared on
//     all of them.
//   - composedRegionObserver records every inner passed to composedDelims — the
//     class/style ordered-attr splitter. Commas/Colons (vs composedDelims) are
//     compared only on these, because composedDelims is byte-based: on a
//     general interp body it would count the ':' of a Go `:=` as a delimiter.
//     composedDelims's real inputs are NOT categorically free of `:=`: a
//     value-form if/switch with a `;`-separated init inside
//     class={...}/style={...} is a real composedDelims input containing one,
//     and scanGoExpr correctly disagrees with composedDelims on it (a known,
//     intentional, benign divergence — spurious-error becomes accept; see
//     TestScanGoExprValueFormInitDivergence). The current corpus simply
//     doesn't happen to contain that shape yet (no `if x := …; …` inside a
//     class/style value), so this loop's exact-match assertion below is not
//     contradicted today; Task 2 must add a corpus case exercising it.
//
// Each recorded region is exactly what Task 2's reroute will hand to scanGoExpr,
// so agreement here is the byte-identical guarantee.
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

	closeSites := 0    // goDepth1End regions (Close + Pipes compared)
	pipeSites := 0     // regions with a close, pipes/segments compared
	composedSites := 0 // composedDelims inputs (Commas/Colons compared)
	markCount := 0     // operand-position element marks encountered (opaque-span path)
	backtickCount := 0 // backtick literal spans encountered (opaque-span path)
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

		// Close (vs goDepth1End) and Pipes (vs splitPipe) on every scanned region.
		for _, r := range regions {
			closeSites++
			got := scanGoExpr(r.src, r.from)
			markCount += len(got.Marks)
			backtickCount += len(got.Backticks)

			wantClose, ok := goDepth1End(r.src, r.from)
			if !ok {
				wantClose = -1
			}
			if got.Close != wantClose {
				t.Errorf("%s: Close(from=%d) = %d, want %d\n  region=%s",
					p, r.from, got.Close, wantClose, snippet(r.src, r.from))
				continue
			}
			if got.Close < 0 {
				continue // unterminated region; nothing further to compare
			}
			pipeSites++
			inner := r.src[r.from:got.Close]

			gotSegs := pipeSegments(inner, r.from, got.Pipes)
			wantSegs := splitPipe(inner)
			if !reflect.DeepEqual(gotSegs, wantSegs) {
				t.Errorf("%s: pipe segments(from=%d) = %q, want %q",
					p, r.from, gotSegs, wantSegs)
			}
		}

		// Commas/Colons (vs composedDelims) on composedDelims's real inputs only.
		// scanGoExpr wants a `{ … }` framing, so run it over the inner with a
		// synthetic closing brace appended; delimiter offsets are then directly
		// inner-relative.
		for _, inner := range composed {
			composedSites++
			got := scanGoExpr(inner+"}", 0)
			if got.Close != len(inner) {
				t.Errorf("%s: composed Close = %d, want %d\n  inner=%q",
					p, got.Close, len(inner), inner)
				continue
			}
			wantCommas, wantColons := composedDelims(inner)
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

	t.Logf("corpus differential: %d input.gsx files; %d goDepth1End regions (Close), %d with a close (Pipes), %d composedDelims inputs (Commas/Colons); opaque spans exercised: %d element marks, %d backtick literals",
		len(paths), closeSites, pipeSites, composedSites, markCount, backtickCount)
	if closeSites == 0 {
		t.Fatal("no interpolation regions captured — observer never fired")
	}
}

// snippet returns a short, printable view of a scanned region for error output.
func snippet(src string, from int) string {
	end := from + 40
	if end > len(src) {
		end = len(src)
	}
	return fmt.Sprintf("%q", src[from:end])
}
