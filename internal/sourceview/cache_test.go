package sourceview

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func TestCacheProjectionCgoClassification(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "go.mod", "module example.com/app\n\ngo 1.26.1\n")
	manifest, err := Build(BuildOptions{ModuleRoot: root, ModulePath: "example.com/app"})
	if err != nil {
		t.Fatal(err)
	}
	projection := &CacheProjection{manifest: manifest}

	for _, test := range []struct {
		name     string
		metadata PackageMetadata
		wantErr  bool
	}{
		{
			name: "standard library cgo",
			metadata: PackageMetadata{
				ImportPath: "runtime/cgo",
				CgoFiles:   []string{"cgo.go"},
				Standard:   true,
			},
		},
		{
			name: "versioned external module cgo",
			metadata: PackageMetadata{
				ImportPath: "example.com/immutable/cgo",
				CgoFiles:   []string{"cgo.go"},
				Module: &ModuleMetadata{
					Path:    "example.com/immutable",
					Version: "v1.2.3",
				},
			},
		},
		{
			name: "main module cgo",
			metadata: PackageMetadata{
				ImportPath: "example.com/app/cgo",
				CgoFiles:   []string{"cgo.go"},
				Module: &ModuleMetadata{
					Path: "example.com/app",
					Dir:  root,
					Main: true,
				},
			},
			wantErr: true,
		},
		{
			name: "local replacement cgo",
			metadata: PackageMetadata{
				ImportPath: "example.com/local/cgo",
				CgoFiles:   []string{"cgo.go"},
				Module: &ModuleMetadata{
					Path:    "example.com/local",
					Version: "v0.0.0",
					Replace: &ModuleMetadata{
						Path: "example.com/local",
						Dir:  filepath.Join(root, "replacement"),
					},
				},
			},
			wantErr: true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			inputs, err := projection.inputsForPackage(test.metadata)
			if test.wantErr {
				if !errors.Is(err, ErrUncacheableCgo) {
					t.Fatalf("inputsForPackage() error = %v, want ErrUncacheableCgo", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("inputsForPackage() error = %v, want nil", err)
			}
			if len(inputs) != 0 {
				t.Fatalf("inputsForPackage() inputs = %v, want none", inputs)
			}
		})
	}
}

