package corpus

import (
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strings"

	"golang.org/x/net/html"
)

// htmlStructuralDiff compares two HTML documents structurally (attribute order
// and insignificant whitespace ignored). A duplicate attribute name on any
// element of either side is itself a divergence: the HTML tokenizer keeps only
// the first occurrence, so the tree comparison alone would never see the second.
func htmlStructuralDiff(got, want string) (string, error) {
	for _, side := range []struct{ name, src string }{{"got", got}, {"want", want}} {
		dup, err := duplicateAttr(side.src)
		if err != nil {
			return "", fmt.Errorf("scan %s HTML: %w", side.name, err)
		}
		if dup != "" {
			return fmt.Sprintf("%s: %s", side.name, dup), nil
		}
	}
	gotTree, err := html.Parse(strings.NewReader(got))
	if err != nil {
		return "", fmt.Errorf("parse got HTML: %w", err)
	}
	wantTree, err := html.Parse(strings.NewReader(want))
	if err != nil {
		return "", fmt.Errorf("parse want HTML: %w", err)
	}
	return compareNodes(gotTree, wantTree), nil
}

var wsRun = regexp.MustCompile(`\s+`)

// compareNodes structurally compares two parsed HTML nodes. It returns "" when
// equal, or a human-readable description of the first divergence.
func compareNodes(a, b *html.Node) string {
	if a.Type != b.Type {
		return fmt.Sprintf("node type %v != %v", a.Type, b.Type)
	}
	switch a.Type {
	case html.ElementNode:
		if a.Data != b.Data {
			return fmt.Sprintf("tag <%s> != <%s>", a.Data, b.Data)
		}
		if as, bs := attrSet(a), attrSet(b); as != bs {
			return fmt.Sprintf("<%s> attrs %q != %q", a.Data, as, bs)
		}
	case html.TextNode:
		at, bt := strings.TrimSpace(collapseWS(a.Data)), strings.TrimSpace(collapseWS(b.Data))
		if at != bt {
			return fmt.Sprintf("text %q != %q", at, bt)
		}
	case html.CommentNode, html.DoctypeNode:
		if a.Data != b.Data {
			return fmt.Sprintf("%v data %q != %q", a.Type, a.Data, b.Data)
		}
	}

	ac, bc := significantChildren(a), significantChildren(b)
	if len(ac) != len(bc) {
		return fmt.Sprintf("<%s> child count %d != %d", nodeLabel(a), len(ac), len(bc))
	}
	for i := range ac {
		if diff := compareNodes(ac[i], bc[i]); diff != "" {
			return diff
		}
	}
	return ""
}

// significantChildren returns a node's children, dropping whitespace-only text
// nodes that sit between elements (insignificant formatting whitespace).
func significantChildren(n *html.Node) []*html.Node {
	var out []*html.Node
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.TextNode && strings.TrimSpace(c.Data) == "" {
			continue
		}
		out = append(out, c)
	}
	return out
}

// attrSet renders a node's attributes as a sorted, comparable key=value set.
func attrSet(n *html.Node) string {
	parts := make([]string, 0, len(n.Attr))
	for _, a := range n.Attr {
		key := a.Key
		if a.Namespace != "" {
			key = a.Namespace + ":" + a.Key
		}
		parts = append(parts, key+"="+a.Val)
	}
	slices.Sort(parts)
	return strings.Join(parts, " ")
}

func collapseWS(s string) string {
	return wsRun.ReplaceAllString(s, " ")
}

func nodeLabel(n *html.Node) string {
	if n.Type == html.ElementNode {
		return n.Data
	}
	return fmt.Sprintf("node(type=%d)", n.Type)
}

// duplicateAttr reports the first start tag in src that carries the same
// attribute name twice, or "" when there is none. The html.Tokenizer drops
// repeated names while tokenizing, so it is used only to find start tags (it
// tracks raw-text elements such as <script>); each tag's raw bytes are then
// re-scanned for attribute names.
func duplicateAttr(src string) (string, error) {
	z := html.NewTokenizer(strings.NewReader(src))
	for {
		switch z.Next() {
		case html.ErrorToken:
			if err := z.Err(); !errors.Is(err, io.EOF) {
				return "", err
			}
			return "", nil
		case html.StartTagToken, html.SelfClosingTagToken:
			raw := string(z.Raw())
			names := rawTagAttrNames(raw)
			seen := make(map[string]bool, len(names))
			for _, n := range names {
				if seen[n] {
					return fmt.Sprintf("duplicate attribute %q in %s", n, raw), nil
				}
				seen[n] = true
			}
		}
	}
}

// rawTagAttrNames returns the attribute names of one raw start tag ("<tag
// ...>"), in source order and including repeats, following the WHATWG
// tokenizer's tag-name, attribute-name and attribute-value states. Names are
// ASCII-lowercased as the tokenizer does.
func rawTagAttrNames(raw string) []string {
	const (
		tagName = iota
		beforeName
		inName
		afterName
		beforeValue
		dqValue
		sqValue
		unquotedValue
		afterQuotedValue
		selfClosing
	)
	isSpace := func(c byte) bool { return c == '\t' || c == '\n' || c == '\f' || c == ' ' || c == '\r' }
	var names []string
	var cur []byte
	flush := func() {
		if cur != nil {
			names = append(names, string(cur))
			cur = nil
		}
	}
	lower := func(c byte) byte {
		if 'A' <= c && c <= 'Z' {
			return c + 'a' - 'A'
		}
		return c
	}
	state := tagName
	for i := 1; i < len(raw); i++ { // raw[0] is '<'
		c := raw[i]
	reconsume:
		switch state {
		case tagName:
			switch {
			case isSpace(c):
				state = beforeName
			case c == '/':
				state = selfClosing
			case c == '>':
				return names
			}
		case beforeName:
			switch {
			case isSpace(c):
			case c == '/' || c == '>':
				state = afterName
				goto reconsume
			default: // includes '=', which starts a name of "="
				flush()
				cur = []byte{lower(c)}
				state = inName
			}
		case inName:
			switch {
			case isSpace(c) || c == '/' || c == '>':
				state = afterName
				goto reconsume
			case c == '=':
				state = beforeValue
			default:
				cur = append(cur, lower(c))
			}
		case afterName:
			switch {
			case isSpace(c):
			case c == '/':
				state = selfClosing
			case c == '=':
				state = beforeValue
			case c == '>':
				flush()
				return names
			default:
				flush()
				cur = []byte{lower(c)}
				state = inName
			}
		case beforeValue:
			switch {
			case isSpace(c):
			case c == '"':
				state = dqValue
			case c == '\'':
				state = sqValue
			case c == '>':
				flush()
				return names
			default:
				state = unquotedValue
			}
		case dqValue:
			if c == '"' {
				state = afterQuotedValue
			}
		case sqValue:
			if c == '\'' {
				state = afterQuotedValue
			}
		case unquotedValue:
			switch {
			case isSpace(c):
				state = beforeName
			case c == '>':
				flush()
				return names
			}
		case afterQuotedValue:
			switch {
			case isSpace(c):
				state = beforeName
			case c == '/':
				state = selfClosing
			case c == '>':
				flush()
				return names
			default:
				state = beforeName
				goto reconsume
			}
		case selfClosing:
			if c == '>' {
				flush()
				return names
			}
			state = beforeName
			goto reconsume
		}
	}
	flush()
	return names
}
