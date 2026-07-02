package codegen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A malformed type-param list must produce a positioned diagnostic and must
// NOT take down generation for healthy siblings in the same package.
func TestBadTypeParamListDiagnosticAndSiblingSurvival(t *testing.T) {
	repoRoot, _ := filepath.Abs("../..")
	tmp := t.TempDir()
	writeFile(t, tmp, "go.mod", "module badtp\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	pkgDir := filepath.Join(tmp, "views")
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Missing constraint: go/parser rejects `[T]` in a func type-param list.
	writeFile(t, pkgDir, "bad.gsx", "package views\n\ncomponent Box[T](value T) {\n\t<span>x</span>\n}\n")
	writeFile(t, pkgDir, "good.gsx", "package views\n\ncomponent Ok() {\n\t<p>ok</p>\n}\n")

	out, err := GenerateDirs(tmp, []string{pkgDir}, Options{}, nil)
	if err != nil {
		t.Fatalf("hard error: %v", err)
	}
	dr := out[pkgDir]
	var found bool
	for _, d := range dr.Diags {
		if d.Code == "invalid-syntax" && strings.Contains(d.Message, "type params") {
			found = true
			if !strings.HasSuffix(d.Start.Filename, "bad.gsx") || d.Start.Line != 3 {
				t.Errorf("diagnostic not anchored at bad.gsx:3: %+v", d.Start)
			}
		}
	}
	if !found {
		t.Fatalf("no invalid-syntax diagnostic for the bad type-param list; diags=%+v", dr.Diags)
	}
	var goodGenerated bool
	for path := range dr.Files {
		if strings.HasSuffix(path, "good.gsx") {
			goodGenerated = true
		}
	}
	if !goodGenerated {
		t.Errorf("sibling good.gsx lost its generated output; files=%v", dr.Files)
	}
}

// A malformed type-param list CO-OCCURRING with a reserved param name must not
// regress to the silent whole-package collapse: the reserved-param branch used
// to fire first and pass the T-typed params into the stub while the type-param
// decl was already nulled — re-introducing the undeclared-T skeleton type error.
// The type-param check must take priority: a broken type-param list makes every
// param type suspect.
func TestBadTypeParamListWithReservedParamName(t *testing.T) {
	repoRoot, _ := filepath.Abs("../..")
	tmp := t.TempDir()
	writeFile(t, tmp, "go.mod", "module badtprsv\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	pkgDir := filepath.Join(tmp, "views")
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// BOTH defects at once: `[T]` is missing its constraint AND the param uses
	// the reserved name `children`, typed by the (broken) type param.
	writeFile(t, pkgDir, "bad.gsx", "package views\n\ncomponent Box[T](children T) {\n\t<span>x</span>\n}\n")
	writeFile(t, pkgDir, "good.gsx", "package views\n\ncomponent Ok() {\n\t<p>ok</p>\n}\n")

	out, err := GenerateDirs(tmp, []string{pkgDir}, Options{}, nil)
	if err != nil {
		t.Fatalf("hard error: %v", err)
	}
	dr := out[pkgDir]
	var found bool
	for _, d := range dr.Diags {
		// The type-param check precedes reserved-param validation, so the
		// surfaced diagnostic is invalid-syntax for the type-param list.
		if d.Code == "invalid-syntax" && strings.Contains(d.Message, "type params") {
			found = true
			if !strings.HasSuffix(d.Start.Filename, "bad.gsx") || d.Start.Line != 3 {
				t.Errorf("diagnostic not anchored at bad.gsx:3: %+v", d.Start)
			}
		}
	}
	if !found {
		t.Fatalf("no positioned invalid-syntax diagnostic for the bad type-param list; diags=%+v", dr.Diags)
	}
	var goodGenerated bool
	for path := range dr.Files {
		if strings.HasSuffix(path, "good.gsx") {
			goodGenerated = true
		}
	}
	if !goodGenerated {
		t.Errorf("sibling good.gsx lost its generated output; files=%v", dr.Files)
	}
}

// A malformed type-param list CO-OCCURRING with an unparsable receiver clause
// must not hard-abort the whole run: the early-exit stubs used to emit c.Recv
// verbatim (withRecv=true), so a bad receiver made the entire skeleton
// unparsable → module_importer hard error → every package in the run lost.
// The stub must fall back to a bare function when the receiver doesn't parse.
func TestBadTypeParamListWithUnparsableRecv(t *testing.T) {
	repoRoot, _ := filepath.Abs("../..")
	tmp := t.TempDir()
	writeFile(t, tmp, "go.mod", "module badtprecv\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	pkgDir := filepath.Join(tmp, "views")
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// BOTH defects at once: the receiver clause `(p !)` is invalid Go AND the
	// `[T]` type-param list is missing its constraint.
	writeFile(t, pkgDir, "bad.gsx", "package views\n\ncomponent (p !) Box[T](v T) {\n\t<span>x</span>\n}\n")
	writeFile(t, pkgDir, "good.gsx", "package views\n\ncomponent Ok() {\n\t<p>ok</p>\n}\n")

	out, err := GenerateDirs(tmp, []string{pkgDir}, Options{}, nil)
	if err != nil {
		t.Fatalf("hard error (whole-run abort — the regression this test pins): %v", err)
	}
	dr := out[pkgDir]
	var found bool
	for _, d := range dr.Diags {
		if d.Code == "invalid-syntax" && strings.Contains(d.Message, "type params") {
			found = true
			if !strings.HasSuffix(d.Start.Filename, "bad.gsx") || d.Start.Line != 3 {
				t.Errorf("diagnostic not anchored at bad.gsx:3: %+v", d.Start)
			}
		}
	}
	if !found {
		t.Fatalf("no positioned invalid-syntax diagnostic for the bad type-param list; diags=%+v", dr.Diags)
	}
	var goodGenerated bool
	for path := range dr.Files {
		if strings.HasSuffix(path, "good.gsx") {
			goodGenerated = true
		}
	}
	if !goodGenerated {
		t.Errorf("sibling good.gsx lost its generated output; files=%v", dr.Files)
	}
}

// A reserved param name CO-OCCURRING with an unparsable receiver clause is the
// same early-stub defect class as above (the checkReservedParams stub also
// emitted c.Recv verbatim, breaking the skeleton parse → whole-run hard abort);
// pin it so the stubRecv guard covers every early-exit branch.
func TestReservedParamWithUnparsableRecv(t *testing.T) {
	repoRoot, _ := filepath.Abs("../..")
	tmp := t.TempDir()
	writeFile(t, tmp, "go.mod", "module rsvrecv\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	pkgDir := filepath.Join(tmp, "views")
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, pkgDir, "bad.gsx", "package views\n\ncomponent (p !) Box(children string) {\n\t<span>x</span>\n}\n")
	writeFile(t, pkgDir, "good.gsx", "package views\n\ncomponent Ok() {\n\t<p>ok</p>\n}\n")

	out, err := GenerateDirs(tmp, []string{pkgDir}, Options{}, nil)
	if err != nil {
		t.Fatalf("hard error (whole-run abort — the regression this test pins): %v", err)
	}
	dr := out[pkgDir]
	var found bool
	for _, d := range dr.Diags {
		if d.Code == "reserved-param" {
			found = true
			if !strings.HasSuffix(d.Start.Filename, "bad.gsx") || d.Start.Line != 3 {
				t.Errorf("diagnostic not anchored at bad.gsx:3: %+v", d.Start)
			}
		}
	}
	if !found {
		t.Fatalf("no positioned reserved-param diagnostic; diags=%+v", dr.Diags)
	}
	var goodGenerated bool
	for path := range dr.Files {
		if strings.HasSuffix(path, "good.gsx") {
			goodGenerated = true
		}
	}
	if !goodGenerated {
		t.Errorf("sibling good.gsx lost its generated output; files=%v", dr.Files)
	}
}
