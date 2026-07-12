package gsx

// JS interpolation escapers for <script> contexts, ported faithfully from the
// Go standard library's html/template/js.go (jsValEscaper, jsStrEscaper,
// jsTmplLitEscaper, jsRegexpEscaper and their helpers). The runtime is
// stdlib-only by design: nothing here imports a third-party package.
//
// The html/template-internal typed-string machinery is dropped; the sole
// passthrough kept is gsx.RawJS (the analogue of template.JS) in JSVal, plus
// the stdlib's fmt.Stringer / json.Marshaler handling.
//
// Note on string literals: many replacement values are JS unicode escapes (for
// example the six-byte sequence backslash-u-0-0-0-0). These are written as
// double-quoted Go literals with the backslash escaped ("\\u0000") so the
// runtime string matches the stdlib's raw-string tables byte for byte.

import (
	"encoding/json"
	"fmt"
	"reflect"
	"regexp"
	"strings"
	"unicode/utf8"
)

// EscapeJSVal returns v escaped for a JavaScript value context.
func EscapeJSVal(v any) string { return jsValEscaper(v) }

// EscapeJSStr returns s escaped for the interior of a JavaScript string literal.
func EscapeJSStr(s string) string { return jsStrString(s) }

// EscapeJSTmpl returns s escaped for the text portion of a JavaScript template literal.
func EscapeJSTmpl(s string) string { return jsTmplString(s) }

// EscapeJSRegexp returns s escaped for a JavaScript regular-expression literal.
func EscapeJSRegexp(s string) string { return jsRegexpString(s) }

// JSVal writes v into a JS value context (e.g. `var x = <here>`), as the
// stdlib's jsValEscaper does. A gsx.RawJS value is emitted verbatim; everything
// else is JSON-marshaled with the </script>, */, <!-- and U+2028/U+2029
// defenses. On a marshal error the comment-safe failsafe string is emitted.
func (gw *Writer) JSVal(v any) {
	if gw.err != nil {
		return
	}
	gw.writeStr(EscapeJSVal(v))
}

// JSValAttr writes v into a JS value context inside an HTML attribute (e.g.
// x-data="<here>"). It applies JS-value escaping first, then HTML-attribute
// escaping, so the result is safe in both JS and HTML attribute contexts.
// A gsx.RawJS value is emitted as raw JS but still HTML-attribute-escaped.
func (gw *Writer) JSValAttr(v any) {
	if gw.err != nil {
		return
	}
	gw.writeStr(htmlReplacer.Replace(EscapeJSVal(v)))
}

// jsStrString returns the JS-string-escaped form of s (the same logic used by
// JSStr), without writing to a Writer.
func jsStrString(s string) string { return replace(s, jsStrReplacementTable) }

// JSStr writes s into the interior of a JS string literal (between quotes), or
// an HTML event-handler attribute, as the stdlib's jsStrEscaper does for a
// plain string.
func (gw *Writer) JSStr(s string) {
	if gw.err != nil {
		return
	}
	gw.writeStr(EscapeJSStr(s))
}

// JSStrAttr writes s into a JS string literal inside an HTML attribute. It
// applies JS-string escaping first, then HTML-attribute escaping.
func (gw *Writer) JSStrAttr(s string) {
	if gw.err != nil {
		return
	}
	gw.writeStr(htmlReplacer.Replace(EscapeJSStr(s)))
}

// jsTmplString returns the JS-template-literal-escaped form of s (the same
// logic used by JSTmpl), without writing to a Writer.
func jsTmplString(s string) string { return replace(s, jsBqStrReplacementTable) }

// JSTmpl writes s into the text portion of a JS template literal (between
// backticks), as the stdlib's jsTmplLitEscaper does.
func (gw *Writer) JSTmpl(s string) {
	if gw.err != nil {
		return
	}
	gw.writeStr(EscapeJSTmpl(s))
}

// JSTmplAttr writes s into a JS template literal inside an HTML attribute. It
// applies JS-template-literal escaping first, then HTML-attribute escaping.
func (gw *Writer) JSTmplAttr(s string) {
	if gw.err != nil {
		return
	}
	gw.writeStr(htmlReplacer.Replace(EscapeJSTmpl(s)))
}

// jsRegexpString returns the JS-regexp-escaped form of s (the same logic used
// by JSRegexp), without writing to a Writer.
func jsRegexpString(s string) string {
	s = replace(s, jsRegexpReplacementTable)
	if s == "" {
		s = "(?:)"
	}
	return s
}

// JSRegexp writes s into a JS regular-expression literal so it is matched
// literally, as the stdlib's jsRegexpEscaper does. An empty input yields
// "(?:)" so that /<here>/ is not parsed as a line comment.
func (gw *Writer) JSRegexp(s string) {
	if gw.err != nil {
		return
	}
	gw.writeStr(EscapeJSRegexp(s))
}

