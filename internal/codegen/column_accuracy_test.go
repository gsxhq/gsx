package codegen

import (
	"os"
	"path/filepath"
	"testing"
)

// TestInterpColumnAccuracy verifies that a type error on an interpolation
// expression is reported at the EXACT source column of the offending
// identifier, not offset by the _gsxuse( wrapper (8 chars).
//
// Source layout (1-based columns as go/token counts them):
//
//	line 4: "\t<div>{ undefinedX }</div>\n"
//	  col 1 = \t
//	  col 2 = <
//	  col 3 = d
//	  col 4 = i
//	  col 5 = v
//	  col 6 = >
//	  col 7 = {       ← Interp.Pos() (the '{')
//	  col 8 = space
//	  col 9 = u       ← undefinedX starts here (ExprPos after this fix)
//
// Before this fix: //line points to col 7 ({), so probe's first token (at
// byte 8 = len("_gsxuse(")) reports col 7+8=15.
// After this fix: column must be 9.
func TestInterpColumnAccuracy(t *testing.T) {
	mod := tempModule(t, "gsxcoltest")
	viewsDir := filepath.Join(mod, "views")
	if err := os.MkdirAll(viewsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Interpolation on line 4; 'undefinedX' starts at column 9 (tab + "<div>{ ").
	src := "package views\n\ncomponent A() {\n\t<div>{ undefinedX }</div>\n}\n"
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
		if d.Source != "types" {
			continue
		}
		// Must be on line 4, column 9.
		if d.Start.Line != 4 {
			t.Errorf("type error on wrong line: got %d, want 4", d.Start.Line)
			continue
		}
		found = true
		if d.Start.Column != 9 {
			t.Errorf("interp column inaccurate: got %d, want 9 (before fix this was 17 = 9 + 8 probe-wrapper offset)",
				d.Start.Column)
		}
	}
	if !found {
		t.Error("no 'types' diagnostic found — undefinedX was not reported as a type error")
	}
}
