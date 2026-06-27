package codegen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gsxhq/gsx/internal/fullmin"
)

// TestMinifyThreading verifies that CSSMinify/JSMinify/CSSMin/JSMin fields in
// Options are correctly threaded through Module.Generate: different minify
// configurations produce different output for a component with a static
// holeless <style> block containing collapsible whitespace.
//
// Three configs are tested:
//
//	builtinMin: CSSMinify=true, CSSMin=nil  → built-in safe whitespace pass
//	noMin:      CSSMinify=false             → no minification
//	fullMin:    CSSMinify=true, CSSMin=fullmin.CSS → aggressive tdewolff pass
func TestMinifyThreading(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	t.Parallel()

	root := t.TempDir()
	repoRoot, _ := filepath.Abs("../..")
	writeFile(t, root, "go.mod",
		"module example.com/mintest\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")

	// Static holeless <style> with collapsible whitespace: multiple spaces between
	// tokens so the built-in safe minifier and fullmin.CSS both transform it, but
	// differently. The built-in preserves spaces around non-delimiter bytes (e.g.
	// spaces around the colon), while fullmin removes them.
	pkgDir := filepath.Join(root, "views")
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	gsxPath := filepath.Join(pkgDir, "views.gsx")
	writeFile(t, pkgDir, "views.gsx",
		"package views\n\ncomponent Page() {\n\t<style>.a {  color : red ;  }</style>\n}\n")

	generate := func(opts Options) string {
		opts.ModuleRoot = root
		opts.ModulePath = "example.com/mintest"
		opts.FilterPkgs = []string{StdImportPath}
		m, err := Open(opts)
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		out, diags, err := m.Generate(pkgDir)
		if err != nil {
			t.Fatalf("Generate: %v (diags=%v)", err, diags)
		}
		b := out[gsxPath]
		if b == nil {
			t.Fatalf("no output for %s; out=%v", gsxPath, out)
		}
		return string(b)
	}

	builtinMin := generate(Options{CSSMinify: true})
	noMin := generate(Options{CSSMinify: false})
	fullMin := generate(Options{CSSMinify: true, CSSMin: fullmin.CSS})

	snip := func(gen, label string) string {
		start := strings.Index(gen, `_gsxgw.S("<style>`)
		if start == -1 {
			t.Fatalf("%s: cannot find _gsxgw.S(\"<style>\") in generated output:\n%s", label, gen)
		}
		end := strings.Index(gen[start:], "\n")
		if end == -1 {
			return gen[start:]
		}
		return gen[start : start+end]
	}

	builtinSnip := snip(builtinMin, "builtinMin")
	noMinSnip := snip(noMin, "noMin")
	fullMinSnip := snip(fullMin, "fullMin")

	t.Logf("builtinMin: %s", builtinSnip)
	t.Logf("noMin:      %s", noMinSnip)
	t.Logf("fullMin:    %s", fullMinSnip)

	// noMin: original CSS preserved verbatim — double spaces intact, semicolon
	// before closing brace retained, spaces around colon preserved.
	const wantNoMin = `_gsxgw.S("<style>.a {  color : red ;  }</style>")`
	if !strings.Contains(noMin, wantNoMin) {
		t.Errorf("noMin: expected verbatim CSS; got snippet: %s", noMinSnip)
	}

	// builtinMin: safe whitespace pass — curly-brace-adjacent spaces dropped,
	// double space collapsed, trailing semicolon before } stripped. Spaces around
	// the colon are preserved (colon is not a CSS delimiter in this pass).
	const wantBuiltin = `_gsxgw.S("<style>.a{color : red}</style>")`
	if !strings.Contains(builtinMin, wantBuiltin) {
		t.Errorf("builtinMin: expected safe-minified CSS; got snippet: %s", builtinSnip)
	}

	// fullMin: aggressive tdewolff pass — all whitespace around property name,
	// colon, and value removed.
	const wantFull = `_gsxgw.S("<style>.a{color:red}</style>")`
	if !strings.Contains(fullMin, wantFull) {
		t.Errorf("fullMin: expected aggressively-minified CSS; got snippet: %s", fullMinSnip)
	}

	// Non-tautological: the three outputs must all differ.
	if noMin == builtinMin {
		t.Errorf("noMin and builtinMin produced identical output (CSSMinify not threaded?)")
	}
	if builtinMin == fullMin {
		t.Errorf("builtinMin and fullMin produced identical output (CSSMin func not threaded?)")
	}
	if noMin == fullMin {
		t.Errorf("noMin and fullMin produced identical output")
	}
}
