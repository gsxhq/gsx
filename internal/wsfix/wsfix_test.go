package wsfix

import (
	"go/token"
	"strconv"
	"strings"
	"testing"

	"github.com/gsxhq/gsx/ast"
	"github.com/gsxhq/gsx/internal/wsnorm"
	"github.com/gsxhq/gsx/parser"
)

func wrap(body string) string { return "package p\n\ncomponent C() {\n" + body + "\n}\n" }

// render serializes the Text values of f in source order, with a marker for
// every other rendering node, so two renderings can be compared exactly.
func render(f *ast.File) string {
	var b strings.Builder
	ast.Inspect(f, func(n ast.Node) bool {
		switch v := n.(type) {
		case *ast.Text:
			b.WriteString(v.Value)
		case *ast.Interp:
			if s, err := strconv.Unquote(strings.TrimSpace(v.Expr)); err == nil {
				b.WriteString(s) // a literal is data: compare its bytes
			} else {
				b.WriteString("{" + strings.TrimSpace(v.Expr) + "}")
			}
		case *ast.Element:
			b.WriteString("<" + v.Tag + ">")
		}
		return true
	})
	return b.String()
}

func parse(t *testing.T, src string) *ast.File {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), "c.gsx", src, 0)
	if err != nil {
		t.Fatalf("parse: %v\n%s", err, src)
	}
	return f
}

// newRender is src under the current rule.
func newRender(t *testing.T, src string) string {
	f := parse(t, src)
	wsnorm.Normalize(f)
	return render(f)
}

// oldRender is src under the pre-v0.5 rule: each Text normalized on its own,
// dropping a line-break run at its edges and a line-break-only Text, keeping
// every other run as one space (a port of v0.4.6 wsnorm.normalizeText).
func oldRender(t *testing.T, src string) string {
	f := parse(t, src)
	var list func([]ast.Markup, bool) []ast.Markup
	var attrs func([]ast.Attr)
	attrs = func(as []ast.Attr) {
		for _, a := range as {
			switch v := a.(type) {
			case *ast.MarkupAttr:
				v.Value = list(v.Value, false)
			case *ast.CondAttr:
				attrs(v.Then)
				attrs(v.Else)
			case *ast.SwitchAttr:
				for _, c := range v.Cases {
					attrs(c.Body)
				}
			}
		}
	}
	list = func(nodes []ast.Markup, preserve bool) []ast.Markup {
		out := nodes[:0]
		for _, n := range nodes {
			switch v := n.(type) {
			case *ast.Text:
				if !preserve {
					if v.Value = oldText(v.Value); v.Value == "" {
						continue
					}
				}
			case *ast.Element:
				v.Children = list(v.Children, preserve || wsnorm.IsPreserveTag(v.Tag))
				attrs(v.Attrs)
			case *ast.Fragment:
				v.Children = list(v.Children, preserve)
			case *ast.MarkerRegion:
				v.Children = list(v.Children, preserve)
			case *ast.IfMarkup:
				v.Then, v.Else = list(v.Then, preserve), list(v.Else, preserve)
			case *ast.ForMarkup:
				v.Body = list(v.Body, preserve)
			case *ast.SwitchMarkup:
				for i := range v.Cases {
					v.Cases[i].Body = list(v.Cases[i].Body, preserve)
				}
			}
			out = append(out, n)
		}
		return out
	}
	for _, d := range f.Decls {
		switch v := d.(type) {
		case *ast.Component:
			v.Body = list(v.Body, false)
		case *ast.GoWithElements:
			for _, part := range v.Parts {
				switch p := part.(type) {
				case *ast.Element:
					p.Children = list(p.Children, wsnorm.IsPreserveTag(p.Tag))
					attrs(p.Attrs)
				case *ast.Fragment:
					p.Children = list(p.Children, false)
				}
			}
		}
	}
	return render(f)
}

