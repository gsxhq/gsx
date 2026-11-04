package corpus

import (
	"testing"

	"github.com/gsxhq/gsx/internal/rendergolden"
)

func TestRenderDiffRequiresExactWhitespace(t *testing.T) {
	want := "<p>a b</p>"
	for _, got := range []string{"<p>a  b</p>", " <p>a b</p>", "<p>a b</p>\n", "<p>a\nb</p>", "<p>a\tb</p>"} {
		if diff, err := renderDiff(got, rendergolden.Encode(want)); err != nil || diff == "" {
			t.Errorf("compare %q against %q = %q, %v; want a mismatch", got, want, diff, err)
		}
	}
	for _, html := range []string{want, want + "\n", "", "\n"} {
		if diff, err := renderDiff(html, rendergolden.Encode(html)); err != nil || diff != "" {
			t.Errorf("identical %q = %q, %v", html, diff, err)
		}
	}
}
