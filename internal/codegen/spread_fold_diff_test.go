package codegen

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/gsxhq/gsx"
	"github.com/gsxhq/gsx/internal/attrclass"
)

// spreadFoldMatrixSrc emits one views.gsx package containing the E0-E7 element
// shapes from docs/superpowers/specs/2026-07-12-multi-spread-merge-design.md's
// "Case dispatch" table (0/1/2/3 spreads x interposed static x conditional x
// class/style x href), all on the SAME tag ("a") so a single URL-classification
// computation (see urlSinkArgsForTag) applies to every reference render:
//
//	E0  0 spreads                              (compile-time literal fast path)
//	E1  1 top-level spread                     (current emitManualSpreadElement)
//	E2  1 spread + pre(overridable)/post(forced) statics
//	E3  1 lone cond-nested spread              (current inline `if c { Spread }`)
//	E4  >=2 adjacent spreads                    (fold: ConcatAttrs(a, b))
//	E5  >=2 spreads + interposed static, COLLIDING key (data-k) to pin position-based last-wins
//	E6  >=2 spreads, second conditional        (fold: ConcatAttrs(a, AttrsCond(c, ->b)))
//	E7  >=2 spreads + interposed conditional static
//	E8  1 spread + root class + Form-2 conditional class (the D3-lift shape:
//	    { if c { class="on8" } else { class="off8" } } on a forwarding element)
//	E9  0 spreads, root style + conditional style   (no-spread same-name fold)
//	E10 0 spreads, root class + if/else class       (no-spread same-name fold)
//	E11 0 spreads, LONE if/else class — branches are mutually exclusive, so the
//	    contributor count max-combines to 1 and the element stays on the inline
//	    per-branch path; the matrix proves that path matches the fold reference
//
// E1 and E4 additionally carry a "class"/"href" pair (E1 with a javascript:
// value) so the differential also exercises class aggregation and leaf URL
// sanitization, not just plain scalars.
const spreadFoldMatrixSrc = `package views

import "github.com/gsxhq/gsx"

component E0() {
	<a id="e0" class="c0" style="color:red" href="/e0">e0</a>
}

component E1() {
	{{ a := gsx.Attrs{{Key: "id", Value: "e1"}, {Key: "class", Value: "c1"}, {Key: "href", Value: "javascript:alert(1)"}} }}
	<a { a... }>e1</a>
}

component E2() {
	{{ a := gsx.Attrs{{Key: "id", Value: "e2-a"}, {Key: "data-mid", Value: "m"}} }}
	<a id="e2-pre" { a... } data-post="p">e2</a>
}

component E3(c bool) {
	{{ a := gsx.Attrs{{Key: "class", Value: "c3"}, {Key: "href", Value: "/e3"}} }}
	<a { if c { { a... } } }>e3</a>
}

component E4() {
	{{ a := gsx.Attrs{{Key: "class", Value: "a4"}, {Key: "data-k", Value: "1"}, {Key: "href", Value: "/e4-a"}} }}
	{{ b := gsx.Attrs{{Key: "class", Value: "b4"}, {Key: "data-k", Value: "2"}} }}
	<a { a... } { b... }>e4</a>
}

component E5() {
	{{ a := gsx.Attrs{{Key: "data-k", Value: "a5"}, {Key: "style", Value: "color:red"}} }}
	{{ b := gsx.Attrs{{Key: "data-k", Value: "b5"}, {Key: "style", Value: "margin:0"}} }}
	<a { a... } data-k="mid5" { b... }>e5</a>
}

component E6(c bool) {
	{{ a := gsx.Attrs{{Key: "data-k", Value: "a6"}, {Key: "class", Value: "x6"}} }}
	{{ b := gsx.Attrs{{Key: "data-k", Value: "b6"}, {Key: "class", Value: "y6"}} }}
	<a { a... } { if c { { b... } } }>e6</a>
}

component E7(c bool) {
	{{ a := gsx.Attrs{{Key: "data-k", Value: "a7"}} }}
	{{ b := gsx.Attrs{{Key: "data-k", Value: "b7"}} }}
	<a { a... } { if c { data-mid="m7" } } { b... }>e7</a>
}

component E8(c bool) {
	{{ a := gsx.Attrs{{Key: "data-k", Value: "a8"}, {Key: "class", Value: "sp8"}} }}
	<a class="base8" { a... } { if c { class="on8" } else { class="off8" } }>e8</a>
}

component E9(c bool) {
	<a id="e9" style="color:red" { if c { style="margin:0" } }>e9</a>
}

component E10(c bool) {
	<a class="base10" { if c { class="on10" } else { class="off10" } }>e10</a>
}

component E11(c bool) {
	<a { if c { class="on11" } else { class="off11" } }>e11</a>
}
`