func oldText(v string) string {
	isWS := func(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\r' }
	nl := func(s string) bool { return strings.ContainsAny(s, "\r\n") }
	if strings.Trim(v, " \t\r\n") == "" {
		if nl(v) {
			return ""
		}
		return " "
	}
	lead := len(v) - len(strings.TrimLeft(v, " \t\r\n"))
	trail := len(strings.TrimRight(v, " \t\r\n"))
	var b strings.Builder
	if lead > 0 && !nl(v[:lead]) {
		b.WriteByte(' ')
	}
	inWS := false
	for i := lead; i < trail; i++ {
		if isWS(v[i]) {
			inWS = true
			continue
		}
		if inWS {
			b.WriteByte(' ')
			inWS = false
		}
		b.WriteByte(v[i])
	}
	if trail < len(v) && !nl(v[trail:]) {
		b.WriteByte(' ')
	}
	return b.String()
}

// TestFixRestoresOldRule is the migration contract: the fixed source renders,
// under the current rule, exactly what the original rendered under the
// pre-v0.5 rule at every fixed boundary, and changes nothing else. Inputs are
// the #276 examples plus the adversarial review's repros.
func TestFixRestoresOldRule(t *testing.T) {
	cases := []struct {
		name, body string
		fixed      int
	}{
		{"old fmt punctuation lines", "\t<p>\n\t\t<c/>\n\t\t: <c/>\n\t\t, <c/>\n\t</p>", 2},
		{"period after link", "\t<p>download <a>it</a>\n\t.</p>", 1},
		{"parens around interp", "\t<p>(\n\t{ n } groups\n\t)</p>", 1},
		{"apostrophe after interp", "\t<p>{ name }\n\t’s Trash</p>", 1},
		{"entity apostrophe", "\t<p>{ name }\n\t&rsquo;s</p>", 1},
		{"percent after interp", "\t<p>{ n }\n\t%</p>", 1},
		{"across comment", "\t<p><b>Hello</b>\n\t{/* c */}\n\t.</p>", 1},
		{"in if body", "\t<p>{ if ok {\n\t\t<b>x</b>\n\t\t.\n\t} }</p>", 1},
		{"in slot", "\t<C title={ <span><b>hi</b>\n\t\t.</span> }/>", 1},
		{"literal before text", "\t<p>to { org }.{ \" \" }\n\tSet it.</p>", 1},
		{"literal after text", "\t<p>Hi\n\t{ \" \" }<b>x</b></p>", 1},
		{"literal at list edge", "\t<p>{ \" \" }\n\tHello</p>", 1},
		{"literal before punctuation run", "\t<p>x{ \" \" }\n\t.</p>", 1},
		{"literal then punctuation", "\t<p>x\n{ \" \" }.</p>", 1},
		{"literal between runs", "\t<p>a\n{ \" \" }\n.</p>", 2},
		{"literal after opener", "\t<p>(\n{ \" \" }\nx</p>", 2},
		{"crlf literal", "\t<p>a\r\n{ \" \" }’(</p>", 1},
		{"inline space then literal", "\t<p>a { \" \" }\nb</p>", 1},
		{"literal then inline space", "\t<p>a\n{ \" \" } b</p>", 1},
		{"two literals", "\t<p>a{ \" \" }{ \" \" }\nb</p>", 1},
		{"literal opening an if body", "\t<p>a <b>b</b>.\n\t{ if ok {\n\t\t{ \" \" }\n\t\t<c/>\n\t} }</p>", 1},
		{"literal closing an else body", "\t<p>{ if ok { x } else {\n\t\t<c/>{ \" \" }\n\t} }\n\tnext</p>", 1},
		{"literal opening a for body", "\t<p>Items:\n\t{ for range 2 {\n\t\t{ \" \" }<i/>\n\t} }</p>", 1},
		{"spaced literal opening an if body", "\t<p>No grants\n\t{ if q != \"\" { { \" match \" }{ q } } }.</p>", 1},
		{"spaced literal before text", "\t<p>{ n }{ \" items\" }\n\tleft</p>", 0},
		{"eight literals", "\t<p>a" + strings.Repeat(`{ " " }`, 8) + "\nb</p>", 1},
		{"switch case literal", "\t<p>Total:\n\t{ switch n {\n\tcase 1:\n\t\t{ \" \" }<b>one</b>\n\t} }</p>", 1},
		{"top-level var", "\t<p/>\n}\n\nvar v = <p><b>hi</b>\n\t.</p>\n\nfunc _() {", 1},
		// Not migration fixes: the old rule rendered these spaces too.
		{"space before comment closes element", "\t<p><b>a {/* c */}\n</b>\n.</p>", 0},
		{"space before bare comment", "\t<p>{ switch {\n\tcase true:\n\t\tx {/* c */}\n\t\t// bare\n\t\t{ \" \" }\n\t\ty\n\t} }</p>", 0},
		{"line break inside text", "\t<p>download it\n\t.</p>", 0},
		{"opener inside text", "\t<p>foo (\nbar)</p>", 0},
		{"repeated periods", "\t<p>x\n.\n.\n.</p>", 0},
		{"same-line space across comment", "\t<p><b>x</b> {/* c */}\n.</p>", 0},
		{"same-line space across go block", "\t<p><b>x</b> {{ _ = 1 }}\n.</p>", 0},
		{"prose", "\t<p>Hello\n\t<b>world</b>\n\tagain</p>", 0},
		{"inline space before punctuation", "\t<p>a . b</p>", 0},
		{"layout literal", "\t<p><b>a</b>{ \" \" }\n\t<b>b</b></p>", 0},
		{"pre", "\t<pre><b>a</b>\n.</pre>", 0},
		{"list edge", "\t<p>\n\t\t.\n\t</p>", 0},
	}
	// An intended #274 difference elsewhere (a glued word now separated): the
	// old rendering is not the target there.
	intended := map[string]bool{"prose": true, "spaced literal before text": true}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			src := wrap(c.body)
			out, n, err := Fix("c.gsx", []byte(src), false)
			if err != nil {
				t.Fatal(err)
			}
			if n != c.fixed {
				t.Fatalf("fixed %d, want %d:\n%s", n, c.fixed, out)
			}
			if n == 0 && string(out) != src {
				t.Fatalf("no fix but source changed:\n%s", out)
			}
			// Only boundaries the old rule rendered differently are fixed; a
			// case with no fix must already render the same both ways.
			if got, want := newRender(t, string(out)), oldRender(t, src); !intended[c.name] && got != want {
				t.Fatalf("fixed source renders\n%q\nold rule rendered\n%q\nfixed:\n%s", got, want, out)
			}
			if again, n2, _ := Fix("c.gsx", out, false); n2 != 0 || string(again) != string(out) {
				t.Fatalf("second Fix changed %d:\n%s", n2, again)
			}
		})
	}
}

