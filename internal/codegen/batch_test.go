package codegen

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gsxhq/gsx/internal/diag"
)

// tempModule creates a temporary Go module with the given module name and returns
// the module root. The caller must create subdirectories and .gsx files.
func tempModule(t *testing.T, moduleName string) string {
	t.Helper()
	repoRoot, _ := filepath.Abs("../..")
	tmp := t.TempDir()
	writeFile(t, tmp, "go.mod",
		"module "+moduleName+"\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n",
	)
	return tmp
}

// makeSubPkg creates a subdirectory under moduleDir and writes the given .gsx
// source as "views.gsx", returning the subdirectory path.
func makeSubPkg(t *testing.T, moduleDir, subdir, gsxSrc string) string {
	t.Helper()
	dir := filepath.Join(moduleDir, subdir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, dir, "views.gsx", gsxSrc)
	return dir
}

// hasDiagErrors reports whether any diagnostic in diags has Error severity.
func hasDiagErrors(diags []diag.Diagnostic) bool {
	for _, d := range diags {
		if d.Severity == diag.Error {
			return true
		}
	}
	return false
}

// TestGeneratePackages_Equivalence checks that GenerateDirs produces output
// that is byte-equal to GeneratePackage run on each individual directory.
func TestGeneratePackages_Equivalence(t *testing.T) {
	t.Parallel()
	tmp := tempModule(t, "gsxbatch")

	dirA := makeSubPkg(t, tmp, "a",
		"package views\n\ncomponent Hello(name string) {\n\t<p>{name}</p>\n}\n",
	)
	dirB := makeSubPkg(t, tmp, "b",
		"package views\n\ncomponent World(count int) {\n\t<span>{count}</span>\n}\n",
	)

	results, err := GenerateDirs(tmp, []string{dirA, dirB}, GenOptions{}, nil)
	if err != nil {
		t.Fatalf("GenerateDirs: %v", err)
	}

	for _, dir := range []string{dirA, dirB} {
		dr, ok := results[dir]
		if !ok {
			t.Errorf("missing result for dir %s", dir)
			continue
		}
		if hasDiagErrors(dr.Diags) {
			t.Errorf("unexpected error diags for dir %s: %v", dir, dr.Diags)
			continue
		}
		if len(dr.Files) == 0 {
			t.Errorf("no files generated for dir %s", dir)
			continue
		}

		// Compare byte-for-byte with GeneratePackage.
		want, err := GeneratePackage(dir)
		if err != nil {
			t.Fatalf("GeneratePackage(%s): %v", dir, err)
		}
		for path, gotBytes := range dr.Files {
			wantBytes, ok := want[path]
			if !ok {
				t.Errorf("dir %s: batch produced extra path %s not in single-package output", dir, path)
				continue
			}
			if !bytes.Equal(gotBytes, wantBytes) {
				t.Errorf("dir %s: file %s output differs between GenerateDirs and GeneratePackage\n--- GenerateDirs ---\n%s\n--- GeneratePackage ---\n%s",
					dir, path, gotBytes, wantBytes)
			}
		}
		for path := range want {
			if _, ok := dr.Files[path]; !ok {
				t.Errorf("dir %s: batch missing path %s that GeneratePackage produced", dir, path)
			}
		}
	}
}

// TestGeneratePackages_ErrorIsolation checks that a type-resolution failure in
// one package does not prevent the others from generating successfully.
func TestGeneratePackages_ErrorIsolation(t *testing.T) {
	t.Parallel()
	tmp := tempModule(t, "gsxbatch")

	dirA := makeSubPkg(t, tmp, "a",
		"package views\n\ncomponent Hello(name string) {\n\t<p>{name}</p>\n}\n",
	)
	dirB := makeSubPkg(t, tmp, "b",
		"package views\n\ncomponent World(count int) {\n\t<span>{count}</span>\n}\n",
	)
	// dirC references an undefined identifier — type resolution must fail for it.
	dirC := makeSubPkg(t, tmp, "c",
		"package views\n\ncomponent Bad() {\n\t<p>{undefinedIdentifier}</p>\n}\n",
	)

	results, err := GenerateDirs(tmp, []string{dirA, dirB, dirC}, GenOptions{}, nil)
	if err != nil {
		t.Fatalf("GenerateDirs returned unexpected top-level error: %v", err)
	}

	for _, tc := range []struct{ name, dir string }{{"a", dirA}, {"b", dirB}} {
		dr, ok := results[tc.dir]
		if !ok {
			t.Errorf("dir %s: missing result", tc.name)
			continue
		}
		if hasDiagErrors(dr.Diags) {
			t.Errorf("dir %s: unexpected errors: %v", tc.name, dr.Diags)
			continue
		}
		if len(dr.Files) == 0 {
			t.Errorf("dir %s: no files generated (silent zero-output failure)", tc.name)
		}
	}
	if dr := results[dirC]; !hasDiagErrors(dr.Diags) {
		t.Errorf("dir c: expected error diagnostics for undefined identifier, got none")
	}
}

