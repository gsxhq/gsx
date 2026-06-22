package codegen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A gsx.RawCSS value (and an alias of it) in a <style> CSS context opts out of the
// CSS value-filter: codegen emits it raw via gw.S, while a plain string goes through
// gw.CSS. The alias case exercises types.Unalias in isRawCSS.
func TestRawCSSAliasOptsOut(t *testing.T) {
	repoRoot, _ := filepath.Abs("../..")
	tmp := t.TempDir()
	writeFile(t, tmp, "go.mod", "module gsxl\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	pkgDir := filepath.Join(tmp, "genpkg")
	os.MkdirAll(pkgDir, 0o755)
	src := "package views\n\n" +
		"import \"github.com/gsxhq/gsx\"\n\n" +
		"type RawStyle = gsx.RawCSS\n\n" +
		"component Styled(plain string, raw RawStyle) {\n" +
		"\t<style>.a { color: ${ plain }; border: ${ raw }; }</style>\n" +
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

	// Plain string CSS value: filtered via gw.CSS.
	if !strings.Contains(got, "_gsxgw.CSS(string(plain))") {
		t.Fatalf("plain CSS value should go through gw.CSS; got:\n%s", got)
	}
	// Aliased RawCSS value: emitted raw via gw.S (filter skipped) — proves the
	// alias is unwrapped (types.Unalias) by isRawCSS.
	if !strings.Contains(got, "_gsxgw.S(string(raw))") {
		t.Fatalf("aliased RawCSS value should be emitted raw via gw.S; got:\n%s", got)
	}
	if strings.Contains(got, "_gsxgw.CSS(string(raw))") {
		t.Fatalf("aliased RawCSS value must not be CSS-filtered; got:\n%s", got)
	}
}
