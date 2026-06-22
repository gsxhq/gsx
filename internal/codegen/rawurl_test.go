package codegen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A gsx.RawURL-typed value in a URL attribute is the author's vouch: codegen must
// skip the scheme sanitizer (gw.URL) and emit it as a plain attribute value
// (gw.AttrValue — still entity-escaped, scheme unchecked). A plain string in the
// same position must still be sanitized via gw.URL.
func TestRawURLBypassesSchemeCheck(t *testing.T) {
	repoRoot, _ := filepath.Abs("../..")
	tmp := t.TempDir()
	writeFile(t, tmp, "go.mod", "module gsxl\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	pkgDir := filepath.Join(tmp, "genpkg")
	os.MkdirAll(pkgDir, 0o755)
	src := "package views\n\n" +
		"import \"github.com/gsxhq/gsx\"\n\n" +
		"component Link(u string) {\n" +
		"\t<a href={u}>checked</a>\n" +
		"\t<a href={gsx.RawURL(u)}>vouched</a>\n" +
		"}\n"
	writeFile(t, pkgDir, "views.gsx", src)

	gen, err := GeneratePackage(pkgDir)
	if err != nil {
		t.Fatal(err)
	}
	var got string
	for _, s := range gen {
		got = string(s)
	}

	// Plain string href: still scheme-sanitized.
	if !strings.Contains(got, "_gsxgw.URL(string(u))") {
		t.Fatalf("plain href should emit gw.URL(string(u)); got:\n%s", got)
	}
	// RawURL href: routed to AttrValue (sanitizer skipped).
	if !strings.Contains(got, "_gsxgw.AttrValue(string(gsx.RawURL(u)))") {
		t.Fatalf("RawURL href should emit gw.AttrValue (skip sanitize); got:\n%s", got)
	}
	// And it must NOT be wrapped by the sanitizer.
	if strings.Contains(got, "_gsxgw.URL(string(gsx.RawURL(u)))") {
		t.Fatalf("RawURL href must not be scheme-sanitized via gw.URL; got:\n%s", got)
	}
}
