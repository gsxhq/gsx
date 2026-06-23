package gen

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// repoRoot resolves the module root (gen is one level under it).
func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	return root
}

// writeFile writes content to dir/name, creating dir if needed.
func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// newModule creates a temp module with a replace directive pointing at the
// repo root, and returns its directory.
func newModule(t *testing.T, mod string) string {
	t.Helper()
	tmp := t.TempDir()
	writeFile(t, tmp, "go.mod", "module "+mod+"\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot(t)+"\n")
	return tmp
}

// goBuild runs `go build ./...` in dir and fails the test on a non-zero exit.
func goBuild(t *testing.T, dir string) {
	t.Helper()
	cmd := exec.Command("go", "build", "./...")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build ./... failed: %v\n%s", err, out)
	}
}

const hiComponent = `package views

component Hi(name string) {
	<p>{name}</p>
}
`

const byeComponent = `package views

component Bye(name string) {
	<p>bye {name}</p>
}
`

// TestGenerateSinglePackage writes two .gsx files into one package, generates,
// asserts the .x.go files exist on disk, and proves the result compiles.
func TestGenerateSinglePackage(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping go-build test in -short mode")
	}
	mod := newModule(t, "gsxgen")
	pkgDir := filepath.Join(mod, "views")
	writeFile(t, pkgDir, "hi.gsx", hiComponent)
	writeFile(t, pkgDir, "bye.gsx", byeComponent)

	res, err := Generate([]string{pkgDir})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if len(res.Errs) != 0 {
		t.Fatalf("expected no errs, got: %v", res.Errs)
	}
	for _, base := range []string{"hi", "bye"} {
		target := filepath.Join(pkgDir, base+".x.go")
		if _, err := os.Stat(target); err != nil {
			t.Fatalf("expected %s on disk: %v", target, err)
		}
	}
	if len(res.Written) != 2 {
		t.Fatalf("expected 2 written, got %d: %v", len(res.Written), res.Written)
	}
	goBuild(t, mod)
}

// TestGenerateNestedDirs proves two package dirs each with a .gsx are both
// generated and the whole module builds.
func TestGenerateNestedDirs(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping go-build test in -short mode")
	}
	mod := newModule(t, "gsxgennested")
	aDir := filepath.Join(mod, "a")
	bDir := filepath.Join(mod, "nested", "b")
	writeFile(t, aDir, "hi.gsx", "package a\n\ncomponent Hi(name string) {\n\t<p>{name}</p>\n}\n")
	writeFile(t, bDir, "bye.gsx", "package b\n\ncomponent Bye(name string) {\n\t<p>bye {name}</p>\n}\n")

	res, err := Generate([]string{mod})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if len(res.Written) != 2 {
		t.Fatalf("expected 2 written, got %d: %v", len(res.Written), res.Written)
	}
	for _, p := range []string{
		filepath.Join(aDir, "hi.x.go"),
		filepath.Join(bDir, "bye.x.go"),
	} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("expected %s on disk: %v", p, err)
		}
	}
	goBuild(t, mod)
}

// TestGeneratePartialFailure proves a codegen error in one dir is reported
// (as an error-severity diagnostic naming the bad file) and writes nothing for
// that dir, while a good dir in the same call IS written.
func TestGeneratePartialFailure(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping module-resolution test in -short mode")
	}
	mod := newModule(t, "gsxgenpartial")
	goodDir := filepath.Join(mod, "good")
	badDir := filepath.Join(mod, "bad")
	writeFile(t, goodDir, "hi.gsx", "package good\n\ncomponent Hi(name string) {\n\t<p>{name}</p>\n}\n")
	// References an undefined symbol -> codegen type-resolution error.
	writeFile(t, badDir, "bad.gsx", "package bad\n\ncomponent Bad() {\n\t<p>{undefinedSymbol}</p>\n}\n")

	res, err := Generate([]string{goodDir, badDir})
	if err == nil {
		t.Fatal("expected a non-nil combined error, got nil")
	}
	// Error diagnostics now live in res.Diags (not res.Errs).
	var badFileDiag bool
	for _, d := range res.Diags {
		if strings.Contains(d.Start.Filename, badDir) {
			badFileDiag = true
			break
		}
	}
	if !badFileDiag {
		t.Fatalf("expected an error diagnostic with a file path under the bad dir %q; diags: %v", badDir, res.Diags)
	}
	// Bad dir: nothing written.
	if _, statErr := os.Stat(filepath.Join(badDir, "bad.x.go")); statErr == nil {
		t.Fatal("expected NO .x.go written for the bad dir")
	}
	// Good dir: written.
	if _, statErr := os.Stat(filepath.Join(goodDir, "hi.x.go")); statErr != nil {
		t.Fatalf("expected good dir .x.go written: %v", statErr)
	}
	if len(res.Written) != 1 {
		t.Fatalf("expected 1 written (good dir), got %d: %v", len(res.Written), res.Written)
	}
	// Operational errors (I/O, load failures) must be empty — codegen errors are in res.Diags.
	if len(res.Errs) != 0 {
		t.Fatalf("expected 0 operational errs (codegen errors go to res.Diags), got %d: %v", len(res.Errs), res.Errs)
	}
}