// TestGeneratePackages_CrossPackage checks that a component in one package can
// reference a component from another package in the same module, with both
// packages resolved in a single GenerateDirs call and no pre-generated
// .x.go files on disk.
func TestGeneratePackages_CrossPackage(t *testing.T) {
	t.Parallel()
	tmp := tempModule(t, "gsxbatch")

	dirUI := filepath.Join(tmp, "ui")
	if err := os.MkdirAll(dirUI, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, dirUI, "button.gsx",
		"package ui\n\ncomponent Button(label string) {\n\t<button>{label}</button>\n}\n",
	)

	dirPages := filepath.Join(tmp, "pages")
	if err := os.MkdirAll(dirPages, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, dirPages, "home.gsx",
		"package pages\n\nimport \"gsxbatch/ui\"\n\ncomponent Home() {\n\t<ui.Button label=\"Go\"/>\n}\n",
	)

	results, err := GenerateDirs(tmp, []string{dirUI, dirPages}, GenOptions{}, nil)
	if err != nil {
		t.Fatalf("GenerateDirs: %v", err)
	}

	for _, dir := range []string{dirUI, dirPages} {
		dr, ok := results[dir]
		if !ok {
			t.Fatalf("missing result for dir %s", dir)
		}
		if hasDiagErrors(dr.Diags) {
			t.Errorf("dir %s: unexpected errors: %v", dir, dr.Diags)
		}
		if len(dr.Files) == 0 {
			t.Errorf("dir %s: no files generated", dir)
		}
	}
}

// TestGeneratePackages_NonCanonicalDir verifies that callers normalizing dir
// paths before passing to GenerateDirs (e.g. via filepath.Abs which collapses
// redundant "/./") still receive a result keyed by the canonical absolute path.
func TestGeneratePackages_NonCanonicalDir(t *testing.T) {
	t.Parallel()
	tmp := tempModule(t, "gsxbatch")

	dirA := makeSubPkg(t, tmp, "a",
		"package views\n\ncomponent Hello(name string) {\n\t<p>{name}</p>\n}\n",
	)

	// Construct a non-canonical path via a redundant "/./", then normalize.
	// GenerateDirs callers are responsible for passing canonical paths;
	// filepath.Abs does this normalization.
	nonCanonical := strings.Join([]string{tmp, ".", "a"}, string(filepath.Separator))
	canonical, _ := filepath.Abs(nonCanonical) // normalizes "/./"; canonical == dirA

	results, err := GenerateDirs(tmp, []string{canonical}, GenOptions{}, nil)
	if err != nil {
		t.Fatalf("GenerateDirs: %v", err)
	}

	// The result is keyed by the canonical path passed in.
	dr, ok := results[canonical]
	if !ok {
		t.Fatalf("result not found under canonical key %s; keys returned: %v", canonical, mapKeys(results))
	}
	if hasDiagErrors(dr.Diags) {
		t.Errorf("unexpected errors: %v", dr.Diags)
	}
	if len(dr.Files) == 0 {
		t.Errorf("no files generated for non-canonical dir input")
	}
	// Canonical path must equal dirA (the actual directory).
	if canonical != dirA {
		t.Errorf("filepath.Abs did not canonicalize non-canonical path: got %q, want %q", canonical, dirA)
	}
}

// mapKeys returns the keys of a map[string]DirResult for test diagnostics.
func mapKeys(m map[string]DirResult) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

func TestGenerateDirs_CustomFilter(t *testing.T) {
	t.Parallel()
	repoRoot, _ := filepath.Abs("../..")
	tmp := t.TempDir()
	writeFile(t, tmp, "go.mod", "module gsxbatchf\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	// a filter package
	os.MkdirAll(filepath.Join(tmp, "myf"), 0o755)
	writeFile(t, filepath.Join(tmp, "myf"), "f.go", "package myf\n\nfunc Shout(s string) string { return s + \"!\" }\n")
	// a gsx package using the custom filter
	os.MkdirAll(filepath.Join(tmp, "v"), 0o755)
	writeFile(t, filepath.Join(tmp, "v"), "v.gsx", "package v\n\ncomponent C(name string) { <p>{ name |> shout }</p> }\n")

	dirV := filepath.Join(tmp, "v")
	res, err := GenerateDirs(tmp, []string{dirV}, GenOptions{FilterPkgs: []string{"gsxbatchf/myf"}, CSSMinify: true, JSMinify: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	r, ok := res[mustAbs(t, dirV)]
	if !ok || hasDiagErrors(r.Diags) {
		t.Fatalf("expected clean result, got ok=%v diags=%+v", ok, r.Diags)
	}
	if len(r.Files) == 0 {
		t.Fatalf("expected generated files")
	}
}

func mustAbs(t *testing.T, p string) string { t.Helper(); a, _ := filepath.Abs(p); return a }
