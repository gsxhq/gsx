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
//     compared only on these, because composedDelims is byte-based: on a general
//     interp body it would count the ':' of a Go `:=` as a delimiter, which it
//     never sees in production (it only ever runs on class/style values).
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
