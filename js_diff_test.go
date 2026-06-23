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

func FuzzJSEscapersMatchStdlib(f *testing.F) {
	for _, s := range jsCorpus() {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		// Non-attr JS contexts: byte-parity vs the html/template sub-context oracle.
		if got, want := gsxJSVal(s), jsValOracle(s); got != want {
			t.Fatalf("JSVal(%q)=%q, oracle=%q", s, got, want)
		}
		if got, want := gsxJSStr(s), jsStrOracle(s); got != want {
			t.Fatalf("JSStr(%q)=%q, oracle=%q", s, got, want)
		}
		if got, want := gsxJSTmpl(s), jsTmplOracle(s); got != want {
			t.Fatalf("JSTmpl(%q)=%q, oracle=%q", s, got, want)
		}
		if got, want := gsxJSRegexp(s), jsRegexpOracle(s); got != want {
			t.Fatalf("JSRegexp(%q)=%q, oracle=%q", s, got, want)
		}
		// Attr variants: JS-escape composed with HTML-attr-escape (htmlReplacer).
		if got, want := gsxJSValAttr(s), htmlReplacer.Replace(jsValOracle(s)); got != want {
			t.Fatalf("JSValAttr(%q)=%q, oracle=%q", s, got, want)
		}
		if got, want := gsxJSStrAttr(s), htmlReplacer.Replace(jsStrOracle(s)); got != want {
			t.Fatalf("JSStrAttr(%q)=%q, oracle=%q", s, got, want)
		}
		if got, want := gsxJSTmplAttr(s), htmlReplacer.Replace(jsTmplOracle(s)); got != want {
			t.Fatalf("JSTmplAttr(%q)=%q, oracle=%q", s, got, want)
		}
		if got, want := gsxJSRegexpAttr(s), htmlReplacer.Replace(jsRegexpOracle(s)); got != want {
			t.Fatalf("JSRegexpAttr(%q)=%q, oracle=%q", s, got, want)
		}
	})
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

// --- gsx *Attr escaper string helpers -----------------------------------------

func gsxJSValAttr(v any) string    { var b strings.Builder; W(&b).JSValAttr(v); return b.String() }
func gsxJSStrAttr(s string) string { var b strings.Builder; W(&b).JSStrAttr(s); return b.String() }
func gsxJSTmplAttr(s string) string {
	var b strings.Builder
	W(&b).JSTmplAttr(s)
	return b.String()
}
func gsxJSRegexpAttr(s string) string {
	var b strings.Builder
	W(&b).JSRegexpAttr(s)
	return b.String()
}

// TestJSValAttrParity asserts that JSValAttr equals htmlReplacer.Replace(jsValOracle(s))
// for the full C1 corpus — proving the composition is JS-escape THEN HTML-attr-escape.
func TestJSValAttrParity(t *testing.T) {
	for _, s := range jsCorpus() {
		want := htmlReplacer.Replace(jsValOracle(s))
		if got := gsxJSValAttr(s); got != want {
			t.Errorf("JSValAttr(%q)=%q, want %q", s, got, want)
		}
	}
}

// TestJSStrAttrParity asserts that JSStrAttr equals htmlReplacer.Replace(jsStrOracle(s)).
func TestJSStrAttrParity(t *testing.T) {
	for _, s := range jsCorpus() {
		want := htmlReplacer.Replace(jsStrOracle(s))
		if got := gsxJSStrAttr(s); got != want {
			t.Errorf("JSStrAttr(%q)=%q, want %q", s, got, want)
		}
	}
}

// TestJSTmplAttrParity asserts that JSTmplAttr equals htmlReplacer.Replace(jsTmplOracle(s)).
func TestJSTmplAttrParity(t *testing.T) {
	for _, s := range jsCorpus() {
		want := htmlReplacer.Replace(jsTmplOracle(s))
		if got := gsxJSTmplAttr(s); got != want {
			t.Errorf("JSTmplAttr(%q)=%q, want %q", s, got, want)
		}
	}
}

// TestJSRegexpAttrParity asserts that JSRegexpAttr equals htmlReplacer.Replace(jsRegexpOracle(s)).
func TestJSRegexpAttrParity(t *testing.T) {
	for _, s := range jsCorpus() {
		want := htmlReplacer.Replace(jsRegexpOracle(s))
		if got := gsxJSRegexpAttr(s); got != want {
			t.Errorf("JSRegexpAttr(%q)=%q, want %q", s, got, want)
		}
	}
}

