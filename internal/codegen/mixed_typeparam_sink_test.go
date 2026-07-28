package codegen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A mixed type parameter renders through AttrAnyToggle, which decides
// presence-vs-value at runtime but only ATTRIBUTE-escapes the string case. On a
// URL, JS or CSS name that would ship an unsanitized value where the sink
// branches sanitize one — href={u} with T string|int emitting javascript:
// straight through — so the toggle path is restricted to plain context.
//
// This guards a regression introduced while widening BoolRendersBare: the
// toggle branch used to be reachable only for the ~25 HTML boolean attribute
// names, none of them a sink, and widening the predicate silently put every
// sink name in its path.
func TestMixedTypeParamDoesNotBypassValueSinks(t *testing.T) {
	t.Parallel()
	repoRoot, _ := filepath.Abs("../..")
	tmp := t.TempDir()
	writeFile(t, tmp, "go.mod", "module gsxsink\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	dir := filepath.Join(tmp, "views")
	os.MkdirAll(dir, 0o755)
	src := `package views

component Link[T string | int](u T) {
	<a href={ u }>x</a>
	<button @click={ u }>c</button>
	<div data-x={ u }></div>
}
`
	if err := os.WriteFile(filepath.Join(dir, "views.gsx"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := GenerateDirs(tmp, []string{dir}, Options{FilterPkgs: []string{stdImportPath}, CSSMinify: true, JSMinify: true}, nil)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	dr := res[dir]
	if hasDiagErrors(dr.Diags) {
		t.Fatalf("generate: unexpected errors: %v", dr.Diags)
	}
	var gen string
	for _, b := range dr.Files {
		gen += string(b)
	}

	for _, name := range []string{`AttrAnyToggle("href"`, `AttrAnyToggle("@click"`} {
		if strings.Contains(gen, name) {
			t.Errorf("%s bypasses the value sink; generated:\n%s", name, gen)
		}
	}
	// The URL sink is still what owns href.
	if !strings.Contains(gen, "_gsxgw.URL(") {
		t.Errorf("href on a mixed type parameter must route through the URL sink; generated:\n%s", gen)
	}
	// Plain context keeps the runtime presence-vs-value decision.
	if !strings.Contains(gen, `AttrAnyToggle("data-x"`) {
		t.Errorf("data-x on a mixed type parameter should use AttrAnyToggle; generated:\n%s", gen)
	}
}