// TestGenerateNoGsxDir proves a dir with no .gsx files is a clean no-op.
func TestGenerateNoGsxDir(t *testing.T) {
	tmp := t.TempDir()
	res, err := Generate([]string{tmp})
	if err != nil {
		t.Fatalf("expected no error for empty dir, got: %v", err)
	}
	if len(res.Written) != 0 || len(res.Errs) != 0 {
		t.Fatalf("expected empty result, got Written=%v Errs=%v", res.Written, res.Errs)
	}
}

// TestGenerateMissingPath proves a non-existent path is an error.
func TestGenerateMissingPath(t *testing.T) {
	_, err := Generate([]string{"/does/not/exist/anywhere"})
	if err == nil {
		t.Fatal("expected error for missing path, got nil")
	}
}

func TestDiscoverDirsFileArg(t *testing.T) {
	mod := t.TempDir()
	pkgDir := filepath.Join(mod, "views")
	gsxPath := writeFile(t, pkgDir, "hi.gsx", hiComponent)

	dirs, err := discoverDirs([]string{gsxPath})
	if err != nil {
		t.Fatalf("discoverDirs: %v", err)
	}
	if len(dirs) != 1 || dirs[0] != pkgDir {
		t.Fatalf("expected [%s], got %v", pkgDir, dirs)
	}
}

func TestDiscoverDirsSkipsJunk(t *testing.T) {
	root := t.TempDir()
	// A real package dir.
	writeFile(t, filepath.Join(root, "pkg"), "a.gsx", hiComponent)
	// Junk dirs that each contain a .gsx but must be skipped.
	for _, junk := range []string{".git", ".hidden", "vendor", "node_modules", "testdata"} {
		writeFile(t, filepath.Join(root, junk), "x.gsx", hiComponent)
		// nested deeper too
		writeFile(t, filepath.Join(root, junk, "sub"), "y.gsx", hiComponent)
	}

	dirs, err := discoverDirs([]string{root})
	if err != nil {
		t.Fatalf("discoverDirs: %v", err)
	}
	want := filepath.Join(root, "pkg")
	if len(dirs) != 1 || dirs[0] != want {
		t.Fatalf("expected only [%s], got %v", want, dirs)
	}
}

func TestDiscoverDirsDefaultsToCwd(t *testing.T) {
	// Empty paths must default to ["."] and not error.
	if _, err := discoverDirs(nil); err != nil {
		t.Fatalf("discoverDirs(nil): %v", err)
	}
}

func TestDiscoverDirsSortedUnique(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "b"), "x.gsx", hiComponent)
	writeFile(t, filepath.Join(root, "a"), "x.gsx", hiComponent)
	// Pass overlapping paths (root + a) to exercise dedupe.
	dirs, err := discoverDirs([]string{root, filepath.Join(root, "a")})
	if err != nil {
		t.Fatalf("discoverDirs: %v", err)
	}
	want := []string{filepath.Join(root, "a"), filepath.Join(root, "b")}
	if len(dirs) != len(want) {
		t.Fatalf("expected %v, got %v", want, dirs)
	}
	for i := range want {
		if dirs[i] != want[i] {
			t.Fatalf("expected sorted unique %v, got %v", want, dirs)
		}
	}
}