func TestCacheProjectionUsesManifestEdgesAndActiveGoSelection(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "go.mod", "module example.com/app\n\ngo 1.26.1\n")
	pageGSX := writeTestFile(t, root, "pages/page.gsx", "package pages\nimport \"example.com/app/ui\"\ncomponent Page() { <ui.Card/> }\n")
	cardGSX := writeTestFile(t, root, "ui/card.gsx", "package ui\nimport \"example.com/app/model\"\ncomponent Card() { <p/> }\n")
	paired := writeTestFile(t, root, "ui/card.x.go", "package stale\nvar Poison = true\n")
	active := writeTestFile(t, root, "ui/authored.x.go", "package ui\ntype Active string\n")
	inactive := writeTestFile(t, root, "ui/inactive.go", "//go:build inactive\n\npackage ui\ntype Inactive string\n")
	model := writeTestFile(t, root, "model/model.go", "package model\ntype Value string\n")

	build := func() (*Manifest, *CacheProjection) {
		t.Helper()
		manifest, err := Build(BuildOptions{ModuleRoot: root, ModulePath: "example.com/app"})
		if err != nil {
			t.Fatal(err)
		}
		mainModule := &ModuleMetadata{Path: "example.com/app", Dir: root, Main: true, GoMod: filepath.Join(root, "go.mod")}
		graph := Graph{
			"example.com/app/pages": {
				ImportPath:      "example.com/app/pages",
				Dir:             filepath.Join(root, "pages"),
				Imports:         []string{"example.com/app/ui"},
				CompiledGoFiles: append(manifestSentinelsForDir(manifest, filepath.Join(root, "pages")), pagePairedIfAny(manifest, pageGSX)...),
				Module:          mainModule,
			},
			"example.com/app/ui": {
				ImportPath:      "example.com/app/ui",
				Dir:             filepath.Join(root, "ui"),
				CompiledGoFiles: append(append(manifestSentinelsForDir(manifest, filepath.Join(root, "ui")), paired), active),
				Module:          mainModule,
			},
			"example.com/app/model": {
				ImportPath:      "example.com/app/model",
				Dir:             filepath.Join(root, "model"),
				CompiledGoFiles: []string{model},
				Module:          mainModule,
			},
		}
		projection, err := NewCacheProjection(manifest, graph)
		if err != nil {
			t.Fatal(err)
		}
		return manifest, projection
	}

	_, first := build()
	digestBefore, err := first.Digest(filepath.Join(root, "pages"), nil)
	if err != nil {
		t.Fatal(err)
	}
	logical, err := first.LogicalFiles(filepath.Join(root, "pages"), nil)
	if err != nil {
		t.Fatal(err)
	}
	wantLogical := []string{pageGSX, cardGSX, active, model, filepath.Join(root, "go.mod")}
	sort.Strings(wantLogical)
	if !reflect.DeepEqual(logical, wantLogical) {
		t.Fatalf("LogicalFiles() = %v, want %v", logical, wantLogical)
	}

	writeTestFile(t, root, "ui/card.x.go", "package different_poison\nfunc (\n")
	_, afterPoison := build()
	poisonDigest, err := afterPoison.Digest(filepath.Join(root, "pages"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if poisonDigest != digestBefore {
		t.Fatal("still-paired generated output changed cache identity")
	}

	writeTestFile(t, root, "ui/inactive.go", "//go:build inactive\n\npackage ui\ntype Inactive int\n")
	_, afterInactive := build()
	inactiveDigest, err := afterInactive.Digest(filepath.Join(root, "pages"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if inactiveDigest != digestBefore {
		t.Fatal("build-excluded companion changed cache identity")
	}

	writeTestFile(t, root, "ui/authored.x.go", "package ui\ntype Active int\n")
	_, afterActive := build()
	activeDigest, err := afterActive.Digest(filepath.Join(root, "pages"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if activeDigest == digestBefore {
		t.Fatal("cmd/go-selected unpaired .x.go edit did not change cache identity")
	}

	_ = inactive
}

func TestCacheProjectionHashesReachableVersionlessReplacementProvenance(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "app")
	replacement := filepath.Join(parent, "replacement")
	writeTestFile(t, root, "go.mod", "module example.com/app\n\ngo 1.26.1\n\nrequire example.com/dep v0.0.0\nreplace example.com/dep => ../replacement\n")
	page := writeTestFile(t, root, "page/page.gsx", "package page\nimport \"example.com/dep\"\ncomponent Page() { <p>{ dep.Value }</p> }\n")
	depMod := writeTestFile(t, replacement, "go.mod", "module example.com/dep\n\ngo 1.26.1\n")
	depSum := writeTestFile(t, replacement, "go.sum", "example.com/transitive v1.0.0 h1:first\n")
	depSource := writeTestFile(t, replacement, "dep.go", "package dep\nconst Value = \"first\"\n")
	unselected := writeTestFile(t, replacement, "inactive.go", "//go:build inactive\n\npackage dep\nconst Inactive = 1\n")

	digest := func() string {
		t.Helper()
		manifest, err := Build(BuildOptions{ModuleRoot: root, ModulePath: "example.com/app"})
		if err != nil {
			t.Fatal(err)
		}
		mainModule := &ModuleMetadata{Path: "example.com/app", Dir: root, Main: true, GoMod: filepath.Join(root, "go.mod")}
		replacementModule := &ModuleMetadata{Path: "example.com/dep", Dir: replacement, GoMod: depMod}
		graph := Graph{
			"example.com/app/page": {
				ImportPath:      "example.com/app/page",
				Dir:             filepath.Join(root, "page"),
				Imports:         []string{"example.com/dep"},
				CompiledGoFiles: manifestSentinelsForDir(manifest, filepath.Join(root, "page")),
				Module:          mainModule,
			},
			"example.com/dep": {
				ImportPath:      "example.com/dep",
				Dir:             replacement,
				CompiledGoFiles: []string{depSource},
				Module: &ModuleMetadata{
					Path:    "example.com/dep",
					Version: "v0.0.0",
					Replace: replacementModule,
				},
			},
		}
		projection, err := NewCacheProjection(manifest, graph)
		if err != nil {
			t.Fatal(err)
		}
		value, err := projection.Digest(filepath.Join(root, "page"), nil)
		if err != nil {
			t.Fatal(err)
		}
		return value
	}

	before := digest()
	writeTestFile(t, replacement, "inactive.go", "//go:build inactive\n\npackage dep\nconst Inactive = 2\n")
	if after := digest(); after != before {
		t.Fatal("unselected replacement source changed cache identity")
	}
	writeTestFile(t, replacement, "go.mod", "module example.com/dep\n\ngo 1.25\n")
	afterMod := digest()
	if afterMod == before {
		t.Fatal("reachable replacement go.mod edit did not change cache identity")
	}
	writeTestFile(t, replacement, "go.sum", "example.com/transitive v1.0.0 h1:second\n")
	afterSum := digest()
	if afterSum == afterMod {
		t.Fatal("reachable replacement go.sum edit did not change cache identity")
	}
	writeTestFile(t, replacement, "dep.go", "package dep\nconst Value = \"second\"\n")
	if after := digest(); after == afterSum {
		t.Fatal("selected replacement source edit did not change cache identity")
	}

	for _, path := range []string{page, depMod, depSum, depSource, unselected} {
		if _, err := os.Stat(path); err != nil {
			t.Fatal(err)
		}
	}
}

func TestCacheProjectionAcceptsSelectedManifestSubset(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "go.mod", "module example.com/app\n\ngo 1.26.1\n")
	writeTestFile(t, root, "ui/card.gsx", "package ui\ncomponent Card() { <p/> }\n")
	adminDir := filepath.Join(root, "admin")
	writeTestFile(t, root, "admin/admin.gsx", "package admin\ncomponent Admin() { <p/> }\n")
	manifest, err := Build(BuildOptions{ModuleRoot: root, ModulePath: "example.com/app"})
	if err != nil {
		t.Fatal(err)
	}
	graphWithUI := Graph{
		"example.com/app/ui": {
			ImportPath: "example.com/app/ui",
			Dir:        filepath.Join(root, "ui"),
			Module: &ModuleMetadata{
				Path:  "example.com/app",
				Dir:   root,
				GoMod: filepath.Join(root, "go.mod"),
				Main:  true,
			},
		},
	}
	projection, err := NewCacheProjection(manifest, graphWithUI)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := projection.Digest(adminDir, nil); err == nil {
		t.Fatal("Digest accepted a selected directory absent from the Go graph")
	}
}

func TestCacheProjectionRejectsMissingTransitiveManifestPackage(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "go.mod", "module example.com/app\n\ngo 1.26.1\n")
	pagesDir := filepath.Join(root, "pages")
	writeTestFile(t, root, "pages/page.gsx", "package pages\nimport \"example.com/app/ui\"\ncomponent Page() { <ui.Card/> }\n")
	writeTestFile(t, root, "ui/card.gsx", "package ui\ncomponent Card() { <p/> }\n")
	manifest, err := Build(BuildOptions{ModuleRoot: root, ModulePath: "example.com/app"})
	if err != nil {
		t.Fatal(err)
	}
	graphWithPagesOnly := Graph{
		"example.com/app/pages": {
			ImportPath: "example.com/app/pages",
			Dir:        pagesDir,
			Module: &ModuleMetadata{
				Path:  "example.com/app",
				Dir:   root,
				GoMod: filepath.Join(root, "go.mod"),
				Main:  true,
			},
		},
	}
	projection, err := NewCacheProjection(manifest, graphWithPagesOnly)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := projection.Digest(pagesDir, nil); err == nil || !strings.Contains(err.Error(), `reachable package "example.com/app/ui" is absent from selected Go graph`) {
		t.Fatalf("Digest() error = %v, want missing reachable ui package", err)
	}
}

func TestCacheProjectionRejectsInvalidSelectedManifestPackage(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "go.mod", "module example.com/app\n\ngo 1.26.1\n")
	uiDir := filepath.Join(root, "ui")
	writeTestFile(t, root, "ui/card.gsx", "package ui\ncomponent Card() { <p/> }\n")
	manifest, err := Build(BuildOptions{ModuleRoot: root, ModulePath: "example.com/app"})
	if err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		name     string
		dir      string
		main     bool
		wantText string
	}{
		{name: "wrong directory", dir: filepath.Join(root, "other"), main: true, wantText: "want manifest dir"},
		{name: "non-main module", dir: uiDir, main: false, wantText: "is not owned by main module"},
	} {
		t.Run(test.name, func(t *testing.T) {
			graph := Graph{
				"example.com/app/ui": {
					ImportPath: "example.com/app/ui",
					Dir:        test.dir,
					Module: &ModuleMetadata{
						Path:  "example.com/app",
						Dir:   root,
						GoMod: filepath.Join(root, "go.mod"),
						Main:  test.main,
					},
				},
			}
			if _, err := NewCacheProjection(manifest, graph); err == nil || !strings.Contains(err.Error(), test.wantText) {
				t.Fatalf("NewCacheProjection() error = %v, want text %q", err, test.wantText)
			}
		})
	}
}

