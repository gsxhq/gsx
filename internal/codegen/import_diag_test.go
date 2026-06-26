package codegen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestUnusedImportDiagnosticMapsToSource verifies that a go/types
// "imported and not used" error on a user import is reported against the .gsx
// SOURCE file (with the correct line), not the synthesized overlay .x.go.
//
// Before the fix, user imports were emitted into the skeleton without a //line
// directive, so the type-checker positioned the error at the overlay .x.go path
// (e.g. pages.x.go:4:8) — a file whose on-disk content (the final generated
// output) is laid out differently, so the rich renderer printed a blank source
// line under the caret.
//
// Source layout (1-based lines):
//
//	line 1: package main
//	line 2: (blank)
//	line 3: import (
//	line 4: \t"context"     ← unused; error must land here
//	line 5: )
func TestUnusedImportDiagnosticMapsToSource(t *testing.T) {
	t.Parallel()
	mod := tempModule(t, "gsximporttest")
	viewsDir := filepath.Join(mod, "views")
	if err := os.MkdirAll(viewsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	src := "package main\n\nimport (\n\t\"context\"\n)\n\ncomponent A() { <div>hi</div> }\n"
	if err := os.WriteFile(filepath.Join(viewsDir, "v.gsx"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := GeneratePackages(mod, []string{viewsDir})
	if err != nil {
		t.Fatalf("GeneratePackages: %v", err)
	}
	pr := out[mustAbs(t, viewsDir)]
	if pr == nil {
		t.Fatal("no PackageResult")
	}

	var found bool
	for _, d := range pr.Diags {
		if d.Source != "types" || !strings.Contains(d.Message, "not used") {
			continue
		}
		found = true
		if !strings.HasSuffix(d.Start.Filename, "v.gsx") {
			t.Errorf("unused-import diagnostic points at %q, want the .gsx source", d.Start.Filename)
		}
		if d.Start.Line != 4 {
			t.Errorf("unused-import diagnostic on wrong line: got %d, want 4", d.Start.Line)
		}
	}
	if !found {
		t.Fatalf("no 'imported and not used' diagnostic found; got %+v", pr.Diags)
	}
}

// TestUnusedImportDiagnosticPerImportLine verifies the .gsx line is resolved
// per-import (via each spec's intra-chunk offset), not anchored at the import
// block's start: here the unused import is the SECOND spec, on line 5, and the
// aliased form is exercised too.
//
//	line 3: import (
//	line 4: \t"io"          ← used (referenced below)
//	line 5: \tx "context"   ← unused alias; error must land here
//	line 6: )
func TestUnusedImportDiagnosticPerImportLine(t *testing.T) {
	t.Parallel()
	mod := tempModule(t, "gsximportline")
	viewsDir := filepath.Join(mod, "views")
	if err := os.MkdirAll(viewsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	src := "package main\n\nimport (\n\t\"io\"\n\tx \"context\"\n)\n\nvar _ io.Writer\n\ncomponent A() { <div>hi</div> }\n"
	if err := os.WriteFile(filepath.Join(viewsDir, "v.gsx"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := GeneratePackages(mod, []string{viewsDir})
	if err != nil {
		t.Fatalf("GeneratePackages: %v", err)
	}
	pr := out[mustAbs(t, viewsDir)]
	if pr == nil {
		t.Fatal("no PackageResult")
	}

	var found bool
	for _, d := range pr.Diags {
		if d.Source != "types" || !strings.Contains(d.Message, "not used") {
			continue
		}
		found = true
		if !strings.HasSuffix(d.Start.Filename, "v.gsx") {
			t.Errorf("diagnostic points at %q, want the .gsx source", d.Start.Filename)
		}
		if d.Start.Line != 5 {
			t.Errorf("aliased unused-import on wrong line: got %d, want 5", d.Start.Line)
		}
	}
	if !found {
		t.Fatalf("no 'imported and not used' diagnostic found; got %+v", pr.Diags)
	}
}
