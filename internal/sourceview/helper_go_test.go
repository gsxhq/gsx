package sourceview

import (
	"os"
	"path/filepath"
	"testing"
)

func TestManifestHelperGoFilesFreezeOverridesAndAbsence(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "go.mod", "module example.com/app\n\ngo 1.26.1\n")
	writeTestFile(t, root, "ui/card.gsx", "package ui\ncomponent Card() { <p/> }\n")
	helper := writeTestFile(t, root, "ui/helper_test.go", "package ui\nfunc _gsxrenderCard() {}\n")
	manifest, err := Build(BuildOptions{ModuleRoot: root, ModulePath: "example.com/app"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(helper, []byte("package ui\nfunc diskChanged() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Dir(helper)
	files := manifest.HelperGoFiles(dir)
	if source, ok := files[helper].Source(); !ok || string(source) != "package ui\nfunc _gsxrenderCard() {}\n" {
		t.Fatalf("frozen helper source = %q, %v", source, ok)
	}

	overridden, err := manifest.WithOverrides(map[string][]byte{helper: []byte("package ui\nfunc overrideName() {}\n")})
	if err != nil {
		t.Fatal(err)
	}
	if source, ok := overridden.HelperGoFiles(dir)[helper].Source(); !ok || string(source) != "package ui\nfunc overrideName() {}\n" {
		t.Fatalf("override helper source = %q, %v", source, ok)
	}

	absent, err := manifest.WithFileSnapshots(map[string]FileSnapshot{helper: AbsentFile()})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot, ok := absent.HelperGoFiles(dir)[helper]; !ok || snapshot.State() != FileAbsent {
		t.Fatalf("absent helper snapshot = %+v, %v", snapshot, ok)
	}
}

func TestManifestRefreshDirsReplacesHelperGoMembership(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "go.mod", "module example.com/app\n\ngo 1.26.1\n")
	writeTestFile(t, root, "ui/card.gsx", "package ui\ncomponent Card() { <p/> }\n")
	dir := filepath.Join(root, "ui")
	oldPath := writeTestFile(t, root, "ui/old_test.go", "package ui\nfunc oldName() {}\n")
	manifest, err := Build(BuildOptions{ModuleRoot: root, ModulePath: "example.com/app"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(oldPath); err != nil {
		t.Fatal(err)
	}
	newPath := writeTestFile(t, root, "ui/new_inactive.go", "//go:build inactive\n\npackage ui\nfunc newName() {}\n")
	if _, ok := manifest.HelperGoFiles(dir)[oldPath]; !ok {
		t.Fatal("frozen helper membership lost old path before refresh")
	}
	if _, ok := manifest.HelperGoFiles(dir)[newPath]; ok {
		t.Fatal("frozen helper membership observed new path before refresh")
	}
	refreshed, err := manifest.RefreshDirs([]string{dir})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := refreshed.HelperGoFiles(dir)[oldPath]; ok {
		t.Fatal("refresh retained removed helper path")
	}
	if source, ok := refreshed.HelperGoFiles(dir)[newPath].Source(); !ok || string(source) != "//go:build inactive\n\npackage ui\nfunc newName() {}\n" {
		t.Fatalf("refreshed helper source = %q, %v", source, ok)
	}
}