func TestCacheProjectionCanonicalizesPhysicalMainModuleSelection(t *testing.T) {
	parent := t.TempDir()
	physical := filepath.Join(parent, "physical")
	logical := filepath.Join(parent, "logical")
	writeTestFile(t, physical, "go.mod", "module example.com/app\n\ngo 1.26.1\n")
	gsxPhysical := writeTestFile(t, physical, "ui/card.gsx", "package ui\ncomponent Card() { <p/> }\n")
	pairedPhysical := writeTestFile(t, physical, "ui/card.x.go", "package poison\nfunc (\n")
	activePhysical := writeTestFile(t, physical, "ui/active.go", "package ui\ntype Active string\n")
	if err := os.Symlink(physical, logical); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	manifest, err := Build(BuildOptions{ModuleRoot: logical, ModulePath: "example.com/app"})
	if err != nil {
		t.Fatal(err)
	}
	physicalRoot, err := filepath.EvalSymlinks(physical)
	if err != nil {
		t.Fatal(err)
	}
	physicalDir := filepath.Join(physicalRoot, "ui")
	var sentinelPhysical string
	for _, logicalSentinel := range manifest.SentinelFiles() {
		rel, err := filepath.Rel(manifest.ModuleRoot(), logicalSentinel)
		if err != nil {
			t.Fatal(err)
		}
		sentinelPhysical = filepath.Join(physicalRoot, rel)
	}
	graph := Graph{
		"example.com/app/ui": {
			ImportPath:      "example.com/app/ui",
			Dir:             physicalDir,
			CompiledGoFiles: []string{sentinelPhysical, pairedPhysical, activePhysical},
			Module: &ModuleMetadata{
				Path:  "example.com/app",
				Dir:   physicalRoot,
				GoMod: filepath.Join(physicalRoot, "go.mod"),
				Main:  true,
			},
		},
	}
	projection, err := NewCacheProjection(manifest, graph)
	if err != nil {
		t.Fatal(err)
	}
	logicalFiles, err := projection.LogicalFiles(filepath.Join(logical, "ui"), nil)
	if err != nil {
		t.Fatal(err)
	}
	gsxLogical := filepath.Join(logical, "ui", filepath.Base(gsxPhysical))
	activeLogical := filepath.Join(logical, "ui", filepath.Base(activePhysical))
	want := []string{filepath.Join(logical, "go.mod"), activeLogical, gsxLogical}
	sort.Strings(want)
	if !reflect.DeepEqual(logicalFiles, want) {
		t.Fatalf("LogicalFiles() = %v, want %v", logicalFiles, want)
	}
}

func manifestSentinelsForDir(manifest *Manifest, dir string) []string {
	var paths []string
	for _, path := range manifest.SentinelFiles() {
		if filepath.Dir(path) == dir {
			paths = append(paths, path)
		}
	}
	return paths
}

func pagePairedIfAny(manifest *Manifest, gsxPath string) []string {
	want := gsxPath[:len(gsxPath)-len(".gsx")] + ".x.go"
	for _, path := range manifest.PairedOutputs() {
		if path == want {
			if _, err := os.Stat(path); err == nil {
				return []string{path}
			}
		}
	}
	return nil
}
