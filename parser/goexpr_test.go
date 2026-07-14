package parser

import (
	"fmt"
	"go/token"
	"strings"
	"testing"
)

func TestScanGoElementMarks(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want []int // byte offsets of element-starting '<'
	}{
		// --- elements at operand positions ---
		{"assign", `x = <div/>`, []int{4}},
		{"define", `x := <Foo/>`, []int{5}},
		{"return", `return <div/>`, []int{7}},
		{"call arg", `f(<Foo/>)`, []int{2}},
		{"second call arg", `f(a, <Foo/>)`, []int{5}},
		// NOTE: the task brief's table has this as []int{4, 9}, but the
		// second '<' in `[]T{<a/>, <b/>}` is genuinely at byte offset 10
		// (count: '[',']','T','{','<','a','/','>',',',' ','<' -> index 10),
		// not 9. Verified with a byte-offset dump; flagged in the task
		// report rather than silently "corrected" without explanation.
		{"slice elem", `[]T{<a/>, <b/>}`, []int{4, 10}},
		{"composite value", `M{K: <Foo/>}`, []int{5}},
		{"paren", `(<div/>)`, []int{1}},
		{"unary not", `!<Foo/>`, []int{1}}, // nonsensical but position-correct
		{"binary rhs", `x && <Foo/>`, []int{5}},

		// --- NOT elements: '<' in operator position is less-than ---
		{"less than", `a < b`, nil},
		{"less no space", `a<b`, nil},
		{"lte", `a <= b`, nil},
		{"shift", `a << b`, nil},
		{"cmp chain", `a < b && c > d`, nil},
		{"index cmp", `arr[i] < n`, nil},
		{"call result cmp", `f(x) < g(y)`, nil},

		// --- NOT elements: channel ops ---
		{"chan recv", `x := <-ch`, nil},
		{"chan send", `ch <- x`, nil},
		{"recv in call", `f(<-ch)`, nil},

		// --- Go generics use [] not <> : no ambiguity ---
		{"generic call", `Map[int, string](m)`, nil},
		{"generic decl frag", `[]Pair[K, V]{}`, nil},

		// --- element with nested Go / attrs / children ---
		{"attrs+interp", `x = <a href={u} class="c">{ label }</a>`, []int{4}},
		{"nested tag not counted twice", `x = <div><span/></div>`, []int{4}}, // outer only; inner is inside the element span
		{"lt after element", `<Foo/> < 3`, []int{0}},                         // element first, then a real '<'
		// Regression: text content is prose, not Go source. The lone
		// apostrophe in "it's" and the "http://" URL must NOT be lexed as a
		// rune literal / line comment by the span walk — otherwise the skip
		// runs to EOF and the sibling <b/> is silently dropped.
		{"text apostrophe and url then sibling", `x = <a href="/help">it's here: http://x</a>, <b/>`, []int{4, 45}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			marks := scanGoElementMarks(c.src)
			got := make([]int, len(marks))
			for i, m := range marks {
				got[i] = m.Off
			}
			if !equalInts(got, c.want) {
				t.Fatalf("scanGoElementMarks(%q) = %v, want %v", c.src, got, c.want)
			}
		})
	}
}

// TestScanGoParts locks in the merged element-mark + prefixed-literal scan the
// two Go-region split paths (splitGoElements, SplitGoExprElements) run: a
// value-position f`/js`/css` literal is reported as a literal item, an element
// mark as an element item, in source order; and a BARE Go raw string and an
// operator-position '<' are NOT items. (js`/css` are no longer gated — they
// split like f` and lower to gsx.RawJS/gsx.RawCSS.)
func TestScanGoParts(t *testing.T) {
	type item struct {
		off int
		lit bool
	}
	cases := []struct {
		name string
		src  string
		want []item
	}{
		{"f literal var", "greeting = f`hi @{name}`", []item{{11, true}}},
		{"f literal call arg", "wrap(f`id-@{n}`)", []item{{5, true}}},
		{"bare backtick not split", "x = `raw`", nil},
		{"js literal splits", "x = js`color`", []item{{4, true}}},
		{"css literal splits", "x = css`c`", []item{{4, true}}},
		{"element only", "x = <Foo/>", []item{{4, false}}},
		{"element then f literal", "wrap(<Foo/>, f`@{n}`)", []item{{5, false}, {13, true}}},
		{"f literal then element", "wrap(f`@{n}`, <Foo/>)", []item{{5, true}, {14, false}}},
		// `xf`…` — the f has no ident boundary (x precedes it), so it is a bare
		// Go raw string suffixed to the ident, never an f` literal.
		{"no ident boundary", "a = xf`nope`", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			parts := scanGoParts(c.src)
			got := make([]item, len(parts))
			for i, p := range parts {
				got[i] = item{p.Off, p.IsLiteral}
			}
			if len(got) != len(c.want) {
				t.Fatalf("scanGoParts(%q) = %v, want %v", c.src, got, c.want)
			}
			for i := range got {
				if got[i] != c.want[i] {
					t.Fatalf("scanGoParts(%q) = %v, want %v", c.src, got, c.want)
				}
			}
		})
	}
}