// TestJSAttrBreakout is the security test: dangerous payloads must NOT produce
// raw '"', '<', or '>' in the output — any such bytes from the input must be
// JS-escaped or HTML-entity-escaped, so the result cannot break out of either
// a double-quoted HTML attribute or the JS string inside it.
func TestJSAttrBreakout(t *testing.T) {
	breakoutPayload := `"><script>alert(1)</script>`

	t.Run("JSValAttr", func(t *testing.T) {
		got := gsxJSValAttr(breakoutPayload)
		if strings.Contains(got, `"`) {
			t.Errorf("JSValAttr output contains raw double-quote: %q", got)
		}
		if strings.Contains(got, "<") {
			t.Errorf("JSValAttr output contains raw '<': %q", got)
		}
		if strings.Contains(got, ">") {
			t.Errorf("JSValAttr output contains raw '>': %q", got)
		}
	})

	t.Run("JSStrAttr", func(t *testing.T) {
		got := gsxJSStrAttr(`a"b`)
		if strings.Contains(got, `"`) {
			t.Errorf("JSStrAttr output contains raw double-quote: %q", got)
		}

		got2 := gsxJSStrAttr(breakoutPayload)
		if strings.Contains(got2, `"`) {
			t.Errorf("JSStrAttr output contains raw double-quote: %q", got2)
		}
		if strings.Contains(got2, "<") {
			t.Errorf("JSStrAttr output contains raw '<': %q", got2)
		}
		if strings.Contains(got2, ">") {
			t.Errorf("JSStrAttr output contains raw '>': %q", got2)
		}
	})

	t.Run("JSTmplAttr", func(t *testing.T) {
		got := gsxJSTmplAttr(breakoutPayload)
		if strings.Contains(got, `"`) {
			t.Errorf("JSTmplAttr output contains raw double-quote: %q", got)
		}
		if strings.Contains(got, "<") {
			t.Errorf("JSTmplAttr output contains raw '<': %q", got)
		}
		if strings.Contains(got, ">") {
			t.Errorf("JSTmplAttr output contains raw '>': %q", got)
		}
	})

	t.Run("JSRegexpAttr", func(t *testing.T) {
		got := gsxJSRegexpAttr(breakoutPayload)
		if strings.Contains(got, `"`) {
			t.Errorf("JSRegexpAttr output contains raw double-quote: %q", got)
		}
		if strings.Contains(got, "<") {
			t.Errorf("JSRegexpAttr output contains raw '<': %q", got)
		}
		if strings.Contains(got, ">") {
			t.Errorf("JSRegexpAttr output contains raw '>': %q", got)
		}
	})
}

// TestJSValAttrRawJS confirms that RawJS is passed through as raw JS (no
// JS-string-escaping of parens etc.) but still HTML-attribute-escaped so it
// survives inside a double-quoted attribute.
func TestJSValAttrRawJS(t *testing.T) {
	// Plain identifier — no HTML-special chars, should come through verbatim.
	if got := gsxJSValAttr(RawJS("toggle()")); got != "toggle()" {
		t.Errorf("JSValAttr(RawJS(%q))=%q, want %q", "toggle()", got, "toggle()")
	}
	// A RawJS with HTML-special chars must be HTML-attr-escaped but NOT JS-escaped.
	got := gsxJSValAttr(RawJS(`foo("bar") && x<y`))
	if strings.Contains(got, `"`) {
		t.Errorf("JSValAttr(RawJS) output contains raw double-quote: %q", got)
	}
	if strings.Contains(got, "<") {
		t.Errorf("JSValAttr(RawJS) output contains raw '<': %q", got)
	}
	// Parens must NOT be backslash-escaped (not JS-string-escaped).
	if strings.Contains(got, `\(`) || strings.Contains(got, `\)`) {
		t.Errorf("JSValAttr(RawJS) parens were JS-escaped, should not be: %q", got)
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

func TestJSValNonStringTypes(t *testing.T) {
	cases := []struct {
		name string
		in   any
		want string
	}{
		{"int", 42, " 42 "},
		{"bool_true", true, " true "},
		{"bool_false", false, " false "},
		{"float", 3.5, " 3.5 "},
		{"nil", nil, " null "},
		{"map", map[string]int{"a": 1}, `{"a":1}`},
		{"slice", []int{1, 2}, `[1,2]`},
	}
	for _, c := range cases {
		if got := gsxJSVal(c.in); got != c.want {
			t.Errorf("JSVal(%s)=%q, want %q", c.name, got, c.want)
		}
	}
}

func TestJSValAttrNonStringTypes(t *testing.T) {
	cases := []struct {
		name string
		in   any
		want string
	}{
		{"int", 42, " 42 "},
		{"bool_true", true, " true "},
		{"bool_false", false, " false "},
		{"float", 3.5, " 3.5 "},
		{"nil", nil, " null "},
		{"map", map[string]int{"a": 1}, `{&#34;a&#34;:1}`},
		{"slice", []int{1, 2}, `[1,2]`},
	}
	for _, c := range cases {
		if got := gsxJSValAttr(c.in); got != c.want {
			t.Errorf("JSValAttr(%s)=%q, want %q", c.name, got, c.want)
		}
	}
}
