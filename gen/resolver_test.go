package gen

import (
	"bytes"
	"strings"
	"testing"

	"github.com/gsxhq/gsx/internal/codegen"
	"github.com/gsxhq/gsx/internal/diag"
)

// TestCachedResolverGenerate proves that Generate of a valid component yields
// the expected generated Go with no diagnostics.
func TestCachedResolverGenerate(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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

	// --- Default path (GenerateDirs) ---
	// Build a temp module so packages.Load can resolve imports.
	mod := newModule(t, "gsxresolvererr")
	// Write the bad component into the temp module's views package.
	viewsDir := mod + "/views"
	writeFile(t, viewsDir, "bad.gsx", badSrc)
	defaultRes, err2 := codegen.GenerateDirs(mod, []string{viewsDir}, codegen.Options{}, nil)
	if err2 != nil {
		t.Fatalf("default path: GenerateDirs error: %v", err2)
	}
	defaultDR := defaultRes[viewsDir]
	var defaultDiag *diag.Diagnostic
	for i := range defaultDR.Diags {
		d := &defaultDR.Diags[i]
		if d.Severity == diag.Error {
			defaultDiag = d
			break
		}
	}
	if defaultDiag == nil {
		t.Fatalf("default path: expected an error diagnostic for {missng}, got none (diags: %+v)", defaultDR.Diags)
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

func TestCachedResolverParseErrorDiagnostic(t *testing.T) {
	t.Parallel()
	const badSrc = `package views

component Meter(value int, color string) {
	<div
		class={ "meter", "meter-full": value >= 100 }
		style={ value |> format("width: %d%%"); "color: " + color }
	/>
}
`
	r, err := NewCachedResolver(repoRoot(t), DefaultPlaygroundImports)
	if err != nil {
		t.Fatal(err)
	}
	res, _ := r.GenerateSource("meter.gsx", []byte(badSrc))
	if len(res.Files) != 0 {
		t.Fatalf("parse error should not produce generated files: %v", res.Files)
	}
	if len(res.Diags) == 0 {
		t.Fatal("expected parse error diagnostic, got none")
	}
	d := res.Diags[0]
	if d.Severity != diag.Error || d.Start.Line != 6 || d.Start.Column != 41 {
		t.Fatalf("diagnostic = %+v, want error at 6:41", d)
	}
	if d.End.Line != 6 || d.End.Column <= d.Start.Column {
		t.Fatalf("diagnostic range = %d:%d..%d:%d, want non-empty range on line 6", d.Start.Line, d.Start.Column, d.End.Line, d.End.Column)
	}
	if !strings.Contains(d.Message, "trailing text after `)`") {
		t.Fatalf("diagnostic message = %q", d.Message)
	}
}
