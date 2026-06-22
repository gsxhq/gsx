package gsx

import (
	"bytes"
	"io"
	"strings"
	"unicode/utf8"
)

// htmlReplacer escapes the bytes unsafe in HTML text and double-quoted attribute
// contexts. The entity set matches html/template.HTMLEscapeString (text/template
// HTMLEscape), which replaces NUL with U+FFFD per the HTML5 tokenizer rule that
// rewrites U+0000 to U+FFFD on a parse error
// (https://html.spec.whatwg.org/multipage/parsing.html#data-state).
var htmlReplacer = strings.NewReplacer(
	"\x00", "�",
	"&", "&amp;",
	"<", "&lt;",
	">", "&gt;",
	`"`, "&#34;",
	"'", "&#39;",
)

// writeHTML streams s to w with HTML text/attribute escaping. strings.Replacer
// writes safe runs directly, so this allocates only for the (rare) entity spans.
func writeHTML(w io.Writer, s string) error {
	_, err := htmlReplacer.WriteString(w, s)
	return err
}

// blockedURL replaces a URL whose scheme is not allow-listed (mirrors
// html/template's #ZgotmplZ sentinel for an unsafe URL).
const blockedURL = "about:invalid#gsx"

// urlSanitize returns s unchanged when it is relative/fragment/query or carries
// an allow-listed scheme (http, https, mailto, tel — case-insensitive); any
// other scheme (javascript:, vbscript:, data:, …) yields blockedURL.
func urlSanitize(s string) string {
	if i := strings.IndexByte(s, ':'); i >= 0 {
		// A scheme exists only when no '/', '?', or '#' precedes the ':'.
		if !strings.ContainsAny(s[:i], "/?#") {
			switch strings.ToLower(s[:i]) {
			case "http", "https", "mailto", "tel":
				// allowed
			default:
				return blockedURL
			}
		}
	}
	return s
}

// writeURL streams a sanitized, attribute-escaped URL value to w.
func writeURL(w io.Writer, s string) error {
	return writeHTML(w, urlSanitize(s))
}

// cssFailsafe replaces a CSS value that could break out of its context. It
// mirrors html/template's "ZgotmplZ" sentinel — a deliberately inert identifier
// that renders harmlessly.
const cssFailsafe = "ZgotmplZ"

var cssExpressionBytes = []byte("expression")
var cssMozBindingBytes = []byte("mozbinding")

// cssValueFilter returns s when it is a safe CSS value, else cssFailsafe. It is a
// port of the standard library's html/template/css.go cssValueFilter (and its
// helpers decodeCSS/isCSSNmchar/skipCSSSpace/hexDecode/isHex), with the typed-
// string machinery removed: gsx always passes an untrusted plain string here.
// It conservatively rejects any value containing 0x00 " ' ( ) / ; @ [ \ ] ` { }
// < >, a -- run, or (after decoding+lowercasing) "expression"/"mozbinding".
func cssValueFilter(s string) string {
	b, id := decodeCSS([]byte(s)), make([]byte, 0, 64)
	for i, c := range b {
		switch c {
		case 0, '"', '\'', '(', ')', '/', ';', '@', '[', '\\', ']', '`', '{', '}', '<', '>':
			return cssFailsafe
		case '-':
			if i != 0 && b[i-1] == '-' {
				return cssFailsafe
			}
		default:
			if c < utf8.RuneSelf && isCSSNmchar(rune(c)) {
				id = append(id, c)
			}
		}
	}
	id = bytes.ToLower(id)
	if bytes.Contains(id, cssExpressionBytes) || bytes.Contains(id, cssMozBindingBytes) {
		return cssFailsafe
	}
	return string(b)
}

// isCSSNmchar reports whether r is a CSS3 nmchar (ignoring multi-rune escapes).
func isCSSNmchar(r rune) bool {
	return 'a' <= r && r <= 'z' ||
		'A' <= r && r <= 'Z' ||
		'0' <= r && r <= '9' ||
		r == '-' || r == '_' ||
		0x80 <= r && r <= 0xd7ff ||
		0xe000 <= r && r <= 0xfffd ||
		0x10000 <= r && r <= 0x10ffff
}

// decodeCSS decodes CSS3 escape sequences in s.
func decodeCSS(s []byte) []byte {
	i := bytes.IndexByte(s, '\\')
	if i == -1 {
		return s
	}
	b := make([]byte, 0, len(s))
	for len(s) != 0 {
		i := bytes.IndexByte(s, '\\')
		if i == -1 {
			i = len(s)
		}
		b, s = append(b, s[:i]...), s[i:]
		if len(s) < 2 {
			break
		}
		if isHex(s[1]) {
			j := 2
			for j < len(s) && j < 7 && isHex(s[j]) {
				j++
			}
			r := hexDecode(s[1:j])
			if r > utf8.MaxRune {
				r, j = r/16, j-1
			}
			n := utf8.EncodeRune(b[len(b):cap(b)], r)
			b, s = b[:len(b)+n], skipCSSSpace(s[j:])
		} else {
			_, n := utf8.DecodeRune(s[1:])
			b, s = append(b, s[1:1+n]...), s[1+n:]
		}
	}
	return b
}

func isHex(c byte) bool {
	return '0' <= c && c <= '9' || 'a' <= c && c <= 'f' || 'A' <= c && c <= 'F'
}

func hexDecode(s []byte) rune {
	n := '\x00'
	for _, c := range s {
		n <<= 4
		switch {
		case '0' <= c && c <= '9':
			n |= rune(c - '0')
		case 'a' <= c && c <= 'f':
			n |= rune(c-'a') + 10
		case 'A' <= c && c <= 'F':
			n |= rune(c-'A') + 10
		default:
			panic("bad hex digit")
		}
	}
	return n
}

func skipCSSSpace(c []byte) []byte {
	if len(c) == 0 {
		return c
	}
	switch c[0] {
	case '\t', '\n', '\f', ' ':
		return c[1:]
	case '\r':
		if len(c) >= 2 && c[1] == '\n' {
			return c[2:]
		}
		return c[1:]
	}
	return c
}

// FilterCSS returns s when it is a safe CSS value, else a harmless inert
// placeholder. Generated code wraps each DYNAMIC composed-style declaration value
// in FilterCSS so untrusted data cannot inject declarations or break out of the
// style attribute. A trusted string-literal declaration is emitted without it.
func FilterCSS(s string) string { return cssValueFilter(s) }