const spreadFoldMarkerPrefix = "\x00SFCASE "
const spreadFoldMarkerSuffix = "\x00\n"

// runSpreadFoldMatrix generates spreadFoldMatrixSrc ONCE (one packages.Load,
// per the corpus batchCodegen pattern this file deliberately does NOT loop
// testing.F over), builds ONE harness binary that renders every scenario, and
// returns each scenario's rendered HTML keyed by name.
func runSpreadFoldMatrix(t *testing.T) map[string]string {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping go-run render test in -short mode")
	}
	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	tmp := t.TempDir()
	writeMultiFile(t, tmp, "go.mod", "module gsxspreadfold\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")

	viewsDir := filepath.Join(tmp, "views")
	if err := os.MkdirAll(viewsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeMultiFile(t, viewsDir, "views.gsx", spreadFoldMatrixSrc)

	genRes, err := GenerateDirs(tmp, []string{viewsDir}, Options{}, nil)
	if err != nil {
		t.Fatalf("GenerateDirs: %v", err)
	}
	dr := genRes[viewsDir]
	if hasDiagErrors(dr.Diags) {
		t.Fatalf("GenerateDirs: unexpected errors: %v", dr.Diags)
	}
	for gsxPath, src := range dr.Files {
		base := strings.TrimSuffix(filepath.Base(gsxPath), ".gsx")
		writeMultiFile(t, viewsDir, base+".x.go", string(src))
	}

	writeMultiFile(t, tmp, "main.go", `package main

import (
	"bytes"
	"context"
	"fmt"
	"io"

	p "gsxspreadfold/views"
)

func render(ctx context.Context, name string, n interface{ Render(context.Context, io.Writer) error }) {
	var buf bytes.Buffer
	if err := n.Render(ctx, &buf); err != nil {
		fmt.Fprintf(&buf, "[render error] %v", err)
	}
	fmt.Print(`+strconv.Quote(spreadFoldMarkerPrefix)+` + name + `+strconv.Quote(spreadFoldMarkerSuffix)+` + buf.String())
}

func main() {
	ctx := context.Background()
	render(ctx, "E0", p.E0())
	render(ctx, "E1", p.E1())
	render(ctx, "E2", p.E2())
	render(ctx, "E3true", p.E3(p.E3Props{C: true}))
	render(ctx, "E3false", p.E3(p.E3Props{C: false}))
	render(ctx, "E4", p.E4())
	render(ctx, "E5", p.E5())
	render(ctx, "E6true", p.E6(p.E6Props{C: true}))
	render(ctx, "E6false", p.E6(p.E6Props{C: false}))
	render(ctx, "E7true", p.E7(p.E7Props{C: true}))
	render(ctx, "E7false", p.E7(p.E7Props{C: false}))
	render(ctx, "E8true", p.E8(p.E8Props{C: true}))
	render(ctx, "E8false", p.E8(p.E8Props{C: false}))
	render(ctx, "E9true", p.E9(p.E9Props{C: true}))
	render(ctx, "E9false", p.E9(p.E9Props{C: false}))
	render(ctx, "E10true", p.E10(p.E10Props{C: true}))
	render(ctx, "E10false", p.E10(p.E10Props{C: false}))
	render(ctx, "E11true", p.E11(p.E11Props{C: true}))
	render(ctx, "E11false", p.E11(p.E11Props{C: false}))
}
`)

	cmd := exec.Command("go", "run", ".")
	cmd.Dir = tmp
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go run failed: %v\n%s", err, out)
	}
	return splitSpreadFoldOutput(string(out))
}

func splitSpreadFoldOutput(out string) map[string]string {
	res := map[string]string{}
	for p := range strings.SplitSeq(out, spreadFoldMarkerPrefix) {
		before, after, ok := strings.Cut(p, spreadFoldMarkerSuffix)
		if !ok {
			continue
		}
		res[before] = after
	}
	return res
}

// urlSinkArgsForTag computes the same navNames/imageNames/srcsetNames/prefixes
// split emitSpreadCall (emit.go) builds for tag, from the SAME production
// pieces (attrclass.Builtin's default classifier + urlWriterMethod) — so the
// reference render below classifies URL keys exactly as generated code would,
// without re-deriving or guessing the classification tables by hand.
func urlSinkArgsForTag(tag string) (nav, img, srcset, prefixes []string) {
	cls := attrclass.Builtin()
	for _, name := range cls.URLExactNames() {
		switch urlWriterMethod(tag, name) {
		case "URLImage":
			img = append(img, name)
		case "Srcset":
			srcset = append(srcset, name)
		default:
			nav = append(nav, name)
		}
	}
	return nav, img, srcset, cls.URLPrefixes()
}

