package gsx

import (
	"bytes"
	"html/template"
	"strings"
	"testing"
)

// --- html/template oracles ----------------------------------------------------

// htmlTextOracle returns what html/template emits for s in HTML *text* context.
func htmlTextOracle(s string) string { return template.HTMLEscapeString(s) }

// cssOracle returns what html/template emits for s inside <style>{{.}}</style>
// (CSS value context) — the layer gsx's cssValueFilter targets.
func cssOracle(s string) string {
	t := template.Must(template.New("c").Parse(`<style>{{.}}</style>`))
	var b bytes.Buffer
	_ = t.Execute(&b, s)
	out := b.String()
	out = strings.TrimPrefix(out, "<style>")
	out = strings.TrimSuffix(out, "</style>")
	return out
}

// urlBlockedByStdlib reports whether html/template neutralizes s as a URL (its
// unsafe-scheme sentinel #ZgotmplZ appears in an href). gsx's urlSanitize does
// scheme-filtering WITHOUT normalization, so we compare the SAFETY DECISION, not
// bytes.
func urlBlockedByStdlib(s string) bool {
	t := template.Must(template.New("u").Parse(`<a href="{{.}}">`))
	var b bytes.Buffer
	_ = t.Execute(&b, s)
	return strings.Contains(b.String(), "#ZgotmplZ")
}

// gsxHTML is writeHTML as a string.
func gsxHTML(s string) string {
	var b strings.Builder
	_ = writeHTML(&b, s)
	return b.String()
}

// --- divergence allow-list ----------------------------------------------------

type diffCtx int

const (
	ctxHTML diffCtx = iota
	ctxCSS
)

type diffKey struct {
	ctx diffCtx
	in  string
}

// knownDivergences records (context,input) pairs where gsx INTENTIONALLY differs
// from html/template, each with a justification. The differential test skips
// ONLY these exact pairs. Add an entry only after confirming the difference is
// deliberate and safe (cite escape.go).
var knownDivergences = map[diffKey]string{
	// (populated during Step 2 triage; URL sentinel is handled by safety-decision
	// parity, not here.)
}

// isKnownURLDivergence reports whether the URL safety-decision difference between
// gsx and html/template for s is intentional and safe. It covers two classes:
//
//  1. tel: scheme — gsx allows tel: (RFC 3966 phone links); stdlib does not.
//     tel: opens the phone dialer and cannot execute scripts. See escape.go:39.
//
//  2. Relative URLs where '#' or '?' precedes ':' — gsx's urlSanitize correctly
//     exempts these from scheme detection per RFC 3986 (after '#' begins the
//     fragment; after '?' begins the query — neither is a scheme context).
//     html/template's isSafeURL only exempts '/' before ':', so it blocks
//     strings like "#:", "#anchor:name", "?q=a:b" as conservative false-positives.
//     In a browser, <a href="#foo:bar"> is a fragment link, not a protocol handler;
//     no script execution or irreversible side effect can occur.
func isKnownURLDivergence(s string) bool {
	i := strings.IndexByte(s, ':')
	if i < 0 {
		return false
	}
	// Class 1: tel: scheme intentionally allowed by gsx.
	if strings.EqualFold(s[:i], "tel") {
		return true
	}
	// Class 2: '#' or '?' precedes ':' — gsx treats this as non-scheme context.
	// stdlib blocks conservatively; gsx follows RFC 3986.
	return strings.ContainsAny(s[:i], "#?")
}

func TestEscaperMatchesStdlib(t *testing.T) {
	inputs := diffCorpus()
	for _, s := range inputs {
		// HTML text context: byte-parity.
		if _, skip := knownDivergences[diffKey{ctxHTML, s}]; !skip {
			if got, want := gsxHTML(s), htmlTextOracle(s); got != want {
				t.Errorf("HTML escape mismatch for %q:\n gsx  = %q\n std  = %q\n(if intentional, add to knownDivergences with a reason)", s, got, want)
			}
		}
		// CSS value context: byte-parity.
		if _, skip := knownDivergences[diffKey{ctxCSS, s}]; !skip {
			if got, want := cssValueFilter(s), cssOracle(s); got != want {
				t.Errorf("CSS filter mismatch for %q:\n gsx  = %q\n std  = %q\n(if intentional, add to knownDivergences with a reason)", s, got, want)
			}
		}
		// URL context: SAFETY-DECISION parity (gsx blocks <=> stdlib neutralizes).
		if !isKnownURLDivergence(s) {
			gsxBlocked := urlSanitize(s) == "about:invalid#gsx"
			if gsxBlocked != urlBlockedByStdlib(s) {
				t.Errorf("URL safety-decision mismatch for %q: gsx blocked=%v, stdlib blocked=%v", s, gsxBlocked, urlBlockedByStdlib(s))
			}
		}
	}
}

// diffCorpus is the shared differential input set: the existing CSS fuzz seeds,
// known XSS vectors, and boundary bytes.
func diffCorpus() []string {
	return []string{
		// benign
		"", "foo", "hello world", "a&b", "10px", "#fff", "color: red",
		"/path/to/x", "https://example.com/a?b=c#d", "mailto:a@b.com", "tel:+1",
		// HTML-significant
		`a<b>c`, `"quoted"`, "'apos'", "x&y", "<script>alert(1)</script>",
		// URL schemes (safety decision)
		"javascript:alert(1)", "JavaScript:alert(1)", "vbscript:x", "data:text/html,x",
		"http://ok", "  javascript:x", "/rel?a=b",
		// CSS (reuse FuzzCSSValueFilter seeds)
		"rgb(1,2,3)", "<!--", "-->", "</style", "expression(alert(1))", "EXPRESSION",
		"-moz-binding", `\3c script`, `-expre\69on`, "url(javascript:alert(1))",
		"a;b}c{d", "--x: ;", "1.25in", "`backtick`", "\x00",
	}
}

func FuzzEscaperMatchesStdlib(f *testing.F) {
	for _, s := range diffCorpus() {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		if _, skip := knownDivergences[diffKey{ctxHTML, s}]; !skip {
			if got, want := gsxHTML(s), htmlTextOracle(s); got != want {
				t.Fatalf("HTML divergence for %q: gsx=%q std=%q", s, got, want)
			}
		}
		if _, skip := knownDivergences[diffKey{ctxCSS, s}]; !skip {
			if got, want := cssValueFilter(s), cssOracle(s); got != want {
				t.Fatalf("CSS divergence for %q: gsx=%q std=%q", s, got, want)
			}
		}
		if !isKnownURLDivergence(s) {
			if (urlSanitize(s) == "about:invalid#gsx") != urlBlockedByStdlib(s) {
				t.Fatalf("URL safety-decision divergence for %q", s)
			}
		}
	})
}
