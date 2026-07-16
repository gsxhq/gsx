package gen

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"github.com/gsxhq/gsx/internal/codegen"
)

func unsetLSPTestEnvironment(t *testing.T, key string) {
	t.Helper()
	value, existed := os.LookupEnv(key)
	if err := os.Unsetenv(key); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if existed {
			_ = os.Setenv(key, value)
		} else {
			_ = os.Unsetenv(key)
		}
	})
}

func lspHasModuleForRoot(a lspAnalyzer, root string) bool {
	a.mods.mu.Lock()
	defer a.mods.mu.Unlock()
	_, ok := a.mods.byRoot[filepath.Clean(root)]
	return ok
}

func TestLSPRefreshDiskTracksClosedGSXChangeCreateAndDelete(t *testing.T) {
	if testing.Short() {
		t.Skip("real module analysis")
	}
	root := t.TempDir()
	repoRoot, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	writeLSPRenameModule(t, root, repoRoot, map[string]string{
		"views/card.gsx": "package views\ncomponent Card(value string) { <p>{value}</p> }\n",
		"views/page.gsx": "package views\ncomponent Page() { <Card value=\"one\"/> }\n",
	})
	viewsDir := filepath.Join(root, "views")
	cardPath := filepath.Join(viewsDir, "card.gsx")
	pagePath := filepath.Join(viewsDir, "page.gsx")
	cardSource, err := os.ReadFile(cardPath)
	if err != nil {
		t.Fatal(err)
	}
	a := newLSPAnalyzer(config{}, nil)
	if _, err := a.SetOverride(cardPath, cardSource); err != nil {
		t.Fatal(err)
	}
	refCount := func() int {
		facts, err := a.AnalyzeModuleParams(viewsDir, nil)
		if err != nil {
			t.Fatal(err)
		}
		if len(facts) != 1 || facts[0].Name != "value" {
			t.Fatalf("facts = %+v, want Card.value", facts)
		}
		return len(facts[0].Refs)
	}
	initial := refCount()

	if err := os.WriteFile(pagePath, []byte("package views\ncomponent Page() { <Card value=\"one\"/> <Card value=\"two\"/> }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := a.RefreshDisk([]string{pagePath}); err != nil {
		t.Fatal(err)
	}
	if got := refCount(); got != initial+1 {
		t.Fatalf("refs after closed-file change = %d, want %d", got, initial+1)
	}

	createdPath := filepath.Join(viewsDir, "more.gsx")
	if err := os.WriteFile(createdPath, []byte("package views\ncomponent More() { <Card value=\"three\"/> }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := a.RefreshDisk([]string{createdPath}); err != nil {
		t.Fatal(err)
	}
	if got := refCount(); got != initial+2 {
		t.Fatalf("refs after closed-file create = %d, want %d", got, initial+2)
	}
	if err := os.Remove(createdPath); err != nil {
		t.Fatal(err)
	}
	if _, err := a.RefreshDisk([]string{createdPath}); err != nil {
		t.Fatal(err)
	}
	if got := refCount(); got != initial+1 {
		t.Fatalf("refs after closed-file delete = %d, want %d", got, initial+1)
	}
}

func TestLSPRefreshDiskRebuildsGoModUniverseAndReplaysOpenBuffers(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/app\n\ngo 1.26.1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "views")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "page.gsx")
	source := []byte("package views\ncomponent Page() { <p/> }\n")
	if err := os.WriteFile(path, source, 0o644); err != nil {
		t.Fatal(err)
	}
	a := newLSPAnalyzer(config{}, nil)
	if _, err := a.SetOverride(path, source); err != nil {
		t.Fatal(err)
	}
	oldModule := lspOverrideModule(a, path)
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/app\n\ngo 1.26.1\n\n// dependency universe changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	affected, err := a.RefreshDisk([]string{filepath.Join(root, "go.mod")})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(affected, []string{dir}) {
		t.Fatalf("affected = %v, want open package %s", affected, dir)
	}
	if lspOverrideModule(a, path) == oldModule {
		t.Fatal("go.mod event reused the old analysis universe")
	}
}