// TestScanGoPartsWholeLiteralPipeDiagnostic pins W1': a `|>` chain directly
// after a value-position literal (f`/js`/css`, either delimiter) is gsx pipe
// syntax with no meaning there (the whole literal, not one of its @{ } holes,
// would be the pipe's input) — SplitGoExprElements must report it, positioned
// at the `|>`, rather than silently leaving it as verbatim GoText that only
// fails much later as an unpositioned "expected operand, found '>'" when the
// assembled skeleton is parsed as Go. Covers both delimiters and a non-f lang
// prefix (js`), matching the brief's self-review checklist.
func TestScanGoPartsWholeLiteralPipeDiagnostic(t *testing.T) {
	const wantMsg = "whole-literal pipelines are not supported in Go-expression position; wrap the literal in a function call instead"
	cases := []struct {
		name string
		src  string
	}{
		{"backtick f literal", "var x = f`hi` |> upper"},
		{"backtick js literal", "var x = js`f()` |> minify"},
		{"dquote f literal", `var x = f"hi" |> upper`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fset := token.NewFileSet()
			file := fset.AddFile("", fset.Base(), len(c.src))
			base := file.Pos(0)
			_, errs := SplitGoExprElements(fset, c.src, base, nil)

			var found *Error
			for i := range errs {
				if errs[i].Msg == wantMsg {
					found = &errs[i]
				}
			}
			if found == nil {
				t.Fatalf("SplitGoExprElements(%q): want whole-literal-pipe diagnostic, got %+v", c.src, errs)
			}
			wantOff := strings.Index(c.src, "|>")
			if wantOff < 0 {
				t.Fatalf("test bug: %q has no |>", c.src)
			}
			if gotOff := fset.Position(found.Pos).Offset; gotOff != wantOff {
				t.Errorf("SplitGoExprElements(%q): diagnostic at offset %d, want %d (the `|>`)", c.src, gotOff, wantOff)
			}
		})
	}
}

// TestScanGoPartsNoWholeLiteralPipeFalsePositive is the negative companion to
// TestScanGoPartsWholeLiteralPipeDiagnostic: legitimate Go following a literal
// (including a bitwise-or NOT immediately followed by '>') must not trip the
// diagnostic, and the literal item itself must still be reported normally
// (detection-only — the split stays well-formed).
func TestScanGoPartsNoWholeLiteralPipeFalsePositive(t *testing.T) {
	const wantMsg = "whole-literal pipelines are not supported in Go-expression position"
	cases := []string{
		"var x = f`hi` + x",
		"var x = f`hi` | mask",     // bitwise-or, not followed by '>'
		"var x = f`hi` |  > upper", // '|' and '>' not adjacent: not a pipe token
		"var x = f`hi`",
		// `|>` inside a Go STRING in the GoText after a literal: prose bytes,
		// never a pipe (the peek only looks at the bytes directly after the
		// literal's own end).
		"var x = f`hi`; var y = \"a |> b\"",
	}
	for _, src := range cases {
		t.Run(src, func(t *testing.T) {
			fset := token.NewFileSet()
			file := fset.AddFile("", fset.Base(), len(src))
			_, errs := SplitGoExprElements(fset, src, file.Pos(0), nil)
			for _, e := range errs {
				if strings.Contains(e.Msg, wantMsg) {
					t.Fatalf("SplitGoExprElements(%q): unexpected whole-literal-pipe diagnostic: %+v", src, e)
				}
			}
		})
	}
}

