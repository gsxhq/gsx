package codegen

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestClearOverrideRestoresSavedSourceInWarmModule(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping module-resolution test in short mode")
	}
	root, pageDir, pagePath := writeOverrideLifecycleModule(t)
	m, err := Open(Options{ModuleRoot: root, ModulePath: "example.com/app"})
	if err != nil {
		t.Fatal(err)
	}

	assertOverrideLifecycleComponents(t, m, pageDir, ".Disk")
	loads := m.externalLoads()

	m.SetOverride(pagePath, []byte("package page\n\ncomponent Buffer() { <div>buffer</div> }\n"))
	assertOverrideLifecycleComponents(t, m, pageDir, ".Buffer")
	if got := m.externalLoads(); got != loads {
		t.Fatalf("body-only override external loads = %d, want warm count %d", got, loads)
	}

	if _, err := m.ClearOverride(pagePath); err != nil {
		t.Fatal(err)
	}
	if got := m.dirtyDirs(); len(got) != 1 || got[0] != pageDir {
		t.Fatalf("dirty dirs after clear = %v, want [%s]", got, pageDir)
	}
	if source, ok := m.source(pagePath); !ok || string(source) != "package page\n\ncomponent Disk() { <div>disk</div> }\n" {
		t.Fatalf("source after clear = %q, %v; want saved source", source, ok)
	}
	assertOverrideLifecycleComponents(t, m, pageDir, ".Disk")
	if got := m.externalLoads(); got != loads {
		t.Fatalf("clear-to-saved external loads = %d, want warm count %d", got, loads)
	}
}

func TestConcurrentSetClearAndPackageLeavesFinalClearAuthoritative(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping module-resolution test in short mode")
	}
	root, pageDir, pagePath := writeOverrideLifecycleModule(t)
	m, err := Open(Options{ModuleRoot: root, ModulePath: "example.com/app"})
	if err != nil {
		t.Fatal(err)
	}
	assertOverrideLifecycleComponents(t, m, pageDir, ".Disk")

	errC := make(chan error, 100)
	var wg sync.WaitGroup
	wg.Add(3)
	go func() {
		defer wg.Done()
		for index := range 40 {
			m.SetOverride(pagePath, fmt.Appendf(nil, "package page\n\ncomponent Buffer() { <div>%d</div> }\n", index))
		}
	}()
	go func() {
		defer wg.Done()
		for range 40 {
			if _, err := m.ClearOverride(pagePath); err != nil {
				errC <- err
			}
		}
	}()
	go func() {
		defer wg.Done()
		for range 20 {
			if _, err := m.Package(pageDir); err != nil {
				errC <- err
			}
		}
	}()
	wg.Wait()
	close(errC)
	for err := range errC {
		t.Error(err)
	}
	if t.Failed() {
		return
	}

	if _, err := m.ClearOverride(pagePath); err != nil {
		t.Fatal(err)
	}
	assertOverrideLifecycleComponents(t, m, pageDir, ".Disk")
}

func TestClearOverrideRemovesUnsavedNewSourceFromWarmModule(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping module-resolution test in short mode")
	}
	root, pageDir, _ := writeOverrideLifecycleModule(t)
	m, err := Open(Options{ModuleRoot: root, ModulePath: "example.com/app"})
	if err != nil {
		t.Fatal(err)
	}
	assertOverrideLifecycleComponents(t, m, pageDir, ".Disk")

	newPath := filepath.Join(pageDir, "new.gsx")
	m.SetOverride(newPath, []byte("package page\n\ncomponent New() { <div>new</div> }\n"))
	assertOverrideLifecycleComponents(t, m, pageDir, ".Disk", ".New")
	loadsWithNewFile := m.externalLoads()

	if _, err := m.ClearOverride(newPath); err != nil {
		t.Fatal(err)
	}
	if _, ok := m.source(newPath); ok {
		t.Fatal("cleared unsaved-new source remains authoritative")
	}
	if got := m.dirtyDirs(); len(got) != 1 || got[0] != pageDir {
		t.Fatalf("dirty dirs after clearing unsaved-new source = %v, want [%s]", got, pageDir)
	}
	assertOverrideLifecycleComponents(t, m, pageDir, ".Disk")
	if got := m.externalLoads(); got != loadsWithNewFile+1 {
		t.Fatalf("clear of published unsaved-new membership external loads = %d, want %d", got, loadsWithNewFile+1)
	}
	m.mu.Lock()
	_, factRetained := m.sourceInventoryFacts[newPath]
	m.mu.Unlock()
	if factRetained {
		t.Fatal("cleared unsaved-new source retained an inventory fact")
	}
}

func TestClearOverrideCancelsOnlyItsOwnUnpublishedManifestChange(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping module-resolution test in short mode")
	}
	root, pageDir, pagePath := writeOverrideLifecycleModule(t)
	m, err := Open(Options{ModuleRoot: root, ModulePath: "example.com/app"})
	if err != nil {
		t.Fatal(err)
	}
	assertOverrideLifecycleComponents(t, m, pageDir, ".Disk")
	loads := m.externalLoads()

	transientPath := filepath.Join(pageDir, "transient.gsx")
	m.SetOverride(transientPath, []byte("package page\n\ncomponent Transient() { <div/> }\n"))
	if _, err := m.ClearOverride(transientPath); err != nil {
		t.Fatal(err)
	}
	assertOverrideLifecycleComponents(t, m, pageDir, ".Disk")
	if got := m.externalLoads(); got != loads {
		t.Fatalf("set+clear before analysis external loads = %d, want unchanged %d", got, loads)
	}

	otherPath := filepath.Join(pageDir, "other.gsx")
	m.SetOverride(otherPath, []byte("package page\n\ncomponent Other() { <div/> }\n"))
	m.SetOverride(pagePath, []byte("package changed\n\ncomponent Changed() { <div/> }\n"))
	if _, err := m.ClearOverride(pagePath); err != nil {
		t.Fatal(err)
	}
	assertOverrideLifecycleComponents(t, m, pageDir, ".Disk", ".Other")
	if got := m.externalLoads(); got != loads+1 {
		t.Fatalf("clearing one path erased another path's reload reason: loads = %d, want %d", got, loads+1)
	}
}

func writeOverrideLifecycleModule(t *testing.T) (root, pageDir, pagePath string) {
	t.Helper()
	root = t.TempDir()
	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/app\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	pageDir = filepath.Join(root, "page")
	if err := os.MkdirAll(pageDir, 0o755); err != nil {
		t.Fatal(err)
	}
	pagePath = filepath.Join(pageDir, "page.gsx")
	if err := os.WriteFile(pagePath, []byte("package page\n\ncomponent Disk() { <div>disk</div> }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return root, pageDir, pagePath
}

func assertOverrideLifecycleComponents(t *testing.T, m *Module, dir string, want ...string) {
	t.Helper()
	pkg, err := m.Package(dir)
	if err != nil {
		t.Fatal(err)
	}
	wantSet := make(map[string]bool, len(want))
	for _, key := range want {
		wantSet[key] = true
	}
	if len(pkg.CrossIndex) != len(wantSet) {
		t.Fatalf("components = %v, want exactly %v", pkg.CrossIndex, want)
	}
	for key := range wantSet {
		if _, ok := pkg.CrossIndex[key]; !ok {
			t.Fatalf("components = %v, missing %s", pkg.CrossIndex, key)
		}
	}
}
