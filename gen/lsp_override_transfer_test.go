package gen

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestLSPSetOverrideTransfersOwnershipAndUnionsAffectedRoots(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping module-resolution test in short mode")
	}
	root, nested, pageDir, sourcePath := setupLSPOverrideTransferModule(t)
	a := newLSPAnalyzer(config{}, nil)
	first := []byte("package nested\n\ncomponent Widget() { <strong>buffer one</strong> }\n")
	if _, err := a.SetOverride(sourcePath, first); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Analyze(pageDir, nil); err != nil {
		t.Fatal(err)
	}

	writeLSPTransferGoMod(t, nested, "example.com/nested")
	got, err := a.SetOverride(sourcePath, []byte("package nested\n\ncomponent Widget() { <strong>buffer two</strong> }\n"))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{nested, pageDir}
	slices.Sort(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("cross-root SetOverride affected = %v, want old/new union %v", got, want)
	}
	if owner := lspOverrideOwner(a, sourcePath); owner != nested {
		t.Fatalf("cross-root SetOverride owner = %q, want %q", owner, nested)
	}
	if old := lspModuleForRoot(a, root); old == nil {
		t.Fatal("old module was not retained for cache reuse")
	} else if affected, err := old.ClearOverride(sourcePath); affected != nil || err != nil {
		t.Fatalf("old module retained override after transfer: ClearOverride = %v, %v", affected, err)
	}
}

func TestLSPSetOverrideSameRootPreservesExactNoOpTransition(t *testing.T) {
	root := t.TempDir()
	writeLSPTransferGoMod(t, root, "example.com/app")
	dir := filepath.Join(root, "page")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "page.gsx")
	if err := os.WriteFile(path, []byte("package page\n\ncomponent Page() { <p>disk</p> }\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	a := newLSPAnalyzer(config{}, nil)
	buffer := []byte("package page\n\ncomponent Page() { <p>buffer</p> }\n")
	if got, err := a.SetOverride(path, buffer); err != nil || !reflect.DeepEqual(got, []string{dir}) {
		t.Fatalf("first SetOverride = %v, %v; want [%s], nil", got, err, dir)
	}
	if got, err := a.SetOverride(path, buffer); err != nil || got != nil {
		t.Fatalf("same-byte same-root SetOverride = %v, %v; want nil, nil", got, err)
	}
	if owner := lspOverrideOwner(a, path); owner != root {
		t.Fatalf("same-root SetOverride owner = %q, want %q", owner, root)
	}
}

func TestLSPSetOverrideFailedRootDiscoveryClearsPreviousAuthority(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping module-resolution test in short mode")
	}
	root, nested, pageDir, sourcePath := setupLSPOverrideTransferModule(t)
	a := newLSPAnalyzer(config{}, nil)
	if _, err := a.SetOverride(sourcePath, []byte("package nested\n\ncomponent Widget() { <b>buffer</b> }\n")); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Analyze(pageDir, nil); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(nested, "go.mod"), []byte("not a go.mod\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := a.SetOverride(sourcePath, []byte("package nested\n\ncomponent Widget() { <i>new buffer</i> }\n"))
	if err == nil || !strings.Contains(err.Error(), "parse") {
		t.Fatalf("SetOverride error = %v, want nearest malformed go.mod error", err)
	}
	want := []string{nested, pageDir}
	slices.Sort(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("failed-root SetOverride affected = %v, want old closure %v", got, want)
	}
	if owner := lspOverrideOwner(a, sourcePath); owner != "" {
		t.Fatalf("failed-root SetOverride retained owner %q", owner)
	}
	if old := lspModuleForRoot(a, root); old == nil {
		t.Fatal("old module was not retained for cache reuse")
	} else if affected, clearErr := old.ClearOverride(sourcePath); affected != nil || clearErr != nil {
		t.Fatalf("failed-root SetOverride retained old authority: ClearOverride = %v, %v", affected, clearErr)
	}
}

func TestLSPSetOverrideUnreadablePreviousClearStillAttachesNewOwner(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping module-resolution test in short mode")
	}
	root := t.TempDir()
	writeLSPTransferGoMod(t, root, "example.com/outer")
	nested := filepath.Join(root, "nested")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	sourcePath := filepath.Join(nested, "broken.gsx")
	if err := os.Mkdir(sourcePath, 0o755); err != nil {
		t.Fatal(err)
	}

	a := newLSPAnalyzer(config{}, nil)
	first := []byte("package nested\n\ncomponent Widget() { <b>buffer one</b> }\n")
	if _, err := a.SetOverride(sourcePath, first); err != nil {
		t.Fatal(err)
	}
	writeLSPTransferGoMod(t, nested, "example.com/nested")

	got, err := a.SetOverride(sourcePath, []byte("package nested\n\ncomponent Widget() { <i>buffer two</i> }\n"))
	if err == nil || !strings.Contains(err.Error(), "read saved source") {
		t.Fatalf("cross-root SetOverride error = %v, want unreadable previous clear error", err)
	}
	if !reflect.DeepEqual(got, []string{nested}) {
		t.Fatalf("cross-root SetOverride affected = %v, want [%s]", got, nested)
	}
	if owner := lspOverrideOwner(a, sourcePath); owner != nested {
		t.Fatalf("cross-root SetOverride owner = %q, want new root %q despite clear error", owner, nested)
	}
	if old := lspModuleForRoot(a, root); old == nil {
		t.Fatal("old module was not retained for cache reuse")
	} else if affected, clearErr := old.ClearOverride(sourcePath); affected != nil || clearErr != nil {
		t.Fatalf("unreadable old module retained authority: ClearOverride = %v, %v", affected, clearErr)
	}
	if result, analyzeErr := a.Analyze(nested, nil); analyzeErr != nil {
		t.Fatalf("new authoritative override did not mask unreadable saved source: %v", analyzeErr)
	} else if result == nil {
		t.Fatal("Analyze returned nil package")
	}
}

