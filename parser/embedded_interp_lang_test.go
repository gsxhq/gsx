package parser

import (
	"testing"

	"github.com/gsxhq/gsx/ast"
)

func TestParseEmbeddedInterpPartLang(t *testing.T) {
	cases := []struct {
		src  string
		want ast.EmbeddedLang
	}{
		{"f`hi @{x}`", ast.EmbeddedText},
		{"js`f(@{x})`", ast.EmbeddedJS},
		{"css`color:@{x}`", ast.EmbeddedCSS},
		{`js"f(@{x})"`, ast.EmbeddedJS},
	}
	for _, tc := range cases {
		p := testParser(tc.src)
		node, err := p.parseEmbeddedInterpPart(0)
		if err != nil {
			t.Fatalf("%s: %v", tc.src, err)
		}
		if node.Lang != tc.want {
			t.Errorf("%s: Lang = %d, want %d", tc.src, node.Lang, tc.want)
		}
	}
}
