package parser

import (
	"go/token"
	"strings"
	"testing"

	"github.com/gsxhq/gsx/ast"
)

// commentAttrs collects the CommentAttr nodes from an attribute list in order.
func commentAttrs(attrs []ast.Attr) []*ast.CommentAttr {
	var out []*ast.CommentAttr
	for _, a := range attrs {
		if c, ok := a.(*ast.CommentAttr); ok {
			out = append(out, c)
		}
	}
	return out
}

// childComments collects the Comment nodes from a children list in order.
func childComments(nodes []ast.Markup) []*ast.Comment {
	var out []*ast.Comment
	for _, n := range nodes {
		if c, ok := n.(*ast.Comment); ok {
			out = append(out, c)
		}
	}
	return out
}

// A braced comment group in attribute position holds a SEQUENCE of comments
// (commentParts' boundary: no real Go token inside the braces). The parser
// emits one CommentAttr per interior comment — collapsing them into one node
// loses the delimiters and corrupts the text on output ("a */ // b"). An
// empty group is a single empty block comment (canonical output /**/).
func TestAttrCommentGroupSplitsPerComment(t *testing.T) {
	cases := []struct {
		name string
		src  string // attribute-position bytes between type="checkbox" and value="x"
		want []ast.CommentAttr
	}{
		{
			name: "two block comments",
			src:  "{ /* a */ /* b */ }",
			want: []ast.CommentAttr{
				{Text: "a", Block: true, Trailing: false},
				{Text: "b", Block: true, Trailing: true}, // same source line as part before it
			},
		},
		{
			name: "block then line",
			src:  "{ /* a */ // b\n\t\t}",
			want: []ast.CommentAttr{
				{Text: "a", Block: true, Trailing: false},
				{Text: "b", Block: false, Trailing: true},
			},
		},
		{
			name: "line then block on next line",
			src:  "{ // a\n\t\t/* b */ }",
			want: []ast.CommentAttr{
				{Text: "a", Block: false, Trailing: false},
				{Text: "b", Block: true, Trailing: false}, // next source line: not trailing
			},
		},
		{
			name: "empty group",
			src:  "{}",
			want: []ast.CommentAttr{
				{Text: "", Block: true, Trailing: false},
			},
		},
		{
			name: "whitespace-only group",
			src:  "{ }",
			want: []ast.CommentAttr{
				{Text: "", Block: true, Trailing: false},
			},
		},
		{
			name: "semicolons tolerated, contribute nothing",
			src:  "{ ; }",
			want: []ast.CommentAttr{
				{Text: "", Block: true, Trailing: false},
			},
		},
		{
			name: "single block comment unchanged",
			src:  "{/* braced note */}",
			want: []ast.CommentAttr{
				{Text: "braced note", Block: true, Trailing: false},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src := "package p\n\ncomponent C() {\n\t<input\n\t\ttype=\"checkbox\"\n\t\t" + tc.src + "\n\t\tvalue=\"x\"\n\t/>\n}\n"
			f := parseStringT(t, src)
			el := f.Decls[0].(*ast.Component).Body[0].(*ast.Element)
			got := commentAttrs(el.Attrs)
			if len(got) != len(tc.want) {
				t.Fatalf("got %d CommentAttr nodes, want %d: %+v", len(got), len(tc.want), got)
			}
			for i, w := range tc.want {
				g := got[i]
				if g.Text != w.Text || g.Block != w.Block || g.Trailing != w.Trailing {
					t.Errorf("node %d = {Text:%q Block:%t Trailing:%t}, want {Text:%q Block:%t Trailing:%t}",
						i, g.Text, g.Block, g.Trailing, w.Text, w.Block, w.Trailing)
				}
			}
		})
	}
}

// Group-node spans partition the group: the first node's span opens at `{`,
// the last closes past `}`, and interior extents are exact SOURCE byte offsets
// even when a comment carries CRLF line endings (go/scanner strips '\r' from
// comment literals, so extents must not be derived from len(lit)).
func TestAttrCommentGroupSpansAreSourceExact(t *testing.T) {
	group := "{ /* a */ /* b\r\nc */ /* d */ }"
	prefix := "package p\n\ncomponent C() {\n\t<input\n\t\ttype=\"checkbox\"\n\t\t"
	src := prefix + group + "\n\t\tvalue=\"x\"\n\t/>\n}\n"
	fset := token.NewFileSet()
	f, err := ParseFile(fset, "test.gsx", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	el := f.Decls[0].(*ast.Component).Body[0].(*ast.Element)
	got := commentAttrs(el.Attrs)
	if len(got) != 3 {
		t.Fatalf("got %d CommentAttr nodes, want 3", len(got))
	}
	off := func(p token.Pos) int { return fset.Position(p).Offset }
	groupOff := len(prefix)
	// First node opens at `{`; last node closes just past `}`.
	if o := off(got[0].Pos()); o != groupOff {
		t.Errorf("first node starts at offset %d, want %d (the `{`)", o, groupOff)
	}
	if o := off(got[2].End()); o != groupOff+len(group) {
		t.Errorf("last node ends at offset %d, want %d (past the `}`)", o, groupOff+len(group))
	}
	// The middle node's extent is the exact source token, CRs included.
	bStart := groupOff + strings.Index(group, "/* b")
	bEnd := groupOff + strings.Index(group, "c */") + len("c */")
	if o := off(got[1].Pos()); o != bStart {
		t.Errorf("middle node starts at offset %d, want %d", o, bStart)
	}
	if o := off(got[1].End()); o != bEnd {
		t.Errorf("middle node ends at offset %d, want %d (exact source extent past `*/`)", o, bEnd)
	}
	// go/scanner strips '\r' from comment literals, so the TEXT is CRLF-
	// normalized (as gofmt does) even though the SPAN covers the raw source.
	if got[1].Text != "b\nc" {
		t.Errorf("middle node text = %q, want %q", got[1].Text, "b\nc")
	}
}

// Same splitting in child position: one *ast.Comment per interior comment,
// all Bare=false (braced). An empty group is a single empty block comment
// (canonical output {}).
func TestChildCommentGroupSplitsPerComment(t *testing.T) {
	cases := []struct {
		name string
		src  string // child-position bytes
		want []ast.Comment
	}{
		{
			name: "block then line",
			src:  "{ /* a */ // b\n\t\t}",
			want: []ast.Comment{
				{Text: "a", Block: true},
				{Text: "b", Block: false},
			},
		},
		{
			name: "two blocks",
			src:  "{ /* a */ /* b */ }",
			want: []ast.Comment{
				{Text: "a", Block: true},
				{Text: "b", Block: true},
			},
		},
		{
			name: "empty group",
			src:  "{}",
			want: []ast.Comment{
				{Text: "", Block: true},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src := "package p\n\ncomponent C() {\n\t<div>\n\t\t" + tc.src + "\n\t\t<span>ok</span>\n\t</div>\n}\n"
			f := parseStringT(t, src)
			el := f.Decls[0].(*ast.Component).Body[0].(*ast.Element)
			got := childComments(el.Children)
			if len(got) != len(tc.want) {
				t.Fatalf("got %d Comment nodes, want %d: %+v", len(got), len(tc.want), got)
			}
			for i, w := range tc.want {
				g := got[i]
				if g.Text != w.Text || g.Block != w.Block || g.Bare {
					t.Errorf("node %d = {Text:%q Block:%t Bare:%t}, want {Text:%q Block:%t Bare:false}",
						i, g.Text, g.Block, g.Bare, w.Text, w.Block)
				}
			}
		})
	}
}
