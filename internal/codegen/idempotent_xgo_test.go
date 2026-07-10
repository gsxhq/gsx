package codegen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Generating twice must produce the same result. The first run leaves a .x.go on
// disk; the second must not type-check that file IN ADDITION to the in-memory
// skeleton built for the same .gsx, or every declaration in it is seen twice.
//
// The skeleton is registered under the .x.go path, and the loop that adds the
// package's hand-written .go files skips paths that already have a skeleton. It
// skipped them by testing `compsByXGo[absPath] != nil` — but that map's value is
// a SLICE of components, and a .gsx that declares no `component` stores a nil
// slice under a present key. Such a file's real .x.go was therefore parsed
// alongside its own skeleton, and `func f` was redeclared.
//
// A .gsx with an element-valued `var` and a plain func has no component, which
// is why this shape and not a component-bearing one exposes it.
func TestGenerateTwiceWithXGoOnDisk(t *testing.T) {
	t.Parallel()
	repoRoot, _ := filepath.Abs("../..")
	tmp := t.TempDir()
	writeFile(t, tmp, "go.mod", "module gsxidem\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	dir := filepath.Join(tmp, "views")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// No `component` anywhere: comps is a nil slice for this file.
	src := `package views

import "github.com/gsxhq/gsx"

var xx = <p>hi</p>

func f() gsx.Node { return xx }
`
	if err := os.WriteFile(filepath.Join(dir, "views.gsx"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}

	gen := func(pass string) {
		t.Helper()
		res, err := GenerateDirs(tmp, []string{dir}, Options{FilterPkgs: []string{stdImportPath}}, nil)
		if err != nil {
			t.Fatalf("%s: generate: %v", pass, err)
		}
		dr := res[dir]
		if hasDiagErrors(dr.Diags) {
			t.Fatalf("%s: unexpected errors: %v", pass, dr.Diags)
		}
		// Write the generated .x.go, exactly as the CLI does, so the next pass
		// sees it on disk.
		for gsxPath, b := range dr.Files {
			xgo := strings.TrimSuffix(gsxPath, ".gsx") + ".x.go"
			if err := os.WriteFile(xgo, b, 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}

	gen("first pass")  // no .x.go on disk
	gen("second pass") // .x.go on disk — must not be type-checked twice
}

// A .gsx that DOES declare a component stores a non-nil comps slice, so the
// buggy `!= nil` guard happened to skip its .x.go. Pinning it guards against a
// fix that swings the other way and starts double-parsing component files.
func TestGenerateTwiceWithXGoOnDiskComponentFile(t *testing.T) {
	t.Parallel()
	repoRoot, _ := filepath.Abs("../..")
	tmp := t.TempDir()
	writeFile(t, tmp, "go.mod", "module gsxidem2\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	dir := filepath.Join(tmp, "views")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	src := `package views

component C() {
	<b>c</b>
}

func helper() string { return "x" }
`
	if err := os.WriteFile(filepath.Join(dir, "views.gsx"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	gen := func(pass string) {
		t.Helper()
		res, err := GenerateDirs(tmp, []string{dir}, Options{FilterPkgs: []string{stdImportPath}}, nil)
		if err != nil {
			t.Fatalf("%s: generate: %v", pass, err)
		}
		dr := res[dir]
		if hasDiagErrors(dr.Diags) {
			t.Fatalf("%s: unexpected errors: %v", pass, dr.Diags)
		}
		for gsxPath, b := range dr.Files {
			xgo := strings.TrimSuffix(gsxPath, ".gsx") + ".x.go"
			if err := os.WriteFile(xgo, b, 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	gen("first pass")
	gen("second pass")
}