func setupLSPOverrideTransferModule(t *testing.T) (root, nested, pageDir, sourcePath string) {
	t.Helper()
	root = t.TempDir()
	writeLSPTransferGoMod(t, root, "example.com/outer")
	nested = filepath.Join(root, "nested")
	pageDir = filepath.Join(root, "page")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(pageDir, 0o755); err != nil {
		t.Fatal(err)
	}
	sourcePath = filepath.Join(nested, "widget.gsx")
	if err := os.WriteFile(sourcePath, []byte("package nested\n\ncomponent Widget() { <span>disk</span> }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	pageSource := "package page\n\nimport \"example.com/outer/nested\"\n\ncomponent Page() { <nested.Widget/> }\n"
	if err := os.WriteFile(filepath.Join(pageDir, "page.gsx"), []byte(pageSource), 0o644); err != nil {
		t.Fatal(err)
	}
	return root, nested, pageDir, sourcePath
}

func writeLSPTransferGoMod(t *testing.T, dir, modulePath string) {
	t.Helper()
	repoRoot, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	contents := "module " + modulePath + "\n\ngo 1.26.1\n\n" +
		"require github.com/gsxhq/gsx v0.0.0\n\n" +
		"replace github.com/gsxhq/gsx => " + repoRoot + "\n"
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}

func lspOverrideOwner(a lspAnalyzer, path string) string {
	a.mods.mu.Lock()
	defer a.mods.mu.Unlock()
	return a.mods.overrideRoots[filepath.Clean(path)]
}

func lspModuleForRoot(a lspAnalyzer, root string) interface {
	ClearOverride(string) ([]string, error)
} {
	a.mods.mu.Lock()
	defer a.mods.mu.Unlock()
	return a.mods.byRoot[filepath.Clean(root)]
}
