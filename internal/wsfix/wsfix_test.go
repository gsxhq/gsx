package wsfix

import (
	"go/token"
	"strings"
	"testing"

	"github.com/gsxhq/gsx/ast"
	"github.com/gsxhq/gsx/internal/wsnorm"
	"github.com/gsxhq/gsx/parser"
)

func wrap(body string) string { return "package p\n\ncomponent C() {\n" + body + "\n}\n" }

// rendered is the normalized text of src: Text values joined, every other node
// shown as a marker, so a fix can be checked against the output it renders.
func rendered(t *testing.T, src string) string {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), "c.gsx", src, 0)
	if err != nil {
		t.Fatalf("parse: %v\n%s", err, src)
	}
	wsnorm.Normalize(f)
	var b strings.Builder
	ast.Inspect(f, func(n ast.Node) bool {
		switch v := n.(type) {
		case *ast.Text:
			b.WriteString(v.Value)
		case *ast.Interp:
			b.WriteString("{" + strings.TrimSpace(v.Expr) + "}")
		case *ast.Element:
			b.WriteString("<" + v.Tag + ">")
		}
		return true
	})
	return b.String()
}

func TestFix(t *testing.T) {
	cases := []struct {
		name, in, want string
		fixed          int
	}{
		{"old fmt punctuation lines",
			"\t<p>\n\t\t<c/>\n\t\t: <c/>\n\t\t, <c/>\n\t</p>",
			"\t<p>\n\t\t<c/>: <c/>, <c/>\n\t</p>", 2},
		{"terminal period", "\t<p>download it\n\t.</p>", "\t<p>download it.</p>", 1},
		{"parens", "\t<p>(\n\t{ n } groups\n\t)</p>", "\t<p>({ n } groups)</p>", 2},
		{"apostrophe after interp", "\t<p>{ name }\n\t’s Trash</p>", "\t<p>{ name }’s Trash</p>", 1},
		{"entity apostrophe", "\t<p>{ name }\n\t&rsquo;s</p>", "\t<p>{ name }&rsquo;s</p>", 1},
		{"percent", "\t<p>{ n }\n\t%</p>", "\t<p>{ n }%</p>", 1},
		{"across comment", "\t<p>Hello\n\t{/* c */}\n\t.</p>", "\t<p>Hello{/* c */}.</p>", 1},
		{"redundant literal before text", "\t<p>to { org }.{ \" \" }\n\tSet it.</p>", "\t<p>to { org }.\n\tSet it.</p>", 1},
		{"redundant literal after text", "\t<p>Hi\n\t{ \" \" }<b>x</b></p>", "\t<p>Hi\n\t<b>x</b></p>", 1},
		{"redundant literal at list edge", "\t<p>{ \" \" }\n\tHello</p>", "\t<p>{ \" \" }Hello</p>", 1},
		{"redundant literal and punctuation", "\t<p>x{ \" \" }\n\t.</p>", "\t<p>x{ \" \" }.</p>", 1},
		// Not findings: ordinary prose, inline spaces, layout, preserved text,
		// two-space literals.
		{"prose", "\t<p>Hello\n\t<b>world</b>\n\tagain</p>", "", 0},
		{"inline space before punctuation", "\t<p>a . b</p>", "", 0},
		{"inline literal", "\t<p>a{ \" \" }b</p>", "", 0},
		{"layout literal", "\t<p><b>a</b>{ \" \" }\n\t<b>b</b></p>", "", 0},
		{"two-space literal", "\t<p>a{ \"  \" }\n\tb</p>", "", 0},
		{"pre", "\t<pre>a\n.</pre>", "", 0},
		{"list edge newline", "\t<p>\n\t\t.\n\t</p>", "", 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in := wrap(c.in)
			out, n, err := Fix("c.gsx", []byte(in))
			if err != nil {
				t.Fatal(err)
			}
			want := in
			if c.want != "" {
				want = wrap(c.want)
			}
			if string(out) != want || n != c.fixed {
				t.Fatalf("Fix = %d fixes\n%s\nwant %d\n%s", n, out, c.fixed, want)
			}
			if again, n2, _ := Fix("c.gsx", out); n2 != 0 || string(again) != string(out) {
				t.Fatalf("second Fix changed %d:\n%s", n2, again)
			}
		})
	}
}

// TestFixRendersOldRule pins the migration contract on the issue's examples:
// the fixed source renders what the pre-#274 rule rendered.
func TestFixRendersOldRule(t *testing.T) {
	cases := []struct{ in, want string }{
		{"\t<p>\n\t\t<c/>\n\t\t: <c/>\n\t\t, <c/>\n\t</p>", "<p><c>: <c>, <c>"},
		{"\t<p>{ inviter } invited you to { org }.{ \" \" }\n\tSet your password.</p>", "<p>{inviter} invited you to {org}. Set your password."},
		{"\t<p>(\n\t{ n } groups\n\t)</p>", "<p>({n} groups)"},
	}
	for _, c := range cases {
		out, _, err := Fix("c.gsx", []byte(wrap(c.in)))
		if err != nil {
			t.Fatal(err)
		}
		if got := rendered(t, string(out)); got != c.want {
			t.Errorf("rendered %q, want %q\nfixed:\n%s", got, c.want, out)
		}
	}
}

func TestFindingPositions(t *testing.T) {
	src := wrap("\t<p>it\n\t.{ \" \" }\n\tnext</p>")
	findings, file, err := Findings("c.gsx", []byte(src))
	if err != nil {
		t.Fatal(err)
	}
	got := map[wsnorm.FindingKind]string{}
	for _, f := range findings {
		got[f.Kind] = src[file.Offset(f.Pos):file.Offset(f.End)]
	}
	if got[wsnorm.PunctuationLineBreak] != "\n\t" || got[wsnorm.RedundantSpaceLiteral] != `{ " " }` {
		t.Fatalf("finding ranges = %q", got)
	}
}