// JSRegexpAttr writes s into a JS regexp literal inside an HTML attribute. It
// applies JS-regexp escaping first, then HTML-attribute escaping.
func (gw *Writer) JSRegexpAttr(s string) {
	if gw.err != nil {
		return
	}
	gw.writeStr(htmlReplacer.Replace(EscapeJSRegexp(s)))
}

var jsonMarshalType = reflect.TypeFor[json.Marshaler]()

// indirectToJSONMarshaler returns the value, after dereferencing as many times
// as necessary to reach the base type (or nil) or an implementation of
// json.Marshaler.
func indirectToJSONMarshaler(a any) any {
	if a == nil {
		return nil
	}

	v := reflect.ValueOf(a)
	for !v.Type().Implements(jsonMarshalType) && v.Kind() == reflect.Pointer && !v.IsNil() {
		v = v.Elem()
	}
	return v.Interface()
}

var scriptTagRe = regexp.MustCompile("(?i)<(/?)script")

// jsValEscaper escapes its input to a JS Expression that has neither
// side-effects nor free variables outside (NaN, Infinity).
func jsValEscaper(arg any) string {
	a := indirectToJSONMarshaler(arg)
	switch t := a.(type) {
	case RawJS:
		return string(t)
	case json.Marshaler:
		// Do not treat as a Stringer.
	case fmt.Stringer:
		a = t.String()
	}
	// TODO: detect cycles before calling Marshal which loops infinitely on
	// cyclic data. This may be an unacceptable DoS risk.
	b, err := json.Marshal(a)
	if err != nil {
		// While the standard JSON marshaler does not include user controlled
		// information in the error message, if a type has a MarshalJSON method,
		// the content of the error message is not guaranteed. Since we insert
		// the error into the template, as part of a comment, we attempt to
		// prevent the error from either terminating the comment, or the script
		// block itself.
		//
		// In particular we:
		//   * replace "*/" comment end tokens with "* /", which does not
		//     terminate the comment
		//   * replace "<script" and "</script" with "\x3Cscript" and
		//     "\x3C/script" (case insensitively), and "<!--" with "\x3C!--",
		//     which prevents confusing script block termination semantics
		//
		// We also put a space before the comment so that if it is flush against
		// a division operator it is not turned into a line comment:
		//     x/{{y}}
		// turning into
		//     x//* error marshaling y:
		//          second line of error message */null
		errStr := err.Error()
		errStr = string(scriptTagRe.ReplaceAll([]byte(errStr), []byte(`\x3C${1}script`)))
		errStr = strings.ReplaceAll(errStr, "*/", "* /")
		errStr = strings.ReplaceAll(errStr, "<!--", `\x3C!--`)
		return fmt.Sprintf(" /* %s */null ", errStr)
	}

	// TODO: maybe post-process output to prevent it from containing
	// "<!--", "-->", "<![CDATA[", "]]>", or "</script"
	// in case custom marshalers produce output containing those.
	// Note: Do not use \x escaping to save bytes because it is not JSON
	// compatible and this escaper supports ld+json content-type.
	if len(b) == 0 {
		// In, `x=y/{{.}}*z` a json.Marshaler that produces "" should
		// not cause the output `x=y/*z`.
		return " null "
	}
	first, _ := utf8.DecodeRune(b)
	last, _ := utf8.DecodeLastRune(b)
	var buf strings.Builder
	// Prevent IdentifierNames and NumericLiterals from running into
	// keywords: in, instanceof, typeof, void
	pad := isJSIdentPart(first) || isJSIdentPart(last)
	if pad {
		buf.WriteByte(' ')
	}
	written := 0
	// Make sure that json.Marshal escapes codepoints U+2028 & U+2029
	// so it falls within the subset of JSON which is valid JS.
	for i := 0; i < len(b); {
		r, n := utf8.DecodeRune(b[i:])
		repl := ""
		switch r {
		case 0x2028:
			repl = "\\u2028"
		case 0x2029:
			repl = "\\u2029"
		}
		if repl != "" {
			buf.Write(b[written:i])
			buf.WriteString(repl)
			written = i + n
		}
		i += n
	}
	if buf.Len() != 0 {
		buf.Write(b[written:])
		if pad {
			buf.WriteByte(' ')
		}
		return buf.String()
	}
	return string(b)
}

