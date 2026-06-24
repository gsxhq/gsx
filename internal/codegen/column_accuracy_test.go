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

// TestInterpShallowColumnNoRegression verifies that a shallow interp
// (identifier at source column ≤ 8, where the compensated //line col would be
// < 1) falls back to the '{'-anchored position — the same as pre-column-accuracy
// behavior — and does NOT report a column that is worse (i.e., column of
// exprPos + 8).
//
// Source layout (1-based columns):
//
//	line 4: "{ undefinedA }\n"   (no leading whitespace)
//	  col 1 = {       ← Interp.Pos()
//	  col 2 = space
//	  col 3 = u       ← undefinedA ExprPos; compensated col = 3−8 = −5 < 1 → fallback
//
// With the buggy fallback (emitSkeletonLine at ExprPos col 3), the probe at
// byte offset 8 causes the type-checker to report col 3+8 = 11.
// With the fix (emitSkeletonLine at Pos() col 1), the probe reports col 1+8 = 9.
// The '{' col is 1, so base anchored col = 1+8 = 9.
// Assertion: reported column ≤ (col of '{') + 8.
func TestInterpShallowColumnNoRegression(t *testing.T) {
	mod := tempModule(t, "gsxshallowtest")
	viewsDir := filepath.Join(mod, "views")
	if err := os.MkdirAll(viewsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Shallow interp: '{ undefinedA }' at column 1 — identifier at col 3.
	// exprCol(3) - probePrefixLen(8) = -5 < 1 → fallback branch.
	src := "package views\n\ncomponent A() {\n{ undefinedA }\n}\n"
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

	// col of '{' is 1; base-anchored column = 1 + 8 = 9.
	const braceCol = 1
	const probePrefixLen = 8
	baseCol := braceCol + probePrefixLen // 9

	var found bool
	for _, d := range pr.Diags {
		if d.Source != "types" {
			continue
		}
		if d.Start.Line != 4 {
			t.Errorf("type error on wrong line: got %d, want 4", d.Start.Line)
			continue
		}
		found = true
		// Must NOT be worse than base ({-anchored). With the bug the buggy fallback
		// anchors at ExprPos (col 3), reporting col 3+8=11, which is > baseCol(9).
		if d.Start.Column > baseCol {
			t.Errorf("shallow interp column regression: got %d, want ≤ %d (base); buggy exprPos-anchored fallback would give %d",
				d.Start.Column, baseCol, 3+probePrefixLen)
		}
	}
	if !found {
		t.Error("no 'types' diagnostic found — undefinedA was not reported as a type error")
	}
}
