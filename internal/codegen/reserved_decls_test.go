package codegen

import (
	"slices"
	"strings"
	"testing"

	"go/token"

	gsxparser "github.com/gsxhq/gsx/parser"
)

// checkReservedNames parses src as a .gsx file, runs checkReservedDecls, and
// returns the offending identifier names it reports (deduped, first-occurrence).
func checkReservedNames(t *testing.T, src string) []string {
	t.Helper()
	fset := token.NewFileSet()
	f, err := gsxparser.ParseFile(fset, "views.gsx", []byte(src), 0)
	if err != nil {
		t.Fatalf("parse %q: %v", src, err)
	}
	rds := checkReservedDecls(f)
	var names []string
	for _, rd := range rds {
		names = append(names, rd.name)
	}
	return names
}

// TestReservedPrefixCaughtInEveryGoContext pins that a `_gsx` identifier is
// rejected wherever gsx sees user Go — the whole point of the scanner-based
// check (it replaced a parse-based one that saw only top-level declarations and,
// worse, could not read a region the formatter paren-wrapped). Each case names a
// distinct Go-fragment context.
func TestReservedPrefixCaughtInEveryGoContext(t *testing.T) {
	cases := map[string]string{
		"top-level var (GoChunk)":  "package views\nvar _gsxfoo = 1\n",
		"top-level func (GoChunk)": "package views\nfunc _gsxfoo() {}\n",
		"func-body local (GoChunk)": "package views\nimport \"github.com/gsxhq/gsx\"\n" +
			"func f() gsx.Node { _gsxio := 4; _ = _gsxio; return nil }\n",
		"func-body local + element (GoWithElements)": "package views\nimport \"github.com/gsxhq/gsx\"\n" +
			"func f() gsx.Node { _gsxio := 4; _ = _gsxio; return <b/> }\n",
		"import alias":   "package views\nimport _gsxfoo \"strings\"\nvar _ = _gsxfoo.Count\n",
		"GoBlock {{ }}":  "package views\ncomponent C() {\n\t{{ _gsxgw := \"z\"; _ = _gsxgw }}\n\t<b/>\n}\n",
		"interpolation":  "package views\ncomponent C() { <b>{ _gsxfoo }</b> }\n",
		"expr attr":      "package views\ncomponent C() { <b id={ _gsxfoo }/> }\n",
		"spread attr":    "package views\ncomponent C() { <b { _gsxfoo... }/> }\n",
		"if cond":        "package views\ncomponent C() { { if _gsxfoo { <b/> } } }\n",
		"for clause":     "package views\ncomponent C() { { for _gsxfoo := range xs { <b/> } } }\n",
		"switch tag":     "package views\ncomponent C() { { switch _gsxfoo { } } }\n",
		"class part":     "package views\ncomponent C() { <b class={ _gsxfoo }/> }\n",
		"pipe stage arg": "package views\ncomponent C() { <b>{ x |> f(_gsxfoo) }</b> }\n",
		// A method name is a `_gsx` identifier in Go position like any other. It
		// cannot collide with a generator import (the generator emits no methods),
		// but the rule is blanket — no `_gsx` identifier anywhere gsx sees Go — so
		// it is reported for consistency rather than carved out as a special case.
		"method name": "package views\ntype T struct{}\nfunc (t T) _gsxMethod() {}\n",
	}
	for name, src := range cases {
		t.Run(name, func(t *testing.T) {
			got := checkReservedNames(t, src)
			if len(got) == 0 || !slices.Contains(got, firstReservedName(src)) {
				t.Fatalf("expected a _gsx identifier reported for %q, got %v", name, got)
			}
		})
	}
}

// TestReservedPrefixNoFalsePositives pins that go/scanner's token classification
// is what does the work: a `_gsx` sequence that is NOT a Go identifier — string
// literal, comment, markup prose — must not be reported. A text-scan would fail
// every one of these.
func TestReservedPrefixNoFalsePositives(t *testing.T) {
	cases := map[string]string{
		"inside a Go string literal": "package views\nvar s = \"_gsxfoo\"\n",
		"inside a Go comment":        "package views\n// _gsxfoo is not an identifier\nvar x = 1\n",
		"markup text content":        "package views\ncomponent C() { <p>_gsxfoo</p> }\n",
		"static attribute value":     "package views\ncomponent C() { <p data-x=\"_gsxfoo\"/> }\n",
		"blank import":               "package views\nimport _ \"strings\"\ncomponent C() { <b/> }\n",
	}
	for name, src := range cases {
		t.Run(name, func(t *testing.T) {
			if got := checkReservedNames(t, src); len(got) != 0 {
				t.Fatalf("expected NO reserved-name report for %q, got %v", name, got)
			}
		})
	}
}

// TestReservedPrefixParenWrappedElement is the exact regression that motivated
// the rewrite: main's formatter wraps a multi-line element literal as
// `return ( <>…</> )`, and `return (\n x \n)` is not a valid Go statement under
// automatic semicolon insertion. The old parse-based check rejected this legal
// gsx; the scanner must accept it (no `_gsx` name → no report) AND still catch a
// `_gsx` name in the surrounding Go.
func TestReservedPrefixParenWrappedElement(t *testing.T) {
	legal := "package views\nimport \"github.com/gsxhq/gsx\"\n" +
		"func Items(xs []string) gsx.Node {\n\treturn (\n\t\t<>\n\t\t\t{ for _, s := range xs { <li>{ s }</li> } }\n\t\t</>\n\t)\n}\n"
	if got := checkReservedNames(t, legal); len(got) != 0 {
		t.Fatalf("paren-wrapped element literal must not be rejected; got %v", got)
	}

	hostile := "package views\nimport \"github.com/gsxhq/gsx\"\n" +
		"func Items(xs []string) gsx.Node {\n\t_gsxio := 0\n\t_ = _gsxio\n\treturn (\n\t\t<>\n\t\t\t{ for _, s := range xs { <li>{ s }</li> } }\n\t\t</>\n\t)\n}\n"
	if got := checkReservedNames(t, hostile); !slices.Contains(got, "_gsxio") {
		t.Fatalf("expected _gsxio reported inside a paren-wrapped-return func; got %v", got)
	}
}

// TestReservedPrefixDedupPerName pins that one offending name is reported once,
// even when it is declared and then used — one mistake, one diagnostic.
func TestReservedPrefixDedupPerName(t *testing.T) {
	src := "package views\nvar _gsxfoo = 1\nvar _ = _gsxfoo + _gsxfoo\n"
	got := checkReservedNames(t, src)
	if len(got) != 1 || got[0] != "_gsxfoo" {
		t.Fatalf("expected exactly one _gsxfoo report, got %v", got)
	}
}

// firstReservedName returns the first `_gsx…` identifier-looking token in src,
// used only to assert the expected name in the multi-context table.
func firstReservedName(src string) string {
	i := strings.Index(src, reservedPrefix)
	if i < 0 {
		return reservedPrefix
	}
	j := i
	for j < len(src) && (isIdentByte(src[j])) {
		j++
	}
	return src[i:j]
}

func isIdentByte(b byte) bool {
	return b == '_' || b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9'
}