// refTagHTML renders the naive full-fold reference for one <a ...>body</a>
// element: fold bag through gsx.ClassMerged/StyleMerged/Spread exactly the way
// the E4-E7 fold path's generated code does (ClassMerged(DefaultClassMerge,
// bag.Class()); StyleMerged("", bag.Style()); Spread(..., excluded=[class,
// style])) — the single, uniform "everything folds into one ConcatAttrs and
// renders through the leaf" reference the governing principle demands, used
// for EVERY scenario including the ones whose shipped codegen takes a
// different (optimized) path.
func refTagHTML(bag gsx.Attrs, body string) string {
	nav, img, srcset, prefixes := urlSinkArgsForTag("a")
	var buf bytes.Buffer
	gw := gsx.W(&buf)
	gw.ClassMerged(gsx.DefaultClassMerge, bag.Class())
	gw.StyleMerged("", bag.Style())
	gw.Spread(context.Background(), bag, nav, img, srcset, prefixes, []string{"class", "style"})
	return "<a" + buf.String() + ">" + body + "</a>"
}

// TestSpreadFoldDiffE0StaticLiteralOrder documents a discovered, PRE-EXISTING
// (not introduced by this feature) scope boundary: E0 (zero spreads, every
// attribute static) compiles to a plain literal string
// (`_gsxgw.S("<a id=\"e0\" ...>")`, verified directly against GenerateDirs'
// output) with NO bag, NO ConcatAttrs, and NO Spread call at all — so there is
// no fold to be byte-identical TO. Its attributes render in AUTHORED order.
// Every path that DOES build a bag (E1-E7, one spread or more) instead calls
// ClassMerged/StyleMerged BEFORE Spread, which hoists class/style ahead of the
// rest regardless of their source position — confirmed byte-identical to the
// full-fold reference by TestSpreadFoldDiffMatrix below for every one of
// those cases. Naively feeding E0's attributes through that same
// ClassMerged/StyleMerged/Spread reference produces a DIFFERENT attribute
// order (class/style hoisted) than the shipped literal — an order-only,
// non-security, pre-existing divergence between "no dynamic content at all"
// and "at least one dynamic contributor", orthogonal to the multi-spread-merge
// feature (Task 3 confirmed 0/1-spread goldens are byte-for-byte unchanged by
// this work). This test pins E0's actual (correct, unchanged) output directly
// rather than asserting it against the hoisting reference, and exists so this
// scope boundary is never silently lost.
func TestSpreadFoldDiffE0StaticLiteralOrder(t *testing.T) {
	got := runSpreadFoldMatrix(t)
	const want = `<a id="e0" class="c0" style="color:red" href="/e0">e0</a>`
	if got["E0"] != want {
		t.Fatalf("E0 static-literal order regressed\n got=%q\nwant=%q", got["E0"], want)
	}
	// Documents (does not fail on) the order-only divergence from the
	// hoisting convention every dynamic-bag path uses, so a future reader
	// sees the comparison rather than having to rediscover it.
	if hoisted := refTagHTML(gsx.Attrs{
		{Key: "id", Value: "e0"}, {Key: "class", Value: "c0"},
		{Key: "style", Value: "color:red"}, {Key: "href", Value: "/e0"},
	}, "e0"); hoisted == want {
		t.Logf("note: hoisting reference now matches literal order verbatim; the divergence documented above may no longer apply")
	}
}

