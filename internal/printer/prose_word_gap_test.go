package printer

import (
	"reflect"
	"strings"
	"testing"
)

func TestExplicitProseSpaceNeverBecomesAttachingBreak(t *testing.T) {
	word := strings.Repeat("x", 35)
	for _, text := range []string{
		word + " !", word + " %", "( " + word, word + " …", word + " &#37;",
		"&lpar; " + word, word + " &hellip;", word + "&nbsp; y", "x &nbsp;" + word,
		word + "\u00a0 y", "x \u3000" + word,
	} {
		for _, width := range []int{20, 80} {
			src := "package p\ncomponent C(){ <p>" + text + "</p> }\n"
			got := fmtSourceWidth(t, src, width)
			if !reflect.DeepEqual(normalizedAST(t, src), normalizedAST(t, got)) {
				t.Fatalf("width%d text%q changed normalized output:\n%s", width, text, got)
			}
			if twice := fmtSourceWidth(t, got, width); twice != got {
				t.Fatalf("not idempotent:\n%s\nthen:\n%s", got, twice)
			}
			if !strings.Contains(got, text) {
				t.Fatalf("explicit space was broken at width%d: %q\n%s", width, text, got)
			}
		}
	}
}