// replace replaces each rune r of s with replacementTable[r], provided that
// r < len(replacementTable). If replacementTable[r] is the empty string then
// no replacement is made.
// It also replaces runes U+2028 and U+2029 with their backslash-u escapes (the
// JS line/paragraph separators, which JS does not treat as valid line breaks).
func replace(s string, replacementTable []string) string {
	var b strings.Builder
	r, w, written := rune(0), 0, 0
	for i := 0; i < len(s); i += w {
		// See comment in htmlEscaper.
		r, w = utf8.DecodeRuneInString(s[i:])
		var repl string
		switch {
		case int(r) < len(lowUnicodeReplacementTable):
			repl = lowUnicodeReplacementTable[r]
		case int(r) < len(replacementTable) && replacementTable[r] != "":
			repl = replacementTable[r]
		case r == 0x2028:
			repl = "\\u2028"
		case r == 0x2029:
			repl = "\\u2029"
		default:
			continue
		}
		if written == 0 {
			b.Grow(len(s))
		}
		b.WriteString(s[written:i])
		b.WriteString(repl)
		written = i + w
	}
	if written == 0 {
		return s
	}
	b.WriteString(s[written:])
	return b.String()
}

var lowUnicodeReplacementTable = []string{
	0: "\\u0000", 1: "\\u0001", 2: "\\u0002", 3: "\\u0003", 4: "\\u0004", 5: "\\u0005", 6: "\\u0006",
	'\a': "\\u0007",
	'\b': "\\u0008",
	'\t': "\\t",
	'\n': "\\n",
	'\v': "\\u000b", // "\v" == "v" on IE 6.
	'\f': "\\f",
	'\r': "\\r",
	0xe:  "\\u000e", 0xf: "\\u000f", 0x10: "\\u0010", 0x11: "\\u0011", 0x12: "\\u0012", 0x13: "\\u0013",
	0x14: "\\u0014", 0x15: "\\u0015", 0x16: "\\u0016", 0x17: "\\u0017", 0x18: "\\u0018", 0x19: "\\u0019",
	0x1a: "\\u001a", 0x1b: "\\u001b", 0x1c: "\\u001c", 0x1d: "\\u001d", 0x1e: "\\u001e", 0x1f: "\\u001f",
}

var jsStrReplacementTable = []string{
	0:    "\\u0000",
	'\t': "\\t",
	'\n': "\\n",
	'\v': "\\u000b", // "\v" == "v" on IE 6.
	'\f': "\\f",
	'\r': "\\r",
	// Encode HTML specials as hex so the output can be embedded
	// in HTML attributes without further encoding.
	'"':  "\\u0022",
	'`':  "\\u0060",
	'&':  "\\u0026",
	'\'': "\\u0027",
	'+':  "\\u002b",
	'/':  "\\/",
	'<':  "\\u003c",
	'>':  "\\u003e",
	'\\': "\\\\",
}

// jsBqStrReplacementTable is like jsStrReplacementTable except it also contains
// the special characters for JS template literals: $, {, and }.
var jsBqStrReplacementTable = []string{
	0:    "\\u0000",
	'\t': "\\t",
	'\n': "\\n",
	'\v': "\\u000b", // "\v" == "v" on IE 6.
	'\f': "\\f",
	'\r': "\\r",
	// Encode HTML specials as hex so the output can be embedded
	// in HTML attributes without further encoding.
	'"':  "\\u0022",
	'`':  "\\u0060",
	'&':  "\\u0026",
	'\'': "\\u0027",
	'+':  "\\u002b",
	'/':  "\\/",
	'<':  "\\u003c",
	'>':  "\\u003e",
	'\\': "\\\\",
	'$':  "\\u0024",
	'{':  "\\u007b",
	'}':  "\\u007d",
}

var jsRegexpReplacementTable = []string{
	0:    "\\u0000",
	'\t': "\\t",
	'\n': "\\n",
	'\v': "\\u000b", // "\v" == "v" on IE 6.
	'\f': "\\f",
	'\r': "\\r",
	// Encode HTML specials as hex so the output can be embedded
	// in HTML attributes without further encoding.
	'"':  "\\u0022",
	'$':  "\\$",
	'&':  "\\u0026",
	'\'': "\\u0027",
	'(':  "\\(",
	')':  "\\)",
	'*':  "\\*",
	'+':  "\\u002b",
	'-':  "\\-",
	'.':  "\\.",
	'/':  "\\/",
	'<':  "\\u003c",
	'>':  "\\u003e",
	'?':  "\\?",
	'[':  "\\[",
	'\\': "\\\\",
	']':  "\\]",
	'^':  "\\^",
	'{':  "\\{",
	'|':  "\\|",
	'}':  "\\}",
}

// isJSIdentPart reports whether the given rune is a JS identifier part.
// It does not handle all the non-Latin letters, joiners, and combining marks,
// but it does handle every codepoint that can occur in a numeric literal or
// a keyword.
func isJSIdentPart(r rune) bool {
	switch {
	case r == '$':
		return true
	case '0' <= r && r <= '9':
		return true
	case 'A' <= r && r <= 'Z':
		return true
	case r == '_':
		return true
	case 'a' <= r && r <= 'z':
		return true
	}
	return false
}
