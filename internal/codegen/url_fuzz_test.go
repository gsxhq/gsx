package codegen

import (
	"html"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"
)

// FuzzURLLiteralSchemeSafety renders `<a href=`{s1}@{a}{s2}@{b}`>` for fuzzed
// static text s1,s2 and hole values a,b, and asserts the browser-effective
// scheme is never dangerous. Task 5 made URL-context backtick literals
// sanitize the WHOLE assembled value via a single _gsxgw.URL(...) call (a
// fail-closed allow-list of http/https/mailto/tel that maps everything else,
// including split-across-holes and obfuscated schemes, to
// "about:invalid#gsx"); this fuzzer is a permanent regression guard for that
// invariant.
func FuzzURLLiteralSchemeSafety(f *testing.F) {
	// Seeds spanning every class the earlier per-hole design failed on.
	f.Add("/u/", "7", "/edit", "")                         // safe path
	f.Add("", "https://ex.com", "/p", "")                  // safe origin from hole
	f.Add("javascript:", "alert(1)", "", "")               // static dangerous scheme
	f.Add("data:text/html,", "<script>x</script>", "", "") // static data:
	f.Add("", "javascript", ":alert(1)", "")               // split across holes
	f.Add("java\tscript:", "alert(1)", "", "")             // control-byte obfuscation
	f.Add(" javascript:", "alert(1)", "", "")              // leading-space obfuscation
	f.Add("", "javascript:alert(1)", "", "")               // whole-value in a hole
	f.Add("//", "evil.com", "/p", "")                      // protocol-relative
	f.Fuzz(func(t *testing.T, s1, a, s2, b string) {
		out, ok := tryRenderHref(t, s1, a, s2, b)
		if !ok {
			return // compile/build error is acceptable (never an XSS)
		}
		val := extractHref(out)
		if sch := effectiveScheme(val); isDangerousScheme(sch) {
			t.Fatalf("dangerous scheme %q from s1=%q a=%q s2=%q b=%q -> href=%q",
				sch, s1, a, s2, b, val)
		}
	})
}

// FuzzURLWholeLiteralPipeSchemeSafety renders `<a href={`@{u}` |> upper}>` for a
// fuzzed hole value u, and asserts the browser-effective scheme is never
// dangerous. This guards the whole-literal-pipe URL invariant (Task 4/5): a
// braced-attr backtick literal followed by `|> filter` assembles the value,
// runs the filter, and only THEN sanitizes via a single _gsxgw.URL(...). A
// filter (upper here) that would turn a hole into a dangerous scheme
// ("javascript:alert(1)" -> "JAVASCRIPT:ALERT(1)") must still be blocked,
// because URL() runs on the POST-pipe result — sanitize-after-pipe.
func FuzzURLWholeLiteralPipeSchemeSafety(f *testing.F) {
	f.Add("javascript:alert(1)")   // dangerous scheme, whole value in the hole
	f.Add("JavaScript:alert(1)")   // mixed-case (upper doesn't change the danger)
	f.Add("data:text/html,<x>")    // data: scheme
	f.Add("java\tscript:alert(1)") // control-byte obfuscation survives upper
	f.Add(" javascript:alert(1)")  // leading-space obfuscation
	f.Add("https://ex.com/p")      // safe origin
	f.Add("/u/7/edit")             // safe relative path
	f.Fuzz(func(t *testing.T, u string) {
		out, ok := tryRenderHrefPiped(t, u)
		if !ok {
			return // compile/build error is acceptable (never an XSS)
		}
		val := extractHref(out)
		if sch := effectiveScheme(val); isDangerousScheme(sch) {
			t.Fatalf("dangerous scheme %q from piped u=%q -> href=%q", sch, u, val)
		}
	})
}

// tryRenderHrefPiped compiles, builds, and renders a one-off component
//
//	component L(u string) { <a href={`@{u}` |> upper}>x</a> }
//
// with u passed as the component's string prop, driving the same GenerateDirs
// pipeline as tryRenderHref but exercising the BRACED whole-literal-pipe attr
// form. The std filter package is wired in via FilterPkgs so `upper` resolves.
func tryRenderHrefPiped(t *testing.T, u string) (string, bool) {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping go-run render fuzz in -short mode")
	}
	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	tmp := t.TempDir()
	writeMultiFile(t, tmp, "go.mod", "module gsxurlfuzz\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")

	viewsDir := filepath.Join(tmp, "views")
	if err := os.MkdirAll(viewsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	src := "package views\n\ncomponent L(u string) {\n\t<a href={f`@{u}` |> upper}>x</a>\n}\n"
	writeMultiFile(t, viewsDir, "views.gsx", src)

	genRes, err := GenerateDirs(tmp, []string{viewsDir}, Options{FilterPkgs: []string{stdImportPath}}, nil)
	if err != nil {
		return "", false
	}
	dr := genRes[viewsDir]
	if hasDiagErrors(dr.Diags) {
		return "", false
	}
	for gsxPath, gen := range dr.Files {
		base := strings.TrimSuffix(filepath.Base(gsxPath), ".gsx")
		writeMultiFile(t, viewsDir, base+".x.go", string(gen))
	}

	writeMultiFile(t, tmp, "main.go", `package main

import (
	"context"
	"os"

	p "gsxurlfuzz/views"
)

func main() {
	_ = p.L(p.LProps{U: `+strconv.Quote(u)+`}).Render(context.Background(), os.Stdout)
}
`)

	cmd := exec.Command("go", "run", ".")
	cmd.Dir = tmp
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", false
	}
	return string(out), true
}

