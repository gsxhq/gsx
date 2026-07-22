package codegen

import (
	"bytes"
	"path/filepath"
	"testing"
)

// TestParseCacheServesIndependentASTs pins the per-file parse cache's core
// invariant: unchanged files are served as independent clones of a pristine
// cached tree, never the shared pristine itself. Two full analyses of the same
// source must (a) produce byte-identical generated output despite the emit
// passes mutating the AST in place (js/css minify, IsComponent stamping,
// Embedded population) and (b) hand out distinct *ast.File pointers. A cache
// that re-served the same mutated tree would fail one or both.
func TestParseCacheServesIndependentASTs(t *testing.T) {
	root := t.TempDir()
	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, root, "go.mod", "module example.com/app\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	writeFile(t, root, "page/other.gsx", "package page\n\ncomponent Other() {\n\t<div>ok</div>\n}\n")
	// page.gsx exercises every in-place mutating pass at once: <style> (CSS
	// minify), <script> (JS minify), and a component tag (IsComponent stamping +
	// positional component call).
	src := "package page\n\ncomponent Home() {\n" +
		"\t<style>.a {\n\t\tcolor: red;\n\t}</style>\n" +
		"\t<script>const answer = 1 + 2;</script>\n" +
		"\t<div>\n\t\t<Other/>\n\t</div>\n" +
		"}\n"
	writeFile(t, root, "page/page.gsx", src)

	m, err := Open(Options{ModuleRoot: root, ModulePath: "example.com/app", JSMinify: true, CSSMinify: true})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	dir := filepath.Join(root, "page")
	pagePath := filepath.Join(dir, "page.gsx")

	// Two full generations over identical source. The second serves cloned
	// pristine trees from the parse cache; the emit passes mutate the clone.
	out1, diags1, err := m.Generate(dir)
	if err != nil {
		t.Fatalf("Generate 1: %v", err)
	}
	out2, diags2, err := m.Generate(dir)
	if err != nil {
		t.Fatalf("Generate 2: %v", err)
	}
	if len(out1[pagePath]) == 0 {
		t.Fatalf("empty generated output for page.gsx; test never reached emit (diags=%v)", diags1)
	}
	if !bytes.Equal(out1[pagePath], out2[pagePath]) {
		t.Fatalf("generated output diverged across two analyses (parse-cache contamination):\n--- run 1 ---\n%s\n--- run 2 ---\n%s", out1[pagePath], out2[pagePath])
	}
	if len(diags1) != len(diags2) {
		t.Fatalf("diagnostics count diverged across analyses: %d vs %d", len(diags1), len(diags2))
	}

	// Independent ASTs: two retained analyses (forced apart by Invalidate, same
	// bytes → parse-cache HIT) must carry distinct *ast.File pointers. A shared
	// pristine tree would make these equal.
	res1, err := m.Package(dir)
	if err != nil {
		t.Fatalf("Package 1: %v", err)
	}
	m.Invalidate(dir)
	res2, err := m.Package(dir)
	if err != nil {
		t.Fatalf("Package 2: %v", err)
	}
	f1 := res1.GSXFiles[pagePath]
	f2 := res2.GSXFiles[pagePath]
	if f1 == nil || f2 == nil {
		t.Fatalf("GSXFiles missing page.gsx: run1=%v run2=%v", f1, f2)
	}
	if f1 == f2 {
		t.Fatal("two independent analyses shared the same *ast.File pointer; parse-cache clone-on-use is not isolating trees")
	}
}
