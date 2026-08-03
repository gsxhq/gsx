package codegen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestUnusedImportDiagnosticMapsToSource verifies that a go/types
// "imported and not used" error on a user import is reported against the .gsx
// SOURCE file, at the exact line AND column of the offending import spec, not
// the synthesized overlay .x.go.
//
// Before the fix, user imports were emitted into the skeleton without a //line
// directive, so the type-checker positioned the error at the overlay .x.go path
// (e.g. pages.x.go:4:8) — a file whose on-disk content (the final generated
// output) is laid out differently, so the rich renderer printed a blank source
// line under the caret.
//
// Source layout (1-based lines and columns):
//
//	line 1: package main
//	line 2: (blank)
//	line 3: import (
//	line 4: \t"context"     ← unused; error must land here, at column 2
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

	out, err := GenerateDirs(mod, []string{viewsDir}, Options{}, nil)
	if err != nil {
		t.Fatalf("GenerateDirs: %v", err)
	}
	dr := out[viewsDir]

	var found bool
	for _, d := range dr.Diags {
		if d.Source != "types" || !strings.Contains(d.Message, "not used") {
			continue
		}
		found = true
		if !strings.HasSuffix(d.Start.Filename, "v.gsx") {
			t.Errorf("unused-import diagnostic points at %q, want the .gsx source", d.Start.Filename)
		}
		if d.Start.Line != 4 || d.Start.Column != 2 {
			t.Errorf("unused-import diagnostic at %d:%d, want 4:2", d.Start.Line, d.Start.Column)
		}
	}
	if !found {
		t.Fatalf("no 'imported and not used' diagnostic found; got %+v", dr.Diags)
	}
}

// TestUnusedImportDiagnosticPerImportLine verifies each unused import resolves
// to its OWN .gsx line and column — the line via the spec's intra-chunk offset,
// the column via the inline `/*line*/` directive that anchors the spec itself.
//
// The fixture deliberately varies the indentation so a single wrong column
// cannot satisfy every case. A line-form `//line` directive can only set the
// next line's FIRST column, but the skeleton writes the spec after a 7-byte
// "import " keyword; the old code compensated by subtracting 7 and clamping at
// 1, which collapsed EVERY import indented by less than 7 columns onto column 8.
// The expectations below are 2, 1, 3 and 8 — the first three all reported 8
// before the fix, and the last one guards the shape the old code got right.
//
//	line  3: import (
//	line  4: \t"io"            ← used (referenced below)
//	line  5: \tx "context"     ← unused alias, tab-indented   → 5:2
//	line  6: "strings"         ← unused, unindented           → 6:1
//	line  7: \t\ty "bytes"     ← unused alias, two tabs       → 7:3
//	line  8: )
//	line 10: import "errors"   ← unused, single-line form     → 10:8
func TestUnusedImportDiagnosticPerImportLine(t *testing.T) {
	t.Parallel()
	mod := tempModule(t, "gsximportline")
	viewsDir := filepath.Join(mod, "views")
	if err := os.MkdirAll(viewsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	src := "package main\n\nimport (\n\t\"io\"\n\tx \"context\"\n\"strings\"\n\t\ty \"bytes\"\n)\n\nimport \"errors\"\n\nvar _ io.Writer\n\ncomponent A() { <div>hi</div> }\n"
	if err := os.WriteFile(filepath.Join(viewsDir, "v.gsx"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}

	// path → expected 1-based .gsx position of the "imported and not used" error.
	want := map[string][2]int{
		"context": {5, 2},
		"strings": {6, 1},
		"bytes":   {7, 3},
		"errors":  {10, 8},
	}

	out, err := GenerateDirs(mod, []string{viewsDir}, Options{}, nil)
	if err != nil {
		t.Fatalf("GenerateDirs: %v", err)
	}

	got := map[string][2]int{}
	for _, d := range out[viewsDir].Diags {
		if d.Source != "types" || !strings.Contains(d.Message, "not used") {
			continue
		}
		if !strings.HasSuffix(d.Start.Filename, "v.gsx") {
			t.Errorf("diagnostic points at %q, want the .gsx source", d.Start.Filename)
			continue
		}
		for path := range want {
			if strings.Contains(d.Message, `"`+path+`"`) {
				got[path] = [2]int{d.Start.Line, d.Start.Column}
			}
		}
	}

	for path, wantPos := range want {
		gotPos, ok := got[path]
		if !ok {
			t.Errorf("no 'imported and not used' diagnostic for %q; got %v", path, got)
			continue
		}
		if gotPos != wantPos {
			t.Errorf("unused import %q at %d:%d, want %d:%d", path, gotPos[0], gotPos[1], wantPos[0], wantPos[1])
		}
	}
}