// tryRenderHref compiles, builds, and renders a one-off component
//
//	component L(a string, b string) { <a href=`<s1>@{a}<s2>@{b}`>x</a> }
//
// with s1/s2 spliced as raw static text (they may contain tabs, spaces,
// colons, or even bytes that break gsx syntax — in which case codegen fails
// and ok is false) and a/b passed as the component's string props. It reuses
// the GenerateDirs pipeline (the same one filters_multi_test.go's
// renderWithFilters drives) rather than a standalone compile path, and
// returns the rendered HTML plus whether the whole pipeline succeeded.
func tryRenderHref(t *testing.T, s1, a, s2, b string) (string, bool) {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping go-run render fuzz in -short mode")
	}
	// s1/s2 are spliced as raw source bytes into the .gsx file; restrict to
	// valid UTF-8 so failures are about URL-scheme safety, not source encoding.
	if !utf8.ValidString(s1) || !utf8.ValidString(s2) {
		return "", false
	}

	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	tmp := t.TempDir()
	writeMultiFile(t, tmp, "go.mod", "module gsxurlfuzz\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")

	viewsDir := filepath.Join(tmp, "views")
	if err := os.MkdirAll(viewsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	src := "package views\n\ncomponent L(a string, b string) {\n\t<a href=f`" + s1 + "@{a}" + s2 + "@{b}`>x</a>\n}\n"
	writeMultiFile(t, viewsDir, "views.gsx", src)

	genRes, err := GenerateDirs(tmp, []string{viewsDir}, Options{}, nil)
	if err != nil {
		return "", false // e.g. a parse error from a stray backtick in s1/s2
	}
	dr := genRes[viewsDir]
	if hasDiagErrors(dr.Diags) {
		return "", false
	}
	for gsxPath, gen := range dr.Files {
		base := strings.TrimSuffix(filepath.Base(gsxPath), ".gsx")
		writeMultiFile(t, viewsDir, base+".x.go", string(gen))
	}

	// a/b are passed as Go string literals via strconv.Quote, which is safe
	// for arbitrary byte content (including invalid UTF-8, control bytes,
	// backticks, quotes) — they never touch the .gsx source, only the
	// generated main's invocation.
	writeMultiFile(t, tmp, "main.go", `package main

import (
	"context"
	"os"

	p "gsxurlfuzz/views"
)

func main() {
	_ = p.L(p.LProps{A: `+strconv.Quote(a)+`, B: `+strconv.Quote(b)+`}).Render(context.Background(), os.Stdout)
}
`)

	cmd := exec.Command("go", "run", ".")
	cmd.Dir = tmp
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", false // codegen produced source that failed to build/run: not an XSS
	}
	return string(out), true
}

// extractHref pulls the href="…" value out of rendered HTML and
// entity-decodes it, mirroring what a browser does: it decodes the attribute
// value BEFORE parsing it as a URL, so an encoded "&#58;" must be treated as
// a literal ":" for scheme purposes.
func extractHref(doc string) string {
	_, rest, found := strings.Cut(doc, `href="`)
	if !found {
		return ""
	}
	val, _, found := strings.Cut(rest, `"`)
	if !found {
		return ""
	}
	return html.UnescapeString(val)
}

// effectiveScheme mimics the WHATWG URL parser's pre-scheme normalization so
// the fuzzer catches the obfuscations that defeated per-hole classification:
// remove ALL ASCII tab/LF/CR, strip leading C0-control-or-space, lowercase,
// then take the run before the first ':' — but only if no '/','?','#'
// precedes it.
func effectiveScheme(v string) string {
	var b strings.Builder
	for i := 0; i < len(v); i++ {
		if c := v[i]; c != '\t' && c != '\n' && c != '\r' {
			b.WriteByte(c)
		}
	}
	s := b.String()
	for len(s) > 0 && s[0] <= ' ' {
		s = s[1:]
	}
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case ':':
			return strings.ToLower(s[:i])
		case '/', '?', '#':
			return "" // relative — no scheme
		}
	}
	return ""
}

func isDangerousScheme(scheme string) bool {
	switch scheme {
	case "", "http", "https", "mailto", "tel", "about": // about = blocked sentinel about:invalid#gsx
		return false
	default:
		return true // javascript, data, vbscript, file, blob, … => must never appear
	}
}
