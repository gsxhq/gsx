package gen

import (
	"bytes"
	"testing"

	"github.com/gsxhq/gsx/internal/codegen"
	"github.com/gsxhq/gsx/internal/diag"
)

// TestCachedResolverGenerate proves that Generate of a valid component yields
// the expected generated Go with no diagnostics.
func TestCachedResolverGenerate(t *testing.T) {
	r, err := NewCachedResolver(repoRoot(t), DefaultPlaygroundImports)
	if err != nil {
		t.Fatal(err)
	}
	src := map[string][]byte{
		"views/comp.gsx": []byte("package views\n\ncomponent G(s string){\n\t<p>{s}</p>\n}\n"),
	}
	res, err := r.Generate("views", src)
	if err != nil {
		t.Fatalf("Generate: %v (diags: %+v)", err, res.Diags)
	}
	if len(res.Diags) != 0 {
		t.Fatalf("expected no diagnostics, got: %+v", res.Diags)
	}
	got := res.Files["views/comp.x.go"]
	if !bytes.Contains(got, []byte("func G(")) {
		t.Fatalf("generated Go missing func G:\n%s", got)
	}
}

// TestCachedResolverTypeError proves that a component with a type error
// ({missng}) yields a positioned diagnostic in the .gsx source, and that the
// position matches the default path's diagnostic for the same input.
func TestCachedResolverTypeError(t *testing.T) {
	const badSrc = "package views\n\ncomponent Bad() {\n\t<p>{missng}</p>\n}\n"

	// --- Cached path ---
	r, err := NewCachedResolver(repoRoot(t), DefaultPlaygroundImports)
	if err != nil {
		t.Fatal(err)
	}
	src := map[string][]byte{
		"views/bad.gsx": []byte(badSrc),
	}
	cachedRes, _ := r.Generate("views", src)
	var cachedDiag *diag.Diagnostic
	for i := range cachedRes.Diags {
		d := &cachedRes.Diags[i]
		if d.Severity == diag.Error {
			cachedDiag = d
			break
		}
	}
	if cachedDiag == nil {
		t.Fatalf("cached path: expected an error diagnostic for {missng}, got none (diags: %+v)", cachedRes.Diags)
	}
	if cachedDiag.Start.Line == 0 {
		t.Errorf("cached path: diagnostic has no line position: %+v", cachedDiag)
	}

	// --- Default path (packages.Load) ---
	// Build a temp module so packages.Load can resolve imports.
	mod := newModule(t, "gsxresolvererr")
	// Write the bad component into the temp module's views package.
	viewsDir := mod + "/views"
	writeFile(t, viewsDir, "bad.gsx", badSrc)
	defaultRes, err2 := codegen.GeneratePackagesWithFilters(mod, []string{viewsDir}, nil, nil, nil, nil, nil, nil, true, true, nil)
	if err2 != nil {
		t.Fatalf("default path: GeneratePackagesWithFilters error: %v", err2)
	}
	defaultPR := defaultRes[viewsDir]
	if defaultPR == nil {
		t.Fatal("default path: no result for viewsDir")
	}
	var defaultDiag *diag.Diagnostic
	for i := range defaultPR.Diags {
		d := &defaultPR.Diags[i]
		if d.Severity == diag.Error {
			defaultDiag = d
			break
		}
	}
	if defaultDiag == nil {
		t.Fatalf("default path: expected an error diagnostic for {missng}, got none (diags: %+v)", defaultPR.Diags)
	}

	// Both paths must produce a diagnostic at the same line/column in the .gsx source.
	if cachedDiag.Start.Line != defaultDiag.Start.Line {
		t.Errorf("line mismatch: cached=%d default=%d",
			cachedDiag.Start.Line, defaultDiag.Start.Line)
	}
	if cachedDiag.Start.Column != defaultDiag.Start.Column {
		t.Errorf("column mismatch: cached=%d default=%d",
			cachedDiag.Start.Column, defaultDiag.Start.Column)
	}
	t.Logf("cached  diag: line=%d col=%d msg=%q file=%q",
		cachedDiag.Start.Line, cachedDiag.Start.Column, cachedDiag.Message, cachedDiag.Start.Filename)
	t.Logf("default diag: line=%d col=%d msg=%q file=%q",
		defaultDiag.Start.Line, defaultDiag.Start.Column, defaultDiag.Message, defaultDiag.Start.Filename)
}
