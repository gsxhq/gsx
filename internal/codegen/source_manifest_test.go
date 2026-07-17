package codegen

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"testing"

	"github.com/gsxhq/gsx/internal/sourceview"
)

func TestBuildSourceInventoryManifestReusesProvidedSnapshotUntilOverride(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/app\n\ngo 1.26.1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "ui")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "card.gsx")
	if err := os.WriteFile(path, []byte("package ui\ncomponent Card() { <p/> }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	manifest, err := sourceview.Build(sourceview.BuildOptions{ModuleRoot: root, ModulePath: "example.com/app"})
	if err != nil {
		t.Fatal(err)
	}
	module, err := Open(Options{ModuleRoot: root, ModulePath: "example.com/app", SourceManifest: manifest})
	if err != nil {
		t.Fatal(err)
	}
	got, err := module.buildSourceInventoryManifest()
	if err != nil {
		t.Fatal(err)
	}
	if got != manifest {
		t.Fatal("cold source selection rebuilt instead of reusing the cache query's exact Manifest object")
	}

	module.SetOverride(path, []byte("package ui\ncomponent Card() { <strong/> }\n"))
	withOverride, err := module.buildSourceInventoryManifest()
	if err != nil {
		t.Fatal(err)
	}
	if withOverride == manifest {
		t.Fatal("override-bearing source selection reused the disk-only Manifest object")
	}
	if source, ok := withOverride.Source(path); !ok || string(source) != "package ui\ncomponent Card() { <strong/> }\n" {
		t.Fatalf("override manifest source = %q, %v", source, ok)
	}
}

func TestProvidedSourceManifestFreezesAnalysisAndCacheUntilRefresh(t *testing.T) {
	root := t.TempDir()
	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/app\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "page")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	oldPath := filepath.Join(dir, "old.gsx")
	newPath := filepath.Join(dir, "new.gsx")
	if err := os.WriteFile(oldPath, []byte("package page\ncomponent Old() { <p>snapshot-old</p> }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	manifest, err := sourceview.Build(sourceview.BuildOptions{ModuleRoot: root, ModulePath: "example.com/app"})
	if err != nil {
		t.Fatal(err)
	}
	cacheIdentity := func(snapshot *sourceview.Manifest) (string, []string) {
		t.Helper()
		graph := sourceview.Graph{
			"example.com/app/page": {
				ImportPath:      "example.com/app/page",
				Dir:             dir,
				CompiledGoFiles: snapshot.SentinelFiles(),
				Module: &sourceview.ModuleMetadata{
					Path:  "example.com/app",
					Dir:   root,
					GoMod: filepath.Join(root, "go.mod"),
					Main:  true,
				},
			},
		}
		projection, err := sourceview.NewCacheProjection(snapshot, graph)
		if err != nil {
			t.Fatal(err)
		}
		digest, err := projection.Digest(dir, nil)
		if err != nil {
			t.Fatal(err)
		}
		files, err := projection.LogicalFiles(dir, nil)
		if err != nil {
			t.Fatal(err)
		}
		return digest, files
	}
	oldDigest, oldFiles := cacheIdentity(manifest)

	module, err := Open(Options{ModuleRoot: root, ModulePath: "example.com/app", SourceManifest: manifest})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(oldPath); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(newPath, []byte("package page\ncomponent New() { <strong>disk-new</strong> }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	output, diagnostics, err := module.Generate(dir)
	if err != nil || len(diagnostics) != 0 {
		t.Fatalf("Generate from frozen snapshot error=%v diagnostics=%v", err, diagnostics)
	}
	if !bytes.Contains(output[oldPath], []byte("snapshot-old")) || len(output[newPath]) != 0 {
		t.Fatalf("pre-refresh output observed live disk instead of snapshot: keys=%v old=%s", outputPaths(output), output[oldPath])
	}
	if !containsPath(oldFiles, oldPath) || containsPath(oldFiles, newPath) {
		t.Fatalf("pre-refresh cache files = %v, want old only", oldFiles)
	}

	if err := module.RefreshDiskSources(dir); err != nil {
		t.Fatal(err)
	}
	module.Invalidate(dir)
	output, diagnostics, err = module.Generate(dir)
	if err != nil || len(diagnostics) != 0 {
		t.Fatalf("Generate after refresh error=%v diagnostics=%v", err, diagnostics)
	}
	if !bytes.Contains(output[newPath], []byte("disk-new")) || len(output[oldPath]) != 0 {
		t.Fatalf("post-refresh output did not transition atomically: keys=%v new=%s", outputPaths(output), output[newPath])
	}
	module.mu.Lock()
	refreshed := module.savedSourceManifest
	module.mu.Unlock()
	newDigest, newFiles := cacheIdentity(refreshed)
	if newDigest == oldDigest {
		t.Fatal("explicit refresh did not change cache source identity")
	}
	if !containsPath(newFiles, newPath) || containsPath(newFiles, oldPath) {
		t.Fatalf("post-refresh cache files = %v, want new only", newFiles)
	}
}

func containsPath(paths []string, want string) bool {
	return slices.Contains(paths, want)
}

func outputPaths(output map[string][]byte) []string {
	paths := make([]string, 0, len(output))
	for path := range output {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths
}
