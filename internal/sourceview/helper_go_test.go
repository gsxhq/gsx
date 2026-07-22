package sourceview

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
)

func TestManifestGoOnlyDirectoryJoinsFirstGsxOverride(t *testing.T) {
	root := t.TempDir()
	goMod := writeTestFile(t, root, "go.mod", "module example.com/app\n\ngo 1.26.1\n")
	dir := filepath.Join(root, "p")
	helper := writeTestFile(t, root, "p/helper_test.go", "package p\nfunc _gsxrenderChild() {}\n")
	saved, err := Build(BuildOptions{ModuleRoot: root, ModulePath: "example.com/app"})
	if err != nil {
		t.Fatal(err)
	}
	if _, active := saved.packageDirs["example.com/app/p"]; active {
		t.Fatal("Go-only directory became an active GSX package")
	}
	if _, overlaid := saved.Overlay()[helper]; overlaid {
		t.Fatal("helper snapshot became a compilation overlay")
	}
	if err := os.WriteFile(helper, []byte("package p\nfunc diskChanged() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	page := filepath.Join(dir, "page.gsx")
	effective, err := saved.WithOverrides(map[string][]byte{
		page: []byte("package p\ncomponent Child() { <span/> }\n"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := effective.packageDirs["example.com/app/p"]; got != dir {
		t.Fatalf("first GSX override package dir = %q, want %q", got, dir)
	}
	if source, ok := effective.HelperGoFiles(dir)[helper].Source(); !ok || string(source) != "package p\nfunc _gsxrenderChild() {}\n" {
		t.Fatalf("frozen Go-only helper source = %q, %v", source, ok)
	}

	mainModule := &ModuleMetadata{Path: "example.com/app", Dir: root, Main: true, GoMod: goMod}
	graph := Graph{
		"example.com/app/p": {
			ImportPath:      "example.com/app/p",
			Dir:             dir,
			CompiledGoFiles: manifestSentinelsForDir(effective, dir),
			Module:          mainModule,
		},
	}
	projection, err := NewCacheProjection(effective, graph)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := projection.Digest(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	logical, err := projection.LogicalFiles(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	wantLogical := []string{goMod, helper, page}
	sort.Strings(wantLogical)
	if !reflect.DeepEqual(logical, wantLogical) {
		t.Fatalf("LogicalFiles() = %v, want %v", logical, wantLogical)
	}

	changed, err := effective.WithOverrides(map[string][]byte{
		helper: []byte("package p\nfunc _gsxrenderChild1() {}\n"),
	})
	if err != nil {
		t.Fatal(err)
	}
	changedProjection, err := NewCacheProjection(changed, graph)
	if err != nil {
		t.Fatal(err)
	}
	changedDigest, err := changedProjection.Digest(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if changedDigest == digest {
		t.Fatal("Go-only helper override did not change cache identity after GSX membership")
	}
}

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