func TestFindings(t *testing.T) {
	src := wrap("\t<p>it\n\t.{ \" \" }\n\tnext <b>x</b>\n\t.</p>")
	findings, file, err := Findings("c.gsx", []byte(src))
	if err != nil {
		t.Fatal(err)
	}
	type got struct {
		kind    wsnorm.FindingKind
		text    string
		migrate bool
	}
	var gots []got
	for _, f := range findings {
		gots = append(gots, got{f.Kind, src[file.Offset(f.Pos):file.Offset(f.End)], f.Migrate})
	}
	want := []got{
		{wsnorm.PunctuationSpace, "\n\t", false}, // inside one Text: lint only
		{wsnorm.RedundantSpaceLiteral, `{ " " }`, true},
		{wsnorm.PunctuationSpace, "\n\t", true},
	}
	if len(gots) != len(want) {
		t.Fatalf("findings = %+v", gots)
	}
	for i := range want {
		if gots[i] != want[i] {
			t.Fatalf("finding %d = %+v, want %+v", i, gots[i], want[i])
		}
	}
}

// TestFixAll covers -all on the #277 follow-up repros: same-line spaces (often
// joined by an earlier gsx fmt) before punctuation and beside spaced literals,
// including a literal at a block edge. Default mode leaves the same-line ones.
func TestFixAll(t *testing.T) {
	cases := []struct {
		name, in, want string
		defaultFixes   int
	}{
		{"space after link", "\t<p>See <a>docs</a> .</p>", "\t<p>See <a>docs</a>.</p>", 0},
		{"space before closer after block", "\t<p>{ if ok { x } } .</p>", "\t<p>{ if ok { x } }.</p>", 0},
		{"inside text", "\t<p>a . b</p>", "\t<p>a. b</p>", 0},
		{"comma and parens", "\t<p>{ n } , items ( { n } )</p>", "\t<p>{ n }, items ({ n })</p>", 0},
		{"literal at block edge", "\t<p>Done. { if ok {\n\t\t{ \" \" }More.\n\t} }</p>", "\t<p>Done.{ if ok {\n\t\t{ \" \" }More.\n\t} }</p>", 0},
		{"literal then same-line space", "\t<p>a{ \" \" } <b>x</b></p>", "\t<p>a{ \" \" }<b>x</b></p>", 0},
		{"same-line space and line break", "\t<p>Listed { \" \" }\n\tthen</p>", "\t<p>Listed{ \" \" }then</p>", 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			src := wrap(c.in)
			out, _, err := Fix("c.gsx", []byte(src), true)
			if err != nil {
				t.Fatal(err)
			}
			if string(out) != wrap(c.want) {
				t.Fatalf("Fix all:\n%s\nwant:\n%s", out, wrap(c.want))
			}
			if again, n, _ := Fix("c.gsx", out, true); n != 0 || string(again) != string(out) {
				t.Fatalf("second Fix all changed %d:\n%s", n, again)
			}
			if _, n, _ := Fix("c.gsx", []byte(src), false); n != c.defaultFixes {
				t.Fatalf("default Fix made %d fixes, want %d", n, c.defaultFixes)
			}
		})
	}
}
