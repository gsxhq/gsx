package corpus

import (
	"slices"
	"strings"
	"testing"
)

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

// TestHTMLStructuralDiffDuplicateAttr pins that a repeated attribute name is a
// divergence even when the first occurrence matches: the HTML tokenizer keeps
// only the first, so the tree comparison alone would pass it.
func TestHTMLStructuralDiffDuplicateAttr(t *testing.T) {
	for _, tc := range []struct{ got, want, wantDiff string }{
		{`<script nonce="a" nonce="b"></script>`, `<script nonce="a"></script>`, `got: duplicate attribute "nonce"`},
		{`<div data-d="caller" data-d="z!"></div>`, `<div data-d="caller"></div>`, `got: duplicate attribute "data-d"`},
		{`<p ID=1 id=2>x</p>`, `<p id=1>x</p>`, `got: duplicate attribute "id"`},
		{`<input disabled disabled/>`, `<input disabled/>`, `got: duplicate attribute "disabled"`},
		{`<p a="1">x</p>`, `<p a="1" a="2">x</p>`, `want: duplicate attribute "a"`},
	} {
		d, err := htmlStructuralDiff(tc.got, tc.want)
		if err != nil {
			t.Fatalf("%s: %v", tc.got, err)
		}
		if !strings.HasPrefix(d, tc.wantDiff) {
			t.Errorf("%s vs %s: diff=%q, want prefix %q", tc.got, tc.want, d, tc.wantDiff)
		}
	}
	// Not duplicates: distinct names, a repeated name inside a value, and
	// attribute-like text inside raw-text elements.
	for _, src := range []string{
		`<a href="x" title="href=y">z</a>`,
		`<script>var s = '<p a=1 a=2>';</script>`,
		`<p a='1"' b="2'" c=3/>`,
	} {
		if d, err := htmlStructuralDiff(src, src); err != nil || d != "" {
			t.Errorf("%s: diff=%q err=%v", src, d, err)
		}
	}
}

func TestRawTagAttrNames(t *testing.T) {
	for raw, want := range map[string][]string{
		`<p>`:                   nil,
		`<p/>`:                  nil,
		`<p a b=1 c='x' d="y">`: {"a", "b", "c", "d"},
		`<p A="1"B=2 /c>`:       {"a", "b", "c"},
		`<p a = "1" =b>`:        {"a", "=b"},
		`<p x="a>b" y=z/>`:      {"x", "y"},
		`<p a="1"/ b>`:          {"a", "b"},
		"<p\ta\nb\fc>":          {"a", "b", "c"},
	} {
		if got := rawTagAttrNames(raw); !slices.Equal(got, want) {
			t.Errorf("rawTagAttrNames(%q) = %q, want %q", raw, got, want)
		}
	}
}