// TestParseFileWholeLiteralPipeDiagnostic pins the full lang x delimiter x
// container-site matrix for W1' through the REAL production entry point
// (ParseFileWithClassifier): the top-level site exercises splitGoElements's
// report, and the interp/GoBlock sites exercise parseInterp/parseGoBlock's
// parse-time pre-check in markup.go — the path that actually fires in a
// `gsx generate` run (SplitGoExprElements's own report, unit-tested above, is
// unreachable for those two sites in the full pipeline because analyze.go's
// deferred split discards its errs). Every one of the 6 (f/js/css x
// backtick/dquote) combos is pinned at every one of the 3 sites, each
// asserting the exact message, exactly once, positioned at the `|>`.
func TestParseFileWholeLiteralPipeDiagnostic(t *testing.T) {
	const wantMsg = "whole-literal pipelines are not supported in Go-expression position; wrap the literal in a function call instead"
	literals := map[string]string{
		"f-backtick":   "f`hi`",
		"js-backtick":  "js`f()`",
		"css-backtick": "css`c`",
		"f-dquote":     `f"hi"`,
		"js-dquote":    `js"f()"`,
		"css-dquote":   `css"c"`,
	}
	sites := map[string]string{
		"toplevel": "package demo\n\nvar x = %s |> upper\n",
		"interp":   "package demo\n\ncomponent Page() {\n\t<p>{ wrap(%s |> upper) }</p>\n}\n",
		"goblock":  "package demo\n\ncomponent Page() {\n\t{{ h := %s |> upper }}\n\t<p>x</p>\n}\n",
	}
	for siteName, tmpl := range sites {
		for litName, lit := range literals {
			t.Run(siteName+"/"+litName, func(t *testing.T) {
				src := fmt.Sprintf(tmpl, lit)
				fset := token.NewFileSet()
				_, errs := ParseFileWithClassifier(fset, "input.gsx", src, 0, nil)
				var found *Error
				for i := range errs {
					if errs[i].Msg == wantMsg {
						if found != nil {
							t.Fatalf("ParseFile(%q): duplicate whole-literal-pipe diagnostic: %+v", src, errs)
						}
						found = &errs[i]
					}
				}
				if found == nil {
					t.Fatalf("ParseFile(%q): want whole-literal-pipe diagnostic, got %+v", src, errs)
				}
				wantOff := strings.Index(src, "|>")
				if gotOff := fset.Position(found.Pos).Offset; gotOff != wantOff {
					t.Errorf("ParseFile(%q): diagnostic at offset %d, want %d (the `|>`)", src, gotOff, wantOff)
				}
			})
		}
	}
}

// TestParseFileNoWholeLiteralPipeFalsePositive: full-file negatives through
// the same real entry point — a `|>` inside an ordinary Go STRING (in the
// GoText after a literal, or alongside one in an interp) must not trip the
// diagnostic, and a legitimate whole-literal pipe at an interp's own
// top-level seed (peeled into a pipe Stage by parsePipe before the pre-check
// runs) must stay accepted.
func TestParseFileNoWholeLiteralPipeFalsePositive(t *testing.T) {
	const wantMsg = "whole-literal pipelines are not supported in Go-expression position"
	srcs := []string{
		// `|>` inside a Go string in the GoText AFTER a literal.
		"package demo\n\nvar x = f`hi`\nvar y = \"a |> b\"\n",
		"package demo\n\nvar x = f`hi`; var y = \"a |> b\"\n",
		// `|>` inside a string next to a literal in an interp.
		"package demo\n\ncomponent Page() {\n\t<p>{ wrap(f`hi`, \"a |> b\") }</p>\n}\n",
		// interp's own top-level seed pipe: legitimate, handled by parsePipe.
		"package demo\n\ncomponent Page() {\n\t<p>{ f`hi` |> upper }</p>\n}\n",
	}
	for _, src := range srcs {
		t.Run(src, func(t *testing.T) {
			fset := token.NewFileSet()
			_, errs := ParseFileWithClassifier(fset, "input.gsx", src, 0, nil)
			for _, e := range errs {
				if strings.Contains(e.Msg, wantMsg) {
					t.Fatalf("ParseFile(%q): unexpected whole-literal-pipe diagnostic: %+v", src, e)
				}
			}
		})
	}
}

// TestContainsEmbeddedLiteralPrefix pins the parse-hot-path gate: a seed with
// no f/js/css-prefixed delimiter — including one holding an ordinary Go
// string or a bare raw string — must be filtered out by the cheap byte scan
// (never reaching go/scanner), while every prefixed lang x delimiter shape
// passes through.
func TestContainsEmbeddedLiteralPrefix(t *testing.T) {
	cases := []struct {
		src  string
		want bool
	}{
		{`user.Name`, false},
		{`fmt.Sprintf("%d", n)`, false}, // plain string: the common hot-path shape
		{"x + `raw`", false},            // bare raw string: no prefix
		{"xf`nope`", false},             // no ident boundary: not an f` literal
		{"f`hi`", true},
		{"js`f()`", true},
		{"css`c`", true},
		{`f"hi"`, true},
		{`js"f()"`, true},
		{`css"c"`, true},
		{`wrap("s", f"hi")`, true},
	}
	for _, c := range cases {
		if got := containsEmbeddedLiteralPrefix(c.src); got != c.want {
			t.Errorf("containsEmbeddedLiteralPrefix(%q) = %v, want %v", c.src, got, c.want)
		}
	}
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
