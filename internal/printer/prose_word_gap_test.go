package printer

import (
	"reflect"
	"strings"
	"testing"
)

func TestProseSpacesBesidePunctuationMayWrap(t *testing.T) {
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
			if width == 20 && strings.Contains(got, text) {
				t.Fatalf("prose space did not wrap at width%d: %q\n%s", width, text, got)
			}
		}
	}
}

func TestInlineWhitespaceThroughEdgeComments(t *testing.T) {
	for _, body := range []string{
		"<p>  {/*a*/}  {/*b*/}   </p>",
		"<p> {/*a*/} </p>",
		"<p><!--a--> Hello</p>",
		"<p>Hello <!--a--></p>",
		"<p>{/*a*/} Hello</p>",
		"<p>Hello {/*a*/}</p>",
		"<p><!--a--> {/*b*/} Hello {/*c*/}<!--d--></p>",
	} {
		for _, width := range []int{1, 20, 80} {
			src := "package p\ncomponent C(){ " + body + " }\n"
			got := fmtSourceWidth(t, src, width)
			if !reflect.DeepEqual(normalizedAST(t, src), normalizedAST(t, got)) {
				t.Fatalf("width%d body%q changed normalized output:\n%s", width, body, got)
			}
			if twice := fmtSourceWidth(t, got, width); twice != got {
				t.Fatalf("width%d not idempotent:\n%s\nthen:\n%s", width, got, twice)
			}
		}
	}
}
