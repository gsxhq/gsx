package codegen

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go/token"

	gsxast "github.com/gsxhq/gsx/ast"
	gsxparser "github.com/gsxhq/gsx/parser"
)

// lineAnchorSrc is a multi-component file where, WITHOUT a //line anchor on each
// generated func declaration, the later funcs' positions drift forward from the
// previous component's body (the bug: go-to-definition landed in the body, not on
// the decl). Components deliberately have multi-line bodies + interleaving so a
// missing anchor produces a clearly wrong line.
const lineAnchorSrc = `package views

// A function/props component with a multi-line body.
component First(name string) {
	<section>
		<h1>Hi {name}</h1>
		<p>welcome</p>
	</section>
}

type Home struct{ Title string }

// A method component (exercises the second emit/skeleton site).
component (h Home) Page() {
	<main>
		<div>{h.Title}</div>
	</main>
}

// A bare nullary function component.
component Last() { <span>done</span> }
`

// declLine returns the 1-based source line of `component <name>` in src.
func declLine(t *testing.T, src, name string) int {
	t.Helper()
	for i, line := range strings.Split(src, "\n") {
		s := strings.TrimSpace(line)
		if strings.HasPrefix(s, "component ") && strings.Contains(s, name) {
			return i + 1
		}
	}
	t.Fatalf("component %q not found in src", name)
	return 0
}

// assertFuncAnchor asserts that in genSrc, the line immediately before the
// generated `func … <name>(` declaration is `//line <file>:<wantLine>:…`. This is
// the behavioral guard for the go-to-definition fix: go/types reports a func's
// position via the preceding //line, so anchoring it to the component decl line
// is what makes go-to-def land on the declaration (not a drifted body line).
func assertFuncAnchor(t *testing.T, genSrc, file, name string, wantLine int) {
	t.Helper()
	lines := strings.Split(genSrc, "\n")
	// The codegen path emits the basename (the .x.go sits beside the .gsx) while
	// the skeleton emits the full .gsx path; both are correct for their context, so
	// match the file:line marker as a substring rather than an exact prefix.
	wantMarker := fmt.Sprintf("%s:%d:", file, wantLine)
	for i, line := range lines {
		// Match the func decl whose name is exactly `name` (function: `func name(`;
		// method: `func (recv) name(`). Guard against substring collisions by
		// requiring `name(` and a `func ` prefix.
		if !strings.HasPrefix(line, "func ") || !strings.Contains(line, name+"(") {
			continue
		}
		if i == 0 {
			t.Fatalf("func %s has no preceding line for a //line anchor", name)
		}
		prev := strings.TrimSpace(lines[i-1])
		if !strings.HasPrefix(prev, "//line ") || !strings.Contains(prev, wantMarker) {
			t.Errorf("func %s: preceding line = %q, want a //line …%s anchor (component decl line %d)",
				name, prev, wantMarker, wantLine)
		}
		return
	}
	t.Fatalf("generated func for component %q not found", name)
}

// TestComponentFuncLineAnchorCodegen verifies the GENERATED .x.go anchors every
// component func declaration to its `component` decl line via a //line directive.
// Regression guard for go-to-definition on a component referenced as a Go call in
// a `{ }` hole (cross-package path), which jumped to a drifted body line before.
func TestComponentFuncLineAnchorCodegen(t *testing.T) {
	repoRoot, _ := filepath.Abs("../..")
	tmp := t.TempDir()
	writeFile(t, tmp, "go.mod", "module gsxa\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	pkgDir := filepath.Join(tmp, "views")
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, pkgDir, "views.gsx", lineAnchorSrc)

	out, err := GeneratePackageWithFilters(pkgDir, nil, nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	var gen string
	for _, b := range out {
		gen += string(b)
	}

	for _, name := range []string{"First", "Page", "Last"} {
		assertFuncAnchor(t, gen, "views.gsx", name, declLine(t, lineAnchorSrc, name))
	}
}

// TestComponentFuncLineAnchorSkeleton verifies the in-memory SKELETON (used by the
// LSP's go/types analysis) anchors every component func to its decl line too.
// Regression guard for the SAME-package go-to-definition path (`{ LocalComp(…) }`),
// which resolves through the skeleton, not the on-disk .x.go.
func TestComponentFuncLineAnchorSkeleton(t *testing.T) {
	repoRoot, _ := filepath.Abs("../..")
	tmp := t.TempDir()
	writeFile(t, tmp, "go.mod", "module gsxa\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	pkgDir := filepath.Join(tmp, "views")
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, pkgDir, "views.gsx", lineAnchorSrc)

	fset := token.NewFileSet()
	file, err := gsxparser.ParseFile(fset, filepath.Join(pkgDir, "views.gsx"), []byte(lineAnchorSrc), 0)
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]*gsxast.File{filepath.Join(pkgDir, "views.gsx"): file}
	propFields, nodeProps, byo, err := componentPropFieldsFor(pkgDir, files)
	if err != nil {
		t.Fatalf("propFields: %v", err)
	}
	table, err := loadFilterTable(pkgDir)
	if err != nil {
		t.Fatalf("loadFilterTable: %v", err)
	}
	skel, _, _, err := buildSkeleton(file, table, propFields, nodeProps, byo, nil, fset)
	if err != nil {
		t.Fatalf("buildSkeleton: %v", err)
	}

	for _, name := range []string{"First", "Page", "Last"} {
		assertFuncAnchor(t, skel, "views.gsx", name, declLine(t, lineAnchorSrc, name))
	}
}
