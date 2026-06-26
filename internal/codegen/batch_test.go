package codegen

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
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

// TestGeneratePackages_Equivalence checks that GeneratePackages produces output
// that is byte-equal to GeneratePackage run on each individual directory.
func TestGeneratePackages_Equivalence(t *testing.T) {
	tmp := tempModule(t, "gsxbatch")

	dirA := makeSubPkg(t, tmp, "a",
		"package views\n\ncomponent Hello(name string) {\n\t<p>{name}</p>\n}\n",
	)
	dirB := makeSubPkg(t, tmp, "b",
		"package views\n\ncomponent World(count int) {\n\t<span>{count}</span>\n}\n",
	)

	results, err := GeneratePackages(tmp, []string{dirA, dirB})
	if err != nil {
		t.Fatalf("GeneratePackages: %v", err)
	}

	for _, dir := range []string{dirA, dirB} {
		res, ok := results[dir]
		if !ok {
			t.Errorf("missing result for dir %s", dir)
			continue
		}
		if res.Err != nil {
			t.Errorf("unexpected error for dir %s: %v", dir, res.Err)
			continue
		}
		if len(res.Files) == 0 {
			t.Errorf("no files generated for dir %s", dir)
			continue
		}

		// Compare byte-for-byte with GeneratePackage.
		want, err := GeneratePackage(dir)
		if err != nil {
			t.Fatalf("GeneratePackage(%s): %v", dir, err)
		}
		for path, gotBytes := range res.Files {
			wantBytes, ok := want[path]
			if !ok {
				t.Errorf("dir %s: batch produced extra path %s not in single-package output", dir, path)
				continue
			}
			if !bytes.Equal(gotBytes, wantBytes) {
				t.Errorf("dir %s: file %s output differs between GeneratePackages and GeneratePackage\n--- GeneratePackages ---\n%s\n--- GeneratePackage ---\n%s",
					dir, path, gotBytes, wantBytes)
			}
		}
		for path := range want {
			if _, ok := res.Files[path]; !ok {
				t.Errorf("dir %s: batch missing path %s that GeneratePackage produced", dir, path)
			}
		}
	}
}

// TestGeneratePackages_ErrorIsolation checks that a type-resolution failure in
// one package does not prevent the others from generating successfully.
func TestGeneratePackages_ErrorIsolation(t *testing.T) {
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

	results, err := GeneratePackages(tmp, []string{dirA, dirB, dirC})
	if err != nil {
		t.Fatalf("GeneratePackages returned unexpected top-level error: %v", err)
	}

	for _, tc := range []struct{ name, dir string }{{"a", dirA}, {"b", dirB}} {
		res := results[tc.dir]
		if res == nil {
			t.Errorf("dir %s: missing result", tc.name)
			continue
		}
		if res.Err != nil {
			t.Errorf("dir %s: unexpected error: %v", tc.name, res.Err)
			continue
		}
		if len(res.Files) == 0 {
			t.Errorf("dir %s: no files generated (silent zero-output failure)", tc.name)
		}
	}
	if res := results[dirC]; res.Err == nil {
		t.Errorf("dir c: expected error for undefined identifier, got nil")
	}
}

// TestGeneratePackages_CrossPackage checks that a component in one package can
// reference a component from another package in the same module, with both
// packages resolved in a single GeneratePackages call and no pre-generated
// .x.go files on disk.
func TestGeneratePackages_CrossPackage(t *testing.T) {
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

	results, err := GeneratePackages(tmp, []string{dirUI, dirPages})
	if err != nil {
		t.Fatalf("GeneratePackages: %v", err)
	}

	for _, dir := range []string{dirUI, dirPages} {
		res, ok := results[dir]
		if !ok {
			t.Fatalf("missing result for dir %s", dir)
		}
		if res.Err != nil {
			t.Errorf("dir %s: unexpected error: %v", dir, res.Err)
		}
		if len(res.Files) == 0 {
			t.Errorf("dir %s: no files generated", dir)
		}
	}
}

// TestGeneratePackages_NonCanonicalDir verifies that non-canonical dir paths
// (containing redundant "/./") are normalized and still produce output. Before
// Fix 1, such a path would never match the go/packages-returned absolute dir,
// causing a silent zero-output result.
func TestGeneratePackages_NonCanonicalDir(t *testing.T) {
	tmp := tempModule(t, "gsxbatch")

	dirA := makeSubPkg(t, tmp, "a",
		"package views\n\ncomponent Hello(name string) {\n\t<p>{name}</p>\n}\n",
	)

	// Construct a non-canonical path via a redundant "/./".
	nonCanonical := strings.Join([]string{tmp, ".", "a"}, string(filepath.Separator))

	results, err := GeneratePackages(tmp, []string{nonCanonical})
	if err != nil {
		t.Fatalf("GeneratePackages: %v", err)
	}

	// The result must be keyed by the normalized (canonical) absolute path.
	res, ok := results[dirA]
	if !ok {
		t.Fatalf("result not found under canonical key %s; keys returned: %v", dirA, mapKeys(results))
	}
	if res.Err != nil {
		t.Errorf("unexpected error: %v", res.Err)
	}
	if len(res.Files) == 0 {
		t.Errorf("no files generated for non-canonical dir input")
	}
}

// mapKeys returns the keys of a map[string]*PackageResult for test diagnostics.
func mapKeys(m map[string]*PackageResult) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

func TestGeneratePackagesWithFilters_CustomFilter(t *testing.T) {
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
	res, err := GeneratePackagesWithFilters(tmp, []string{dirV}, []string{"gsxbatchf/myf"}, nil, nil, nil, nil, nil, true, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	r := res[mustAbs(t, dirV)]
	if r == nil || r.Err != nil {
		t.Fatalf("expected clean result, got %+v", r)
	}
	if len(r.Files) == 0 {
		t.Fatalf("expected generated files")
	}
}

func mustAbs(t *testing.T, p string) string { t.Helper(); a, _ := filepath.Abs(p); return a }
