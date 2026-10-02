package parser

import (
	"strings"

	"golang.org/x/net/html"
)

// decodeAttrValue returns a double-quoted attribute value's text as the browser
// reads it: character references (&amp;, &quot;, &#34;, …) decoded under the
// HTML tokenizer's attribute-value rules, which leave a legacy reference
// without ';' alone when an alphanumeric or '=' follows (?a=1&copy=2 stays
// as written). The decoding is x/net/html's own: raw is tokenized as the value
// of a double-quoted attribute, the only context its exported API decodes
// that way. raw never contains '"' — the parser ends the value there.
func decodeAttrValue(raw string) string {
	if !strings.Contains(raw, "&") {
		return raw
	}
	z := html.NewTokenizer(strings.NewReader(`<a v="` + raw + `">`))
	if z.Next() != html.StartTagToken {
		panic("parser: decodeAttrValue: synthesized tag did not tokenize as a start tag")
	}
	_, val, _ := z.TagAttr()
	return string(val)
}
