package corpus

import "testing"

func TestHTMLStructuralDiff(t *testing.T) {
	if d, err := htmlStructuralDiff("<p>Hi  X</p>", "<p>Hi X</p>"); err != nil || d != "" {
		t.Errorf("collapse-ws: diff=%q err=%v", d, err)
	}
	if d, err := htmlStructuralDiff(`<a id="1" class="x">y</a>`, `<a class="x" id="1">y</a>`); err != nil || d != "" {
		t.Errorf("attr-order: diff=%q err=%v", d, err)
	}
	if d, _ := htmlStructuralDiff("<p>A</p>", "<p>B</p>"); d == "" {
		t.Errorf("expected a diff for differing text")
	}
}
