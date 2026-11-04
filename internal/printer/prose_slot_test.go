package printer

import (
	"reflect"
	"strings"
	"testing"
)

func TestMarkupSlotTextTailPadding(t *testing.T) {
	for _, tail := range []string{"word", "word ", "word  ", "word\n", "%", "word followed by a long sentence that exceeds the available attribute width"} {
		for _, middle := range []string{"<b>x</b>", "<b>\n x\n</b>", "<b>x</b>\n// source comment\n<i>y</i>"} {
			src := "package p\ncomponent C(){ <Card content={" + middle + "\n" + tail + "}/> }\n"
			for _, width := range []int{20, 80, 120} {
				got := fmtSourceWidth(t, src, width)
				if !reflect.DeepEqual(normalizedAST(t, src), normalizedAST(t, got)) {
					t.Fatalf("width %d tail %q changed content:\n%s", width, tail, got)
				}
				if twice := fmtSourceWidth(t, got, width); twice != got {
					t.Fatalf("not idempotent:\n%s\nthen:\n%s", got, twice)
				}
				if strings.Contains(got, "word  }") {
					t.Fatalf("added trailing space: %q", got)
				}
			}
		}
	}
}
