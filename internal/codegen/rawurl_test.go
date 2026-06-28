package codegen

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// A gsx.RawURL-typed value in a URL attribute is the author's vouch: codegen must
// skip the scheme sanitizer (gw.URL) and emit it as a plain attribute value
// (gw.AttrValue — still entity-escaped, scheme unchecked). A plain string in the
// same position must still be sanitized via gw.URL.
func TestRawURLBypassesSchemeCheck(t *testing.T) {
	t.Parallel()
	repoRoot, _ := filepath.Abs("../..")
	tmp := t.TempDir()
	writeFile(t, tmp, "go.mod", "module gsxl\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	pkgDir := filepath.Join(tmp, "genpkg")
	os.MkdirAll(pkgDir, 0o755)
	src := "package views\n\n" +
		"import \"github.com/gsxhq/gsx\"\n\n" +
		"type Trusted = gsx.RawURL\n\n" +
		"component Link(u string) {\n" +
		"\t<a href={u}>checked</a>\n" +
		"\t<a href={gsx.RawURL(u)}>vouched</a>\n" +
		"}\n\n" +
		"component Aliased(tu Trusted) { <a href={tu}>x</a> }\n"
	writeFile(t, pkgDir, "views.gsx", src)

	res, err := GenerateDirs(tmp, []string{pkgDir}, GenOptions{FilterPkgs: []string{stdImportPath}, CSSMinify: true, JSMinify: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	gen := res[pkgDir].Files
	var got string
	for _, s := range gen {
		got = string(s)
	}

	// Plain string href: still scheme-sanitized via gw.URL.
	if !strings.Contains(got, "_gsxgw.URL(string(u))") {
		t.Fatalf("plain href should emit gw.URL(string(u)); got:\n%s", got)
	}
	// RawURL href: routed to gw.AttrValue (entity-escaped, sanitizer skipped).
	// Matched loosely so a benign codegen refactor of the cast spelling doesn't
	// fail this for non-security reasons; the corpus render golden is the strict guard.
	if !regexp.MustCompile(`_gsxgw\.AttrValue\([^\n]*RawURL`).MatchString(got) {
		t.Fatalf("RawURL href should emit gw.AttrValue (skip sanitize); got:\n%s", got)
	}
	// And it must NOT be wrapped by the scheme sanitizer.
	if regexp.MustCompile(`_gsxgw\.URL\([^\n]*RawURL`).MatchString(got) {
		t.Fatalf("RawURL href must not be scheme-sanitized via gw.URL; got:\n%s", got)
	}
	// An ALIAS of gsx.RawURL (type Trusted = gsx.RawURL) must also opt out — the
	// type detection unwraps aliases (types.Unalias), so it routes to AttrValue.
	if !strings.Contains(got, "_gsxgw.AttrValue(string(tu))") {
		t.Fatalf("aliased RawURL href should emit gw.AttrValue; got:\n%s", got)
	}
	if strings.Contains(got, "_gsxgw.URL(string(tu))") {
		t.Fatalf("aliased RawURL href must not be scheme-sanitized; got:\n%s", got)
	}
}
