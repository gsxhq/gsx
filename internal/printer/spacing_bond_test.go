package printer

import (
	"bytes"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gsxhq/gsx/ast"
	"github.com/gsxhq/gsx/internal/txtar"
	"github.com/gsxhq/gsx/internal/wsnorm"
	"github.com/gsxhq/gsx/parser"
)

// TestFmtKeepsBondedBreaks formats every corpus source at several widths and
// requires the number of line breaks on spacing-bonded boundaries (text and
// the inline content it touches, comments and Go blocks between included) to
// stay the same: the formatter never writes such a break (gsxhq/gsx#260) and
// never removes one the author wrote in a remaining bonded run. This also
// sees breaks beside comments or Go blocks.
func TestFmtKeepsBondedBreaks(t *testing.T) {
	t.Parallel()
	srcs := corpusGsxSources(t)
	fmtCases, err := filepath.Glob("../gsxfmt/testdata/cases/*.txtar")
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range fmtCases {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range txtar.Parse(data).Files {
			if strings.HasSuffix(f.Name, ".gsx") {
				srcs = append(srcs, corpusSource{path + ":" + f.Name, string(f.Data)})
			}
		}
	}
	for _, s := range srcs {
		want, ok := bondedBreaks(s.src)
		if !ok {
			continue // a parse-error case
		}
		for _, width := range []int{80, 40, 20, 1} {
			fset := token.NewFileSet()
			f, err := parser.ParseFile(fset, "in.gsx", s.src, 0)
			if err != nil {
				t.Fatal(err)
			}
			wsnorm.Normalize(f)
			var b bytes.Buffer
			if err := Fprint(&b, f, width, 0); err != nil {
				continue
			}
			got, ok := bondedBreaks(b.String())
			if !ok {
				t.Errorf("%s: width %d: formatted output does not parse", s.name, width)
				continue
			}
			if got != want {
				t.Errorf("%s: width %d: %d line break(s) on bonded boundaries in the input, %d in the output:\n%s", s.name, width, want, got, b.String())
			}
		}
	}
}

// bondedBreaks counts the boundaries in src's normalized children lists that
// lie in a lost-space run and whose right-hand node carries an authored line
// break.
func bondedBreaks(src string) (int, bool) {
	f, err := parser.ParseFile(token.NewFileSet(), "x.gsx", src, 0)
	if err != nil {
		return 0, false
	}
	wsnorm.Normalize(f)
	n := 0
	var list func(nodes []ast.Markup, preserve bool)
	var attrs func(as []ast.Attr)
	list = func(nodes []ast.Markup, preserve bool) {
		if !preserve {
			for i, b := range lostSpaceRuns(nodes) {
				if b && ast.LeadingBreak(nodes[i]) {
					n++
				}
			}
		}
		for _, m := range nodes {
			switch v := m.(type) {
			case *ast.Element:
				attrs(v.Attrs)
				list(v.Children, preserve || wsnorm.IsPreserveTag(v.Tag))
			case *ast.Fragment:
				list(v.Children, preserve)
			case *ast.MarkerRegion:
				list(v.Children, preserve)
			case *ast.IfMarkup:
				list(v.Then, preserve)
				list(v.Else, preserve)
			case *ast.ForMarkup:
				list(v.Body, preserve)
			case *ast.SwitchMarkup:
				for _, c := range v.Cases {
					list(c.Body, preserve)
				}
			}
		}
	}
	attrs = func(as []ast.Attr) {
		for _, a := range as {
			if m, ok := a.(*ast.MarkupAttr); ok {
				list(m.Value, false)
			}
		}
	}
	for _, d := range f.Decls {
		if c, ok := d.(*ast.Component); ok {
			list(c.Body, false)
		}
	}
	return n, true
}

// lostSpaceRuns marks source boundaries with no normalized separator. The
// formatter bonds additional spaced boundaries to retain whitespace placement
// across comments, but those may flatten an authored break without changing text.
func lostSpaceRuns(nodes []ast.Markup) []bool {
	in := make([]bool, len(nodes))
	for i, node := range nodes {
		text, ok := node.(*ast.Text)
		if !ok || strings.Trim(text.Value, " ") == "" {
			continue
		}
		for _, dir := range []int{-1, 1} {
			if dir < 0 && strings.HasPrefix(text.Value, " ") || dir > 0 && strings.HasSuffix(text.Value, " ") {
				continue
			}
			j, ok := wsnorm.TextBoundarySibling(nodes, i, dir)
			if !ok {
				continue
			}
			if other, ok := nodes[j].(*ast.Text); ok {
				if dir < 0 && strings.HasSuffix(other.Value, " ") || dir > 0 && strings.HasPrefix(other.Value, " ") {
					continue
				}
			}
			from, to := i, j
			if from > to {
				from, to = to, from
			}
			for k := from + 1; k <= to; k++ {
				in[k] = true
			}
		}
	}
	return in
}
