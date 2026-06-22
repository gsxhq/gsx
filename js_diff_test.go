package gsx

import (
	"bytes"
	"html/template"
	"strings"
	"testing"
)

// --- html/template JS sub-context oracles -------------------------------------
//
// Each oracle renders the input through html/template in a specific JS
// sub-context inside a <script> block, then trims the static wrapper to extract
// exactly the bytes the stdlib emits for that context. gsx's escapers must be
// byte-for-byte identical.

// jsValOracle returns what html/template emits for s in JS *value* context.
func jsValOracle(s string) string {
	t := template.Must(template.New("v").Parse(`<script>var x={{.}}</script>`))
	var b bytes.Buffer
	_ = t.Execute(&b, s)
	out := strings.TrimPrefix(b.String(), "<script>var x=")
	return strings.TrimSuffix(out, "</script>")
}

// jsStrOracle returns what html/template emits for s inside a "double-quoted"
// JS string literal.
func jsStrOracle(s string) string {
	t := template.Must(template.New("s").Parse(`<script>var x="{{.}}"</script>`))
	var b bytes.Buffer
	_ = t.Execute(&b, s)
	out := strings.TrimPrefix(b.String(), `<script>var x="`)
	return strings.TrimSuffix(out, `"</script>`)
}

// jsTmplOracle returns what html/template emits for s inside a `backtick`
// template literal. Probing confirmed this wrapper routes to jsTmplLitEscaper
// (e.g. a`b -> a`b, ${x} -> ${x}).
func jsTmplOracle(s string) string {
	t := template.Must(template.New("t").Parse("<script>var x=`{{.}}`</script>"))
	var b bytes.Buffer
	_ = t.Execute(&b, s)
	out := strings.TrimPrefix(b.String(), "<script>var x=`")
	return strings.TrimSuffix(out, "`</script>")
}

// jsRegexpOracle returns what html/template emits for s inside a /regexp/
// literal. Probing confirmed this wrapper routes to jsRegexpEscaper
// (e.g. *+? -> \*+\?, "" -> (?:)).
func jsRegexpOracle(s string) string {
	t := template.Must(template.New("r").Parse("<script>var x=/{{.}}/</script>"))
	var b bytes.Buffer
	_ = t.Execute(&b, s)
	out := strings.TrimPrefix(b.String(), "<script>var x=/")
	return strings.TrimSuffix(out, "/</script>")
}

// --- gsx escaper string helpers -----------------------------------------------

func gsxJSVal(v any) string    { var b strings.Builder; W(&b).JSVal(v); return b.String() }
func gsxJSStr(s string) string { var b strings.Builder; W(&b).JSStr(s); return b.String() }
func gsxJSTmpl(s string) string {
	var b strings.Builder
	W(&b).JSTmpl(s)
	return b.String()
}
func gsxJSRegexp(s string) string {
	var b strings.Builder
	W(&b).JSRegexp(s)
	return b.String()
}

// jsCorpus is the shared input set: boundary bytes, quote/comment-breakout
// vectors, JS specials, and the U+2028/U+2029 line terminators.
func jsCorpus() []string {
	return []string{
		"", "abc", `a"b`, "a'b", "</script>", "</SCRIPT>", "<!--", "-->",
		"a b", "a\tb", "a\nb", "a\rb", "a\\b", "a\\n", "a</script>b",
		"x&y", "&amp;", "a\x00b", "a`b", "a${x}b", "$foo|x.y", "x^y",
		"*+?", "[](){}", "<![CDATA[", "]]>", "a b", "a b",
		"foo\xA0bar", "a/b", "42", "1.5", "true", "null",
	}
}

func TestJSValDiff(t *testing.T) {
	for _, s := range jsCorpus() {
		if got, want := gsxJSVal(s), jsValOracle(s); got != want {
			t.Errorf("JSVal(%q)=%q, html/template oracle=%q", s, got, want)
		}
	}
}

func TestJSStrDiff(t *testing.T) {
	for _, s := range jsCorpus() {
		if got, want := gsxJSStr(s), jsStrOracle(s); got != want {
			t.Errorf("JSStr(%q)=%q, oracle=%q", s, got, want)
		}
	}
}

func TestJSTmplDiff(t *testing.T) {
	for _, s := range jsCorpus() {
		if got, want := gsxJSTmpl(s), jsTmplOracle(s); got != want {
			t.Errorf("JSTmpl(%q)=%q, oracle=%q", s, got, want)
		}
	}
}

func TestJSRegexpDiff(t *testing.T) {
	for _, s := range jsCorpus() {
		if got, want := gsxJSRegexp(s), jsRegexpOracle(s); got != want {
			t.Errorf("JSRegexp(%q)=%q, oracle=%q", s, got, want)
		}
	}
}

// TestJSRawPassthrough confirms a gsx.RawJS value is emitted verbatim in value
// context, bypassing JSON marshaling (the analogue of template.JS).
func TestJSRawPassthrough(t *testing.T) {
	if got := gsxJSVal(RawJS("x()")); got != "x()" {
		t.Errorf("JSVal(RawJS(%q))=%q, want %q", "x()", got, "x()")
	}
	if got := gsxJSVal(RawJS("</script>")); got != "</script>" {
		t.Errorf("RawJS must pass through verbatim, got %q", got)
	}
}

// TestJSValMarshalFailsafe confirms the comment-safe failsafe on a marshal
// error, matching the stdlib's neutralization of */ and script breakouts.
func TestJSValMarshalFailsafe(t *testing.T) {
	got := gsxJSVal(badJSMarshaler{})
	// Shape: " /* <neutralized error> */null ".
	if !strings.HasPrefix(got, " /* ") || !strings.HasSuffix(got, " */null ") {
		t.Fatalf("failsafe shape wrong: %q", got)
	}
	// Inspect only the comment body; the wrapping " /* " ... " */null " is the
	// legitimate comment delimiter and must be left intact.
	body := strings.TrimSuffix(strings.TrimPrefix(got, " /* "), " */null ")
	if strings.Contains(body, "*/") {
		t.Errorf("failsafe left a comment terminator in the body: %q", body)
	}
	if strings.Contains(body, "<script") || strings.Contains(body, "</script") {
		t.Errorf("failsafe left a script tag in the body: %q", body)
	}
	if strings.Contains(body, "<!--") {
		t.Errorf("failsafe left a comment opener in the body: %q", body)
	}
}

type badJSMarshaler struct{}

func (badJSMarshaler) MarshalJSON() ([]byte, error) {
	return nil, errString("*/ </script> <!-- boom")
}

type errString string

func (e errString) Error() string { return string(e) }