func TestLSPRefreshDiskConfigEventUsesExactOpenConfigProvenance(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/app\n\ngo 1.26.1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"first", "second"} {
		dir := filepath.Join(root, name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name+".gsx"), []byte("package "+name+"\ncomponent Page() { <p/> }\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	firstPath := filepath.Join(root, "first", "first.gsx")
	secondPath := filepath.Join(root, "second", "second.gsx")
	rootConfig := filepath.Join(root, "gsx.toml")
	if err := os.WriteFile(rootConfig, []byte("[[url_attrs]]\nname = \"href\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	a := newLSPAnalyzer(config{}, nil)
	for _, path := range []string{firstPath, secondPath} {
		source, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := a.SetOverride(path, source); err != nil {
			t.Fatal(err)
		}
	}
	firstConfig := filepath.Join(root, "first", "gsx.toml")
	if err := os.WriteFile(firstConfig, []byte("[[url_attrs]]\nname = \"hx-get\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	affected, err := a.RefreshDisk([]string{firstConfig})
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(affected)
	if !reflect.DeepEqual(affected, []string{filepath.Join(root, "first"), filepath.Join(root, "second")}) {
		t.Fatalf("affected = %v, want both open packages transferred by the one per-root semantic Module", affected)
	}
	createdModule := lspOverrideModule(a, firstPath)
	if err := os.Remove(firstConfig); err != nil {
		t.Fatal(err)
	}
	affected, err = a.RefreshDisk([]string{firstConfig})
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(affected)
	if !reflect.DeepEqual(affected, []string{filepath.Join(root, "first"), filepath.Join(root, "second")}) {
		t.Fatalf("affected after nearer config deletion = %v, want both open packages transferred to root config", affected)
	}
	if lspOverrideModule(a, firstPath) == createdModule {
		t.Fatal("deleted nearer config retained its semantic Module")
	}
}

func TestLSPRefreshDiskReopenCannotReuseRetainedStaleUniverse(t *testing.T) {
	root := t.TempDir()
	repoRoot, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	goMod := "module example.com/app\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => " + repoRoot + "\n"
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte(goMod), 0o644); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "views")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "page.gsx"), []byte("package views\ncomponent Page() { <p/> }\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Run("go.mod event evicts root without open override", func(t *testing.T) {
		a := newLSPAnalyzer(config{}, nil)
		if _, err := a.Analyze(dir, nil); err != nil {
			t.Fatal(err)
		}
		oldModule := lspModuleForRoot(a, root)
		goModPath := filepath.Join(root, "go.mod")
		if err := os.WriteFile(goModPath, []byte(goMod+"// changed dependencies\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if affected, err := a.RefreshDisk([]string{goModPath}); err != nil || affected != nil {
			t.Fatalf("refresh affected, err = %v, %v; want no open package, nil", affected, err)
		}
		if lspHasModuleForRoot(a, root) {
			t.Fatal("go.mod event retained closed stale Module")
		}
		if _, err := a.Analyze(dir, nil); err != nil {
			t.Fatal(err)
		}
		if lspModuleForRoot(a, root) == oldModule {
			t.Fatal("reopen reused pre-go.mod Module")
		}
	})

	t.Run("config identity is revalidated on reopen", func(t *testing.T) {
		a := newLSPAnalyzer(config{}, nil)
		if _, err := a.Analyze(dir, nil); err != nil {
			t.Fatal(err)
		}
		oldModule := lspModuleForRoot(a, root)
		configPath := filepath.Join(root, "gsx.toml")
		if err := os.WriteFile(configPath, []byte("[[url_attrs]]\nname = \"hx-get\"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if affected, err := a.RefreshDisk([]string{configPath}); err != nil || affected != nil {
			t.Fatalf("refresh affected, err = %v, %v; want no open package, nil", affected, err)
		}
		if _, err := a.Analyze(dir, nil); err != nil {
			t.Fatal(err)
		}
		if lspModuleForRoot(a, root) == oldModule {
			t.Fatal("reopen reused Module with stale semantic config identity")
		}
	})
}

func TestLSPRefreshDiskUsesExactFrozenGoWorkspaceIdentity(t *testing.T) {
	writeModule := func(t *testing.T, root string) (string, []byte) {
		t.Helper()
		if err := os.MkdirAll(filepath.Join(root, "views"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/app\n\ngo 1.26.1\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(root, "views", "page.gsx")
		source := []byte("package views\ncomponent Page() { <p/> }\n")
		if err := os.WriteFile(path, source, 0o644); err != nil {
			t.Fatal(err)
		}
		return path, source
	}

	t.Run("persisted GOENV GOWORK", func(t *testing.T) {
		unsetLSPTestEnvironment(t, "GOWORK")
		workspaceRoot := t.TempDir()
		moduleRoot := filepath.Join(workspaceRoot, "app")
		path, source := writeModule(t, moduleRoot)
		goWorkPath := filepath.Join(workspaceRoot, "go.work")
		if err := os.WriteFile(goWorkPath, []byte("go 1.26.1\nuse ./app\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		goEnvPath := filepath.Join(t.TempDir(), "goenv")
		if err := os.WriteFile(goEnvPath, []byte("GOWORK="+goWorkPath+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Setenv("GOENV", goEnvPath)
		a := newLSPAnalyzer(config{}, nil)
		if _, err := a.SetOverride(path, source); err != nil {
			t.Fatal(err)
		}
		oldModule := lspOverrideModule(a, path)
		if err := os.WriteFile(goWorkPath, []byte("go 1.26.1\nuse ./app\n// changed\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		affected, err := a.RefreshDisk([]string{goWorkPath})
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(affected, []string{filepath.Dir(path)}) {
			t.Fatalf("affected = %v, want %s", affected, filepath.Dir(path))
		}
		if lspOverrideModule(a, path) == oldModule {
			t.Fatal("persisted GOWORK event reused old Module")
		}
	})

	t.Run("workspace deletion switches to parent", func(t *testing.T) {
		unsetLSPTestEnvironment(t, "GOWORK")
		t.Setenv("GOENV", "off")
		workspaceRoot := t.TempDir()
		nestedRoot := filepath.Join(workspaceRoot, "nested")
		moduleRoot := filepath.Join(nestedRoot, "app")
		path, source := writeModule(t, moduleRoot)
		parentWork := filepath.Join(workspaceRoot, "go.work")
		nearWork := filepath.Join(nestedRoot, "go.work")
		if err := os.WriteFile(parentWork, []byte("go 1.26.1\nuse ./nested/app\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(nearWork, []byte("go 1.26.1\nuse ./app\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		a := newLSPAnalyzer(config{}, nil)
		if _, err := a.SetOverride(path, source); err != nil {
			t.Fatal(err)
		}
		oldModule := lspOverrideModule(a, path)
		if got, err := oldModule.(*codegen.Module).GoWorkFile(); err != nil || canonicalWatchedPath(got) != canonicalWatchedPath(nearWork) {
			t.Fatalf("initial workspace = %q, %v; want %q", got, err, nearWork)
		}
		if err := os.Remove(nearWork); err != nil {
			t.Fatal(err)
		}
		affected, err := a.RefreshDisk([]string{nearWork})
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(affected, []string{filepath.Dir(path)}) {
			t.Fatalf("affected = %v, want %s", affected, filepath.Dir(path))
		}
		if lspOverrideModule(a, path) == oldModule {
			t.Fatal("workspace switch reused old Module")
		}
		module := lspOverrideModule(a, path).(*codegen.Module)
		if got, err := module.GoWorkFile(); err != nil || canonicalWatchedPath(got) != canonicalWatchedPath(parentWork) {
			t.Fatalf("replacement workspace = %q, %v; want %q", got, err, parentWork)
		}
	})
}
