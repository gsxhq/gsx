package codegen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A mixed type parameter on a PLAIN name renders through AttrAnyToggle, which
// decides presence-vs-value at runtime but only ATTRIBUTE-escapes the string
// case. On a name with a sink that would ship an unsanitized value where the
// sink sanitizes one — href={u} with T string|int emitting javascript: straight
// through — so the toggle branch is restricted to plain context and every sink
// name routes to its runtime twin instead (URLVal, RefreshContentVal, …).
//
// This guards a regression introduced while widening BoolRendersBare: the
// toggle branch used to be reachable only for the ~25 HTML boolean attribute
// names, none of them a sink, and widening the predicate silently put every
// sink name in its path.
//
// The render side of this is pinned by corpus security/url_sink_anymixed and
// security/meta_refresh_unknown_kind; what is asserted HERE is the emit
// decision, which those goldens only imply.
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
	<meta http-equiv="refresh" content={ u } />
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

	for _, name := range []string{`AttrAnyToggle("href"`, `AttrAnyToggle("@click"`, `AttrAnyToggle("content"`} {
		if strings.Contains(gen, name) {
			t.Errorf("%s bypasses the value sink; generated:\n%s", name, gen)
		}
	}
	// Each sink's runtime twin owns its name: the static form takes a string,
	// which a mixed type parameter cannot be converted to at generate time.
	for _, want := range []string{"_gsxgw.URLVal(", "_gsxgw.RefreshContentVal("} {
		if !strings.Contains(gen, want) {
			t.Errorf("a mixed type parameter must route through %s; generated:\n%s", want, gen)
		}
	}
	// Plain context keeps the runtime presence-vs-value decision.
	if !strings.Contains(gen, `AttrAnyToggle("data-x"`) {
		t.Errorf("data-x on a mixed type parameter should use AttrAnyToggle; generated:\n%s", gen)
	}
}
