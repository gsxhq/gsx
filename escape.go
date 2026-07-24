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
	if before, _, ok := strings.Cut(s, ":"); ok {
		// A scheme exists only when no '/', '?', or '#' precedes the ':'.
		if !strings.ContainsAny(before, "/?#") {
			switch strings.ToLower(before) {
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

// imageDataMIMEs is the allow-list of data: MIME types permitted in an image
// resource sink (SinkImage). Raster types are inert pixels; image/svg+xml is
// safe HERE because SinkImage is only <img>/<source>/poster/background, where
// browsers load SVG in restricted mode (no script, no external fetch). It is
// NOT permitted on iframe/object/embed/script sinks, which never reach this
// sanitizer (codegen routes them through urlSanitize). Keys are lowercase.
var imageDataMIMEs = map[string]bool{
	"image/png": true, "image/jpeg": true, "image/gif": true,
	"image/webp": true, "image/avif": true, "image/svg+xml": true,
}

// isImageDataURL reports whether s is a data: URL whose MIME is in the image
// allow-list, in one of two accepted encodings (parsed conservatively):
//
//   - base64: data:<mime>[;param]*;base64,<payload> — the ";base64," marker
//     must be the final meta parameter.
//   - percent-encoded: data:<mime>[;charset=utf-8],<payload> — the RFC 2397
//     readable authoring form, accepted only when the payload strictly
//     validates via isPercentSafePayload (printable ASCII, well-formed %XX
//     escapes). Any other meta parameter rejects.
//
// Neither encoding is what makes the value safe here: the guarantees are the
// image-MIME allow-list plus browsers loading image resource sinks in
// restricted mode (see imageDataMIMEs), and both apply equally to either
// payload encoding. The strict payload validation exists to keep the parse
// conservative, not to constrain what the bytes decode to.
func isImageDataURL(s string) bool {
	const prefix = "data:"
	if len(s) < len(prefix) || !strings.EqualFold(s[:len(prefix)], prefix) {
		return false
	}
	rest := s[len(prefix):]
	meta, payload, ok := strings.Cut(rest, ",") // e.g. "image/png;base64" or "image/svg+xml"
	if !ok {
		return false
	}
	mimePart, params, hasParams := strings.Cut(meta, ";")
	if !imageDataMIMEs[strings.ToLower(mimePart)] {
		return false
	}
	if !hasParams {
		return isPercentSafePayload(payload)
	}
	// base64 form: the meta parameters must include base64 as the final token.
	if strings.EqualFold(params, "base64") ||
		strings.HasSuffix(strings.ToLower(params), ";base64") {
		return true
	}
	// percent-encoded form: charset=utf-8 is the only permitted parameter.
	if strings.EqualFold(params, "charset=utf-8") {
		return isPercentSafePayload(payload)
	}
	return false
}

// isPercentSafePayload reports whether p is a strictly valid plain-text data:
// URL payload: every byte is printable ASCII (0x20–0x7E) and every '%' begins
// a well-formed two-hex-digit escape. Percent-encoding is optional for other
// bytes — any printable ASCII byte may appear literally. Stray '%', control
// bytes, and raw non-ASCII bytes reject (non-ASCII content must be
// percent-encoded).
func isPercentSafePayload(p string) bool {
	for i := 0; i < len(p); i++ {
		c := p[i]
		if c == '%' {
			if i+2 >= len(p) || !isHex(p[i+1]) || !isHex(p[i+2]) {
				return false
			}
			i += 2
			continue
		}
		if c < 0x20 || c > 0x7e {
			return false
		}
	}
	return true
}

// urlSanitizeImage is urlSanitize for an image RESOURCE sink: it accepts the
// same relative/fragment/query and http/https/mailto/tel values, and ALSO
// accepts a data: URL whose MIME is in the image allow-list. Every other scheme
// (including non-image data:) yields blockedURL.
func urlSanitizeImage(s string) string {
	if before, _, ok := strings.Cut(s, ":"); ok {
		if !strings.ContainsAny(before, "/?#") {
			switch strings.ToLower(before) {
			case "http", "https", "mailto", "tel":
				// allowed
			case "data":
				if isImageDataURL(s) {
					return s
				}
				return blockedURL
			default:
				return blockedURL
			}
		}
	}
	return s
}

// writeURLImage streams an image-resource-sanitized, attribute-escaped URL to w.
func writeURLImage(w io.Writer, s string) error {
	return writeHTML(w, urlSanitizeImage(s))
}

// srcsetSanitize sanitizes a srcset attribute value using the WHATWG srcset
// grammar: a comma-separated list of image candidates ("url [descriptor]").
// Each candidate's URL is the run of non-whitespace bytes (commas inside a URL —
// a data: URL's ";base64," separator, a query's "?a=1,2" — stay part of the
// URL); a run's trailing commas are candidate boundaries; the rest up to the
// next comma is an inert descriptor. Each URL is sanitized as an image resource
// (urlSanitizeImage); a blocked URL collapses its whole candidate to blockedURL.
// The descriptor needs no validation — writeSrcset HTML-escapes the whole
// result, so descriptor content can never break out of the attribute. This is a
// faithful port of the WHATWG grammar, not html/template's srcset code (which
// over-blocks valid inputs like "a.jpg 1.5x" and mangles data: URLs).
func srcsetSanitize(s string) string {
	var b strings.Builder
	i := 0
	for i < len(s) {
		// 1. Candidate separators (leading whitespace + commas) copied verbatim.
		sep := i
		for i < len(s) && (isASCIIWhitespaceByte(s[i]) || s[i] == ',') {
			i++
		}
		b.WriteString(s[sep:i])
		if i >= len(s) {
			break
		}
		// 2. URL = run of non-whitespace bytes (commas inside a URL stay).
		urlStart := i
		for i < len(s) && !isASCIIWhitespaceByte(s[i]) {
			i++
		}
		run := s[urlStart:i]
		url := strings.TrimRight(run, ",") // trailing commas are boundaries
		i -= len(run) - len(url)           // re-consume them as separators
		// 3. Descriptor: rest up to the next comma, only when the URL run had no
		//    trailing-comma boundary. Inert (HTML-escaped downstream).
		descStart := i
		if len(url) == len(run) {
			for i < len(s) && s[i] != ',' {
				i++
			}
		}
		desc := s[descStart:i]
		// 4. A blocked URL collapses the whole candidate; else URL + descriptor.
		if urlSanitizeImage(url) == blockedURL {
			b.WriteString(blockedURL)
		} else {
			b.WriteString(url)
			b.WriteString(desc)
		}
	}
	return b.String()
}

// writeSrcset streams a sanitized, attribute-escaped srcset value to w.
func writeSrcset(w io.Writer, s string) error {
	return writeHTML(w, srcsetSanitize(s))
}

func isASCIIWhitespaceByte(c byte) bool {
	switch c {
	case '\t', '\n', '\f', '\r', ' ':
		return true
	default:
		return false
	}
}

func skipASCIIWhitespace(s string, i int) int {
	for i < len(s) && isASCIIWhitespaceByte(s[i]) {
		i++
	}
	return i
}

// refreshContentSanitize parses a meta refresh content value with the WHATWG
// "shared declarative refresh steps" grammar and applies urlSanitize to the
// embedded redirect URL. Values that don't parse as a refresh directive with a
// URL are returned unchanged.
func refreshContentSanitize(s string) string {
	i := skipASCIIWhitespace(s, 0)
	startTime := i
	for i < len(s) && (('0' <= s[i] && s[i] <= '9') || s[i] == '.') {
		i++
	}
	if i == startTime || i >= len(s) {
		return s
	}
	if s[i] != ';' && s[i] != ',' && !isASCIIWhitespaceByte(s[i]) {
		return s
	}
	i = skipASCIIWhitespace(s, i)
	if i < len(s) && (s[i] == ';' || s[i] == ',') {
		i++
	}
	i = skipASCIIWhitespace(s, i)
	if i >= len(s) {
		return s
	}

	if i+3 <= len(s) && strings.EqualFold(s[i:i+3], "url") {
		j := skipASCIIWhitespace(s, i+3)
		if j < len(s) && s[j] == '=' {
			i = skipASCIIWhitespace(s, j+1)
		}
	}

	if i >= len(s) {
		return s
	}
	urlStart, urlEnd := i, len(s)
	if s[i] == '\'' || s[i] == '"' {
		quote := s[i]
		urlStart++
		urlEnd = urlStart
		for urlEnd < len(s) && s[urlEnd] != quote {
			urlEnd++
		}
	}
	url := s[urlStart:urlEnd]
	if sanitized := urlSanitize(url); sanitized != url {
		return s[:urlStart] + sanitized + s[urlEnd:]
	}
	return s
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
	found := bytes.Contains(s, []byte{'\\'})
	if !found {
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