// TestSpreadFoldDiffMatrix proves the codegen dispatch differential (Tier 2 of
// the design's "Fuzz / property" test plan) for every E1-E7 shape (E0 is
// covered separately above — it has no bag/fold to compare against): whichever
// path codegen takes for a given spread count (the current single-spread/
// lone-cond-spread paths for E1-E3; the >=2-spread fold for E4-E7) must render
// BYTE-IDENTICAL to independently folding that scenario's own attributes into
// one ConcatAttrs and rendering via the shared Spread leaf (refTagHTML).
// generate+build+render runs ONCE for the whole matrix (packages.Load is
// expensive; see runSpreadFoldMatrix).
func TestSpreadFoldDiffMatrix(t *testing.T) {
	got := runSpreadFoldMatrix(t)

	cases := []struct {
		name string
		bag  gsx.Attrs
		body string
	}{
		{"E1", gsx.Attrs{
			{Key: "id", Value: "e1"}, {Key: "class", Value: "c1"},
			{Key: "href", Value: "javascript:alert(1)"},
		}, "e1"},
		{"E2", gsx.ConcatAttrs(
			gsx.Attrs{{Key: "id", Value: "e2-pre"}},
			gsx.Attrs{{Key: "id", Value: "e2-a"}, {Key: "data-mid", Value: "m"}},
			gsx.Attrs{{Key: "data-post", Value: "p"}},
		), "e2"},
		{"E3true", gsx.Attrs{{Key: "class", Value: "c3"}, {Key: "href", Value: "/e3"}}, "e3"},
		{"E3false", nil, "e3"},
		{"E4", gsx.ConcatAttrs(
			gsx.Attrs{{Key: "class", Value: "a4"}, {Key: "data-k", Value: "1"}, {Key: "href", Value: "/e4-a"}},
			gsx.Attrs{{Key: "class", Value: "b4"}, {Key: "data-k", Value: "2"}},
		), "e4"},
		{"E5", gsx.ConcatAttrs(
			gsx.Attrs{{Key: "data-k", Value: "a5"}, {Key: "style", Value: "color:red"}},
			gsx.Attrs{{Key: "data-k", Value: "mid5"}},
			gsx.Attrs{{Key: "data-k", Value: "b5"}, {Key: "style", Value: "margin:0"}},
		), "e5"},
		{"E6true", gsx.ConcatAttrs(
			gsx.Attrs{{Key: "data-k", Value: "a6"}, {Key: "class", Value: "x6"}},
			gsx.Attrs{{Key: "data-k", Value: "b6"}, {Key: "class", Value: "y6"}},
		), "e6"},
		{"E6false", gsx.ConcatAttrs(
			gsx.Attrs{{Key: "data-k", Value: "a6"}, {Key: "class", Value: "x6"}},
			nil,
		), "e6"},
		{"E7true", gsx.ConcatAttrs(
			gsx.Attrs{{Key: "data-k", Value: "a7"}},
			gsx.Attrs{{Key: "data-mid", Value: "m7"}},
			gsx.Attrs{{Key: "data-k", Value: "b7"}},
		), "e7"},
		{"E7false", gsx.ConcatAttrs(
			gsx.Attrs{{Key: "data-k", Value: "a7"}},
			nil,
			gsx.Attrs{{Key: "data-k", Value: "b7"}},
		), "e7"},
		{"E8true", gsx.ConcatAttrs(
			gsx.Attrs{{Key: "class", Value: "base8"}},
			gsx.Attrs{{Key: "data-k", Value: "a8"}, {Key: "class", Value: "sp8"}},
			gsx.Attrs{{Key: "class", Value: "on8"}},
		), "e8"},
		{"E8false", gsx.ConcatAttrs(
			gsx.Attrs{{Key: "class", Value: "base8"}},
			gsx.Attrs{{Key: "data-k", Value: "a8"}, {Key: "class", Value: "sp8"}},
			gsx.Attrs{{Key: "class", Value: "off8"}},
		), "e8"},
		{"E9true", gsx.ConcatAttrs(
			gsx.Attrs{{Key: "id", Value: "e9"}, {Key: "style", Value: "color:red"}},
			gsx.Attrs{{Key: "style", Value: "margin:0"}},
		), "e9"},
		{"E9false", gsx.ConcatAttrs(
			gsx.Attrs{{Key: "id", Value: "e9"}, {Key: "style", Value: "color:red"}},
			nil,
		), "e9"},
		{"E10true", gsx.ConcatAttrs(
			gsx.Attrs{{Key: "class", Value: "base10"}},
			gsx.Attrs{{Key: "class", Value: "on10"}},
		), "e10"},
		{"E10false", gsx.ConcatAttrs(
			gsx.Attrs{{Key: "class", Value: "base10"}},
			gsx.Attrs{{Key: "class", Value: "off10"}},
		), "e10"},
		{"E11true", gsx.Attrs{{Key: "class", Value: "on11"}}, "e11"},
		{"E11false", gsx.Attrs{{Key: "class", Value: "off11"}}, "e11"},
	}

	// runSpreadFoldMatrix's harness renders all scenarios (E0 plus the
	// E1-E8 cases here); E0 is checked separately above.
	if len(got) != len(cases)+1 {
		t.Fatalf("runSpreadFoldMatrix returned %d renders, want %d: %v", len(got), len(cases)+1, got)
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			gotHTML, ok := got[c.name]
			if !ok {
				t.Fatalf("no render captured for %s", c.name)
			}
			want := refTagHTML(c.bag, c.body)
			if gotHTML != want {
				t.Fatalf("%s: shipped codegen != full-fold reference\n got=%q\nwant=%q", c.name, gotHTML, want)
			}
		})
	}
}
