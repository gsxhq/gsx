package codegen

import (
	"go/token"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"golang.org/x/tools/txtar"

	"github.com/gsxhq/gsx/internal/diag"
	"github.com/gsxhq/gsx/internal/gsxfmt"
	"github.com/gsxhq/gsx/internal/wsnorm"
	"github.com/gsxhq/gsx/parser"
)

// TestFmtKeepsChildElementSpacing formats every .gsx source in the semantic
// and fmt corpora at several widths (narrow ones force breaks at nearly every
// candidate) and requires child-element-spacing to report the same warnings
// (message and neighbour, as a multiset) before and after. More means gsx fmt wrote a break
// the lint rejects (gsxhq/gsx#260); fewer means it joined an authored break
// and hid the warning. The lint runs without types, so non-literal
// interpolations are opaque to it; the formatter bonds them regardless.
func TestFmtKeepsChildElementSpacing(t *testing.T) {
	t.Parallel()
	var archives []string
	for _, pat := range []string{
		"../corpus/testdata/cases/*/*.txtar",
		"../corpus/testdata/cases/*.txtar",
		"../gsxfmt/testdata/cases/*.txtar",
	} {
		m, err := filepath.Glob(pat)
		if err != nil {
			t.Fatal(err)
		}
		archives = append(archives, m...)
	}
	if len(archives) < 100 {
		t.Fatalf("found only %d corpus archives; glob paths are stale", len(archives))
	}
	checked := 0
	for _, path := range archives {
		ar, err := txtar.ParseFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range ar.Files {
			if !strings.HasSuffix(f.Name, ".gsx") {
				continue
			}
			name := path + "/" + f.Name
			want, ok := spacingWarnings(name, f.Data)
			if !ok {
				continue // a parse-error case
			}
			for _, width := range []int{80, 40, 20, 1} {
				out, err := gsxfmt.Format(f.Name, f.Data, width)
				if err != nil {
					continue
				}
				got, ok := spacingWarnings(name, out)
				if !ok {
					t.Errorf("%s: width %d: formatted output does not parse", name, width)
					continue
				}
				if !slices.Equal(got, want) {
					t.Errorf("%s: width %d: child-element-spacing warnings differ\ninput:  %q\noutput: %q\n%s", name, width, want, got, out)
				}
				checked++
			}
		}
	}
	if checked < 1000 {
		t.Fatalf("checked only %d formatted sources", checked)
	}
}

// spacingWarnings lists the child-element-spacing messages on src, sorted,
// without types.
func spacingWarnings(name string, src []byte) ([]string, bool) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, name, src, 0)
	if err != nil {
		return nil, false
	}
	wsnorm.Normalize(f)
	var bag diag.Bag
	reportChildElementSpacing(f, src, fset, nil, funcTables{}, &bag)
	var msgs []string
	for _, d := range bag.Sorted() {
		if d.Code == diag.CodeChildElementSpacing {
			msgs = append(msgs, d.Message)
		}
	}
	slices.Sort(msgs)
	return msgs, true
}
