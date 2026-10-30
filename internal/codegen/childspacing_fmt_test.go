package codegen

import (
	"go/token"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/tools/txtar"

	"github.com/gsxhq/gsx/internal/diag"
	"github.com/gsxhq/gsx/internal/gsxfmt"
	"github.com/gsxhq/gsx/internal/wsnorm"
	"github.com/gsxhq/gsx/parser"
)

// TestFmtKeepsChildElementSpacingCount formats every .gsx source in the
// semantic and fmt corpora at several widths (narrow ones force breaks at
// nearly every candidate) and requires child-element-spacing to report the
// same number of warnings before and after. More means gsx fmt wrote a break
// the lint rejects (gsxhq/gsx#260); fewer means it joined an authored break
// and hid the warning. The lint runs without types, so non-literal
// interpolations are opaque to it; the formatter bonds them regardless.
func TestFmtKeepsChildElementSpacingCount(t *testing.T) {
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
				if got != want {
					t.Errorf("%s: width %d: child-element-spacing reports %d warning(s) on the input but %d on the formatted output:\n%s", name, width, want, got, out)
				}
				checked++
			}
		}
	}
	if checked < 1000 {
		t.Fatalf("checked only %d formatted sources", checked)
	}
}

// spacingWarnings counts child-element-spacing warnings on src, without types.
func spacingWarnings(name string, src []byte) (int, bool) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, name, src, 0)
	if err != nil {
		return 0, false
	}
	wsnorm.Normalize(f)
	var bag diag.Bag
	reportChildElementSpacing(f, src, fset, nil, funcTables{}, &bag)
	n := 0
	for _, d := range bag.Sorted() {
		if d.Code == diag.CodeChildElementSpacing {
			n++
		}
	}
	return n, true
}
