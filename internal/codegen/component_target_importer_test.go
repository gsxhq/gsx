package codegen

import (
	"go/build"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTargetDeclarationTestModule(t *testing.T) (root, uiDir string) {
	t.Helper()
	root = t.TempDir()
	uiDir = filepath.Join(root, "ui")
	if err := os.MkdirAll(uiDir, 0o755); err != nil {
		t.Fatal(err)
	}
	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, root, "go.mod", "module example.com/app\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	writeFile(t, uiDir, "card.gsx", `package ui

component Card(title string, count int) { <div/> }
`)
	return root, uiDir
}

func TestTargetDeclarationImporterUsesAuthoritativeCompiledFilesWithGOFLAGS(t *testing.T) {
	t.Setenv("GOFLAGS", "-tags=feature")
	for _, pairedSource := range []struct {
		name   string
		source string
	}{
		{name: "wrong package", source: "package stale\nvar Poison = 1\n"},
		{name: "syntax error", source: "package ui\nfunc (\n"},
	} {
		t.Run(pairedSource.name, func(t *testing.T) {
			root, uiDir := writeTargetDeclarationTestModule(t)
			writeFile(t, uiDir, "card.gsx", "package ui\ncomponent Card(value Active) { <div/> }\n")
			writeFile(t, uiDir, "feature.go", "//go:build feature\n\npackage ui\ntype Active int\n")
			writeFile(t, uiDir, "default.go", "//go:build !feature\n\npackage ui\ntype Active string\n")
			writeFile(t, uiDir, "card.x.go", pairedSource.source)

			module, err := Open(Options{ModuleRoot: root, ModulePath: "example.com/app"})
			if err != nil {
				t.Fatal(err)
			}
			external, err := module.externalImporter()
			if err != nil {
				t.Fatal(err)
			}
			pkg, err := newComponentTargetImporter(module, external).Import("example.com/app/ui")
			if err != nil {
				t.Fatal(err)
			}
			param := exactTargetSignature(t, pkg, "Card").Params().At(0).Type()
			named, ok := param.(*types.Named)
			if !ok {
				t.Fatalf("Card parameter = %T %v, want named Active", param, param)
			}
			basic, ok := named.Underlying().(*types.Basic)
			if !ok || basic.Kind() != types.Int {
				t.Fatalf("active GOFLAGS build context selected %v, want Active with int underlying type", named.Underlying())
			}
		})
	}
}

func TestGenerateUsesAuthoritativeCompiledFilesWithGOFLAGS(t *testing.T) {
	t.Setenv("GOFLAGS", "-tags=feature")
	root, uiDir := writeTargetDeclarationTestModule(t)
	writeFile(t, uiDir, "card.gsx", "package ui\ncomponent Card(value Active) { <div>{ value }</div> }\n")
	writeFile(t, uiDir, "feature.go", "//go:build feature\n\npackage ui\ntype Active int\n")

	module, err := Open(Options{ModuleRoot: root, ModulePath: "example.com/app"})
	if err != nil {
		t.Fatal(err)
	}
	output, diagnostics, err := module.Generate(uiDir)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if hasError(diagnostics) {
		t.Fatalf("Generate diagnostics = %v, want authoritative feature-tag companion", diagnostics)
	}
	if len(output) != 1 || len(output[filepath.Join(uiDir, "card.gsx")]) == 0 {
		t.Fatalf("Generate output = %v, want card.gsx", keysOfGenerated(output))
	}
}

func TestGenerateUsesAuthoritativeCgoSyntax(t *testing.T) {
	if !build.Default.CgoEnabled {
		t.Skip("cgo is disabled for the active build context")
	}
	root, uiDir := writeTargetDeclarationTestModule(t)
	writeFile(t, uiDir, "card.gsx", "package ui\ncomponent Card(value Native) { <div/> }\n")
	writeFile(t, uiDir, "native.go", `package ui

/* typedef int native_int; */
import "C"

type Native C.native_int
`)

	module, err := Open(Options{ModuleRoot: root, ModulePath: "example.com/app"})
	if err != nil {
		t.Fatal(err)
	}
	output, diagnostics, err := module.Generate(uiDir)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if hasError(diagnostics) {
		t.Fatalf("Generate diagnostics = %v, want retained cgo-transformed syntax", diagnostics)
	}
	if len(output) != 1 || len(output[filepath.Join(uiDir, "card.gsx")]) == 0 {
		t.Fatalf("Generate output = %v, want card.gsx", keysOfGenerated(output))
	}
}

func TestTargetSourceInventoryRejectsExplicitGoOverlay(t *testing.T) {
	root, uiDir := writeTargetDeclarationTestModule(t)
	overlayPath := filepath.Join(root, "user-overlay.json")
	writeFile(t, uiDir, "model.go", "package ui\ntype Active string\n")
	writeFile(t, root, "user-overlay.json", `{"Replace":{}}`)
	t.Setenv("GOFLAGS", "-overlay="+overlayPath)

	module, err := Open(Options{ModuleRoot: root, ModulePath: "example.com/app"})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := module.externalImporter(); err == nil || !strings.Contains(err.Error(), "GOFLAGS -overlay") {
		t.Fatalf("externalImporter error = %v, want hard GOFLAGS -overlay boundary", err)
	}
	if got := module.externalLoads(); got != 0 {
		t.Fatalf("external loads = %d, want rejection before packages.Load", got)
	}
}

func TestTargetSourceInventoryRepresentsGsxOnlyPackageWithPairedOutput(t *testing.T) {
	root, uiDir := writeTargetDeclarationTestModule(t)
	writeFile(t, uiDir, "card.x.go", "package stale\nfunc (\n")
	module, err := Open(Options{ModuleRoot: root, ModulePath: "example.com/app"})
	if err != nil {
		t.Fatal(err)
	}
	external, err := module.externalImporter()
	if err != nil {
		t.Fatal(err)
	}
	packageInfo, found, ready := module.targetSourcePackage(uiDir)
	if !ready || !found {
		t.Fatalf("GSX-only inventory = found %v ready %v, want explicit package entry", found, ready)
	}
	if len(packageInfo.compiledGoFiles) != 0 || len(packageInfo.metadataErrors) != 0 {
		t.Fatalf("GSX-only inventory = files %v metadata %v, want authoritative empty companion set", packageInfo.compiledGoFiles, packageInfo.metadataErrors)
	}
	pkg, err := newComponentTargetImporter(module, external).Import("example.com/app/ui")
	if err != nil {
		t.Fatal(err)
	}
	if got := exactTargetSignature(t, pkg, "Card").Params().Len(); got != 2 {
		t.Fatalf("exact Card arity = %d, want authored arity 2", got)
	}
}

func TestTargetSourceInventoryExplicitlyLoadsWildcardOmittedGsxPackages(t *testing.T) {
	for _, relativeDir := range []string{"testdata/ui", "_private/ui"} {
		t.Run(relativeDir, func(t *testing.T) {
			root := t.TempDir()
			dir := filepath.Join(root, filepath.FromSlash(relativeDir))
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			repoRoot, err := filepath.Abs("../..")
			if err != nil {
				t.Fatal(err)
			}
			writeFile(t, root, "go.mod", "module example.com/app\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
			writeFile(t, dir, "model.go", "package ui\ntype Model struct{}\n")
			writeFile(t, dir, "card.gsx", "package ui\ncomponent Card(value Model) { <div/> }\n")
			module, err := Open(Options{ModuleRoot: root, ModulePath: "example.com/app"})
			if err != nil {
				t.Fatal(err)
			}
			external, err := module.externalImporter()
			if err != nil {
				t.Fatal(err)
			}
			packagePath, _ := importPathForDir(root, "example.com/app", dir)
			pkg, err := newComponentTargetImporter(module, external).Import(packagePath)
			if err != nil {
				t.Fatal(err)
			}
			if got := exactTargetSignature(t, pkg, "Card").Params().At(0).Type().String(); !strings.HasSuffix(got, ".Model") {
				t.Fatalf("Card parameter = %s, want retained Model", got)
			}
		})
	}
}

func TestTargetSourceInventoryLoadsDependenciesImportedOnlyByGsx(t *testing.T) {
	root := t.TempDir()
	depRoot := filepath.Join(root, "dep")
	uiDir := filepath.Join(root, "ui")
	for _, dir := range []string{depRoot, uiDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, root, "go.mod", "module example.com/app\n\ngo 1.26.1\n\nrequire (\n\tgithub.com/gsxhq/gsx v0.0.0\n\texample.com/dep v0.0.0\n)\nreplace github.com/gsxhq/gsx => "+repoRoot+"\nreplace example.com/dep => ./dep\n")
	writeFile(t, depRoot, "go.mod", "module example.com/dep\n\ngo 1.26.1\n")
	writeFile(t, depRoot, "dep.go", "package dep\ntype Value string\n")
	writeFile(t, uiDir, "card.gsx", `package ui

import "example.com/dep"

component Card(value dep.Value) { <div/> }
`)
	module, err := Open(Options{ModuleRoot: root, ModulePath: "example.com/app"})
	if err != nil {
		t.Fatal(err)
	}
	external, err := module.externalImporter()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := external.Import("example.com/dep"); err != nil {
		t.Fatalf("GSX-only import was absent from the one cold load: %v", err)
	}
	pkg, err := newComponentTargetImporter(module, external).Import("example.com/app/ui")
	if err != nil {
		t.Fatal(err)
	}
	if got := exactTargetSignature(t, pkg, "Card").Params().At(0).Type().String(); got != "example.com/dep.Value" {
		t.Fatalf("Card parameter = %s, want example.com/dep.Value", got)
	}
}

func TestTargetSourceInventoryRebuildsWhenGsxImportSurfaceChanges(t *testing.T) {
	root := t.TempDir()
	depRoot := filepath.Join(root, "dep")
	uiDir := filepath.Join(root, "ui")
	for _, dir := range []string{depRoot, uiDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, root, "go.mod", "module example.com/app\n\ngo 1.26.1\n\nrequire (\n\tgithub.com/gsxhq/gsx v0.0.0\n\texample.com/dep v0.0.0\n)\nreplace github.com/gsxhq/gsx => "+repoRoot+"\nreplace example.com/dep => ./dep\n")
	writeFile(t, depRoot, "go.mod", "module example.com/dep\n\ngo 1.26.1\n")
	writeFile(t, depRoot, "dep.go", "package dep\ntype Value string\n")
	cardPath := filepath.Join(uiDir, "card.gsx")
	writeFile(t, uiDir, "card.gsx", "package ui\ncomponent Card(value int) { <div/> }\n")
	module, err := Open(Options{ModuleRoot: root, ModulePath: "example.com/app"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := module.externalImporter(); err != nil {
		t.Fatal(err)
	}
	if module.externalImportPaths["example.com/dep"] {
		t.Fatal("dependency imported only by the future override was already published")
	}
	module.SetOverride(cardPath, []byte(`package ui

import "example.com/dep"

component Card(value dep.Value) { <div/> }
`))
	if !module.sourceInventoryDirty {
		t.Fatal("new GSX import did not invalidate the authoritative load manifest")
	}
	module.maybeRebuildFset()
	external, err := module.externalImporter()
	if err != nil {
		t.Fatal(err)
	}
	pkg, err := newComponentTargetImporter(module, external).Import("example.com/app/ui")
	if err != nil {
		t.Fatal(err)
	}
	if got := exactTargetSignature(t, pkg, "Card").Params().At(0).Type().String(); got != "example.com/dep.Value" {
		t.Fatalf("Card parameter after manifest rebuild = %s, want example.com/dep.Value", got)
	}
	if got := module.externalLoads(); got != 2 {
		t.Fatalf("external loads = %d, want one cold load plus one import-surface rebuild", got)
	}
}

func TestTargetSourceInventoryKeepsWarmForAlreadyPublishedImport(t *testing.T) {
	root, uiDir := writeTargetDeclarationTestModule(t)
	cardPath := filepath.Join(uiDir, "card.gsx")
	module, err := Open(Options{ModuleRoot: root, ModulePath: "example.com/app"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := module.externalImporter(); err != nil {
		t.Fatal(err)
	}
	if !module.externalImportPaths["github.com/gsxhq/gsx"] {
		t.Fatal("mandatory runtime import was not published by the cold importer")
	}

	module.SetOverride(cardPath, []byte(`package ui

import "github.com/gsxhq/gsx"

component Card(value gsx.Attrs) { <div/> }
`))
	if module.sourceInventoryDirty {
		t.Fatal("already-published GSX import unnecessarily invalidated the cold source inventory")
	}
	module.maybeRebuildFset()
	external, err := module.externalImporter()
	if err != nil {
		t.Fatal(err)
	}
	pkg, err := newComponentTargetImporter(module, external).Import("example.com/app/ui")
	if err != nil {
		t.Fatal(err)
	}
	if got := exactTargetSignature(t, pkg, "Card").Params().At(0).Type().String(); got != "github.com/gsxhq/gsx.Attrs" {
		t.Fatalf("Card parameter after warm import edit = %s, want gsx.Attrs", got)
	}
	if got := module.externalLoads(); got != 1 {
		t.Fatalf("external loads = %d, want one cold load and no already-published-import reload", got)
	}
}

func TestTargetDeclarationImporterRejectsActiveCompanionComponentCollision(t *testing.T) {
	root, uiDir := writeTargetDeclarationTestModule(t)
	writeFile(t, uiDir, "manual.go", `package ui

import "github.com/gsxhq/gsx"

func Card(value bool) gsx.Node { return nil }
`)
	module, err := Open(Options{ModuleRoot: root, ModulePath: "example.com/app"})
	if err != nil {
		t.Fatal(err)
	}
	external, err := module.externalImporter()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := newComponentTargetImporter(module, external).Import("example.com/app/ui"); err == nil {
		t.Fatal("active companion/component redeclaration was accepted")
	}
	if module.targetDeclTypes[uiDir] != nil {
		t.Fatal("active companion/component redeclaration cached an exact package")
	}
}

func TestTargetDeclarationImporterRejectsEveryActiveCompanionComponentCollision(t *testing.T) {
	for _, test := range []struct {
		name      string
		gsxSource string
		goSource  string
	}{
		{
			name:      "package variable",
			gsxSource: "package ui\ncomponent Card(value int) { <div/> }\n",
			goSource:  "package ui\nvar Card = 1\n",
		},
		{
			name:      "package type",
			gsxSource: "package ui\ncomponent Card(value int) { <div/> }\n",
			goSource:  "package ui\ntype Card struct{}\n",
		},
		{
			name: "method",
			gsxSource: `package ui
type Form struct{}
component (f Form) Field(value int) { <div/> }
`,
			goSource: `package ui
import "github.com/gsxhq/gsx"
func (Form) Field(value bool) gsx.Node { return nil }
`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			root, uiDir := writeTargetDeclarationTestModule(t)
			writeFile(t, uiDir, "card.gsx", test.gsxSource)
			writeFile(t, uiDir, "manual.go", test.goSource)
			module, err := Open(Options{ModuleRoot: root, ModulePath: "example.com/app"})
			if err != nil {
				t.Fatal(err)
			}
			external, err := module.externalImporter()
			if err != nil {
				t.Fatal(err)
			}
			if _, err := newComponentTargetImporter(module, external).Import("example.com/app/ui"); err == nil {
				t.Fatal("active companion collision was accepted")
			}
			if module.targetDeclTypes[uiDir] != nil {
				t.Fatal("active companion collision published an exact package")
			}
		})
	}
}

func TestTargetDeclarationImporterRejectsCompanionCollisionWithGsxVariants(t *testing.T) {
	root, uiDir := writeTargetDeclarationTestModule(t)
	writeFile(t, uiDir, "card.gsx", "//go:build variantA\n\npackage ui\ncomponent Card(value int) { <div/> }\n")
	writeFile(t, uiDir, "card_b.gsx", "//go:build variantB\n\npackage ui\ncomponent Card(value int) { <span/> }\n")
	writeFile(t, uiDir, "manual.go", `package ui
import "github.com/gsxhq/gsx"
func Card(value bool) gsx.Node { return nil }
`)
	module, err := Open(Options{ModuleRoot: root, ModulePath: "example.com/app"})
	if err != nil {
		t.Fatal(err)
	}
	external, err := module.externalImporter()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := newComponentTargetImporter(module, external).Import("example.com/app/ui"); err == nil {
		t.Fatal("active companion collision with a logical GSX variant set was accepted")
	}
	if module.targetDeclTypes[uiDir] != nil {
		t.Fatal("variant collision published an exact package")
	}
}

func TestTargetDeclarationImporterRejectsGoChunkComponentCollision(t *testing.T) {
	root, uiDir := writeTargetDeclarationTestModule(t)
	writeFile(t, uiDir, "card.gsx", `package ui
import "github.com/gsxhq/gsx"
func Card(value bool) gsx.Node { return nil }
component Card(value int) { <div/> }
`)
	module, err := Open(Options{ModuleRoot: root, ModulePath: "example.com/app"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := newComponentTargetImporter(module, targetTestImporter()).Import("example.com/app/ui"); err == nil {
		t.Fatal("GoChunk/component collision was accepted")
	}
	if module.targetDeclTypes[uiDir] != nil {
		t.Fatal("GoChunk/component collision published an exact package")
	}
}

func TestTargetDeclarationImporterRejectsWithinFileDuplicateInNonRepresentativeVariant(t *testing.T) {
	root, uiDir := writeTargetDeclarationTestModule(t)
	writeFile(t, uiDir, "a.gsx", "//go:build variantA\n\npackage ui\ncomponent Card(value int) { <div/> }\n")
	writeFile(t, uiDir, "z.gsx", `//go:build variantB

package ui
component Card(value int) { <span/> }
component Card(value int) { <b/> }
`)
	module, err := Open(Options{ModuleRoot: root, ModulePath: "example.com/app"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := newComponentTargetImporter(module, targetTestImporter()).Import("example.com/app/ui"); err == nil {
		t.Fatal("within-file duplicate in a non-representative variant was folded away")
	}
	if module.targetDeclTypes[uiDir] != nil {
		t.Fatal("within-file duplicate published an exact package")
	}
}

func TestTargetDeclarationImporterUsesAuthoritativeCgoSyntax(t *testing.T) {
	if !build.Default.CgoEnabled {
		t.Skip("cgo is disabled for the active build context")
	}
	root, uiDir := writeTargetDeclarationTestModule(t)
	writeFile(t, uiDir, "card.gsx", "package ui\ncomponent Card(value Native) { <div/> }\n")
	writeFile(t, uiDir, "native.go", `package ui

/* typedef int native_int; */
import "C"

type Native C.native_int
`)
	module, err := Open(Options{ModuleRoot: root, ModulePath: "example.com/app"})
	if err != nil {
		t.Fatal(err)
	}
	external, err := module.externalImporter()
	if err != nil {
		t.Fatal(err)
	}
	pkg, err := newComponentTargetImporter(module, external).Import("example.com/app/ui")
	if err != nil {
		t.Fatal(err)
	}
	param := exactTargetSignature(t, pkg, "Card").Params().At(0)
	if param.Name() != "value" || param.Type().String() != "example.com/app/ui.Native" {
		t.Fatalf("Card parameter = %s %s, want value Native", param.Name(), param.Type())
	}
}

func TestTargetDeclarationImporterRejectsActiveCompanionSyntaxError(t *testing.T) {
	root, uiDir := writeTargetDeclarationTestModule(t)
	writeFile(t, uiDir, "broken.go", "package ui\nfunc (\n")
	module, err := Open(Options{ModuleRoot: root, ModulePath: "example.com/app"})
	if err != nil {
		t.Fatal(err)
	}
	external, err := module.externalImporter()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := newComponentTargetImporter(module, external).Import("example.com/app/ui"); err == nil {
		t.Fatal("active companion syntax error was accepted")
	}
	if module.targetDeclTypes[uiDir] != nil {
		t.Fatal("active companion syntax error published an exact package")
	}
}

func TestTargetDeclarationImporterExcludesInactiveCompanionCollision(t *testing.T) {
	t.Setenv("GOFLAGS", "-tags=feature")
	root, uiDir := writeTargetDeclarationTestModule(t)
	writeFile(t, uiDir, "active.go", "//go:build feature\n\npackage ui\ntype Active int\n")
	writeFile(t, uiDir, "inactive.go", `//go:build !feature

package ui

import "github.com/gsxhq/gsx"

func Card(value bool) gsx.Node { return nil }
`)
	module, err := Open(Options{ModuleRoot: root, ModulePath: "example.com/app"})
	if err != nil {
		t.Fatal(err)
	}
	external, err := module.externalImporter()
	if err != nil {
		t.Fatal(err)
	}
	pkg, err := newComponentTargetImporter(module, external).Import("example.com/app/ui")
	if err != nil {
		t.Fatal(err)
	}
	if exactTargetSignature(t, pkg, "Card").Params().Len() != 2 {
		t.Fatal("inactive companion replaced the authored component declaration")
	}
}

func TestTargetSourceInventoryIncludesUnsavedGsxPairBeforeColdLoad(t *testing.T) {
	root := t.TempDir()
	uiDir := filepath.Join(root, "ui")
	if err := os.MkdirAll(uiDir, 0o755); err != nil {
		t.Fatal(err)
	}
	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, root, "go.mod", "module example.com/app\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	writeFile(t, uiDir, "card.x.go", "package stale\nfunc (\n")
	module, err := Open(Options{ModuleRoot: root, ModulePath: "example.com/app"})
	if err != nil {
		t.Fatal(err)
	}
	module.SetOverride(filepath.Join(uiDir, "card.gsx"), []byte("package ui\ncomponent Card(value int) { <div/> }\n"))
	external, err := module.externalImporter()
	if err != nil {
		t.Fatal(err)
	}
	pkg, err := newComponentTargetImporter(module, external).Import("example.com/app/ui")
	if err != nil {
		t.Fatal(err)
	}
	if got := exactTargetSignature(t, pkg, "Card").Params().At(0).Name(); got != "value" {
		t.Fatalf("Card parameter = %q, want override declaration", got)
	}
}

func TestTargetSourceInventoryRebuildsWhenNewOverrideClaimsPairedOutput(t *testing.T) {
	root := t.TempDir()
	uiDir := filepath.Join(root, "ui")
	if err := os.MkdirAll(uiDir, 0o755); err != nil {
		t.Fatal(err)
	}
	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, root, "go.mod", "module example.com/app\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	writeFile(t, uiDir, "card.x.go", "package stale\nvar Old = true\n")
	module, err := Open(Options{ModuleRoot: root, ModulePath: "example.com/app"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := module.externalImporter(); err != nil {
		t.Fatal(err)
	}
	module.SetOverride(filepath.Join(uiDir, "card.gsx"), []byte("package ui\ncomponent Card(value int) { <div/> }\n"))
	if !module.sourceInventoryDirty {
		t.Fatal("new authoritative GSX source did not invalidate the cold source inventory")
	}
	module.maybeRebuildFset()
	if module.sourceInventoryReady || module.sourceInventoryDirty {
		t.Fatal("source inventory rebuild did not clear the old selection atomically")
	}
	external, err := module.externalImporter()
	if err != nil {
		t.Fatal(err)
	}
	pkg, err := newComponentTargetImporter(module, external).Import("example.com/app/ui")
	if err != nil {
		t.Fatal(err)
	}
	if exactTargetSignature(t, pkg, "Card").Params().Len() != 1 {
		t.Fatal("rebuilt source inventory did not expose the new GSX declaration")
	}
	if got := module.externalLoads(); got != 2 {
		t.Fatalf("external loads = %d, want one initial load and one required rebuild", got)
	}
}

func TestTargetSourceInventoryRebuildsWhenEmptyOverrideClaimsPairedOutput(t *testing.T) {
	root := t.TempDir()
	uiDir := filepath.Join(root, "ui")
	if err := os.MkdirAll(uiDir, 0o755); err != nil {
		t.Fatal(err)
	}
	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, root, "go.mod", "module example.com/app\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	writeFile(t, uiDir, "model.go", "package ui\ntype Model struct{}\n")
	writeFile(t, uiDir, "card.x.go", "package stale\nvar Old = true\n")
	module, err := Open(Options{ModuleRoot: root, ModulePath: "example.com/app"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := module.externalImporter(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(uiDir, "card.gsx")
	module.SetOverride(path, nil)
	if !module.sourceInventoryDirty {
		t.Fatal("first empty override did not invalidate a newly claimed paired output")
	}
	module.SetOverride(path, []byte("package ui\ncomponent Card(value int) { <div/> }\n"))
	module.maybeRebuildFset()
	external, err := module.externalImporter()
	if err != nil {
		t.Fatal(err)
	}
	pkg, err := newComponentTargetImporter(module, external).Import("example.com/app/ui")
	if err != nil {
		t.Fatal(err)
	}
	if exactTargetSignature(t, pkg, "Card").Params().Len() != 1 {
		t.Fatal("rebuilt source inventory did not expose the newly filled override")
	}
}

func TestTargetSourceInventoryRejectsBundleCompanions(t *testing.T) {
	root, uiDir := writeTargetDeclarationTestModule(t)
	writeFile(t, uiDir, "model.go", "package ui\ntype Model struct{}\n")
	module, err := Open(Options{
		ModuleRoot: root,
		ModulePath: "example.com/app",
		Bundle:     testBundle(targetTestImporter(), funcTables{}),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := newComponentTargetImporter(module, targetTestImporter()).Import("example.com/app/ui"); err == nil {
		t.Fatal("Bundle mode guessed an active companion file set without source inventory")
	}
}

func TestTargetSourceInventoryExcludesNestedModule(t *testing.T) {
	root, rootUI := writeTargetDeclarationTestModule(t)
	nested := filepath.Join(root, "nested")
	uiDir := filepath.Join(nested, "ui")
	if err := os.MkdirAll(uiDir, 0o755); err != nil {
		t.Fatal(err)
	}
	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, root, "go.mod", "module example.com/app\n\ngo 1.26.1\n\nrequire (\n github.com/gsxhq/gsx v0.0.0\n example.com/app/nested v0.0.0\n)\nreplace github.com/gsxhq/gsx => "+repoRoot+"\nreplace example.com/app/nested => ./nested\n")
	writeFile(t, nested, "go.mod", "module example.com/app/nested\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	writeFile(t, uiDir, "view.gsx", "package ui\ncomponent View(value Model) { <div/> }\n")
	writeFile(t, uiDir, "model.go", "package ui\ntype Model struct{}\n")
	writeFile(t, uiDir, "view.x.go", "package ui\nimport \"github.com/gsxhq/gsx\"\ntype ViewProps struct { Value Model }; func View(ViewProps) gsx.Node { return nil }\n")
	writeFile(t, rootUI, "card.gsx", "package ui\nimport nested \"example.com/app/nested/ui\"\ncomponent Card(value nested.Model) { <div/> }\n")
	module, err := Open(Options{ModuleRoot: root, ModulePath: "example.com/app"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := module.externalImporter(); err != nil {
		t.Fatal(err)
	}
	if _, found, ready := module.targetSourcePackage(uiDir); !ready || found {
		t.Fatalf("nested-module inventory = found %v ready %v, want authoritative absence", found, ready)
	}
}

func TestTargetSourceOverlayDoesNotHideNestedModuleGeneratedOutput(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "nested")
	nestedUI := filepath.Join(nested, "ui")
	if err := os.MkdirAll(nestedUI, 0o755); err != nil {
		t.Fatal(err)
	}
	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, root, "go.mod", "module example.com/app\n\ngo 1.26.1\n\nrequire example.com/nested v0.0.0\nreplace example.com/nested => ./nested\n")
	writeFile(t, nested, "go.mod", "module example.com/nested\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	writeFile(t, nestedUI, "view.gsx", "package ui\ncomponent View(value int) { <div/> }\n")
	writeFile(t, nestedUI, "view.x.go", `package ui

import "github.com/gsxhq/gsx"

func View(value int) gsx.Node { return nil }
`)
	writeFile(t, root, "bridge.go", `package app

import nested "example.com/nested/ui"

var _ = nested.View
`)

	module, err := Open(Options{ModuleRoot: root, ModulePath: "example.com/app"})
	if err != nil {
		t.Fatal(err)
	}
	external, err := module.externalImporter()
	if err != nil {
		t.Fatal(err)
	}
	pkg, err := external.Import("example.com/nested/ui")
	if err != nil {
		t.Fatal(err)
	}
	if pkg.Scope().Lookup("View") == nil {
		t.Fatalf("parent module overlay hid nested generated output; root load errors=%v", module.extErrs["example.com/app"])
	}
}

func TestTargetDeclarationImporterRechecksGoOnlyIntermediaryThroughExactGraph(t *testing.T) {
	root := t.TempDir()
	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, root, "go.mod", "module example.com/app\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")

	viewsDir := filepath.Join(root, "views")
	bridgeDir := filepath.Join(root, "bridge")
	pagesDir := filepath.Join(root, "pages")
	for _, dir := range []string{viewsDir, bridgeDir, pagesDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeFile(t, viewsDir, "card.gsx", `package views

component Card(title string) { <div>{ title }</div> }
`)
	writeFile(t, bridgeDir, "bridge.go", `package bridge

import "example.com/app/views"

var Card = views.Card
`)
	writeFile(t, pagesDir, "page.gsx", `package pages

import "example.com/app/bridge"

component Page() { <bridge.Card title="hello"/> }
`)

	module, err := Open(Options{ModuleRoot: root, ModulePath: "example.com/app"})
	if err != nil {
		t.Fatal(err)
	}
	external, err := module.externalImporter()
	if err != nil {
		t.Fatal(err)
	}
	analysis, err := module.analyze(pagesDir, &moduleImporter{m: module, external: external, seen: map[string]bool{}})
	if err != nil {
		t.Fatal(err)
	}
	if len(analysis.targetDiagnostics) != 0 || len(analysis.targetErrs) != 0 {
		t.Fatalf("exact target discovery through Go-only intermediary failed: diagnostics=%v errors=%v", analysis.targetDiagnostics, analysis.targetErrs)
	}
	if len(analysis.targetFacts) != 1 {
		t.Fatalf("target facts = %d, want one bridge.Card fact", len(analysis.targetFacts))
	}
	fact := analysis.targetFacts[analysis.callSites.records[0].id]
	if fact.provenance != targetPackageVar || fact.raw == nil || fact.raw.Params().Len() != 1 || fact.raw.Params().At(0).Name() != "title" {
		t.Fatalf("bridge.Card fact = %+v, want exact package variable with (title string)", fact)
	}
	if got := module.targetImports[pagesDir]; len(got) != 1 || got[0] != bridgeDir || !module.targetImportedBy[bridgeDir][pagesDir] {
		t.Fatalf("exact target graph pages->bridge = forward %v reverse %v", got, module.targetImportedBy[bridgeDir])
	}
	if got := module.targetImports[bridgeDir]; len(got) != 1 || got[0] != viewsDir || !module.targetImportedBy[viewsDir][bridgeDir] {
		t.Fatalf("exact target graph bridge->views = forward %v reverse %v", got, module.targetImportedBy[viewsDir])
	}
	if module.targetDeclTypes[bridgeDir] == nil || module.targetDeclTypes[viewsDir] == nil {
		t.Fatal("completed exact intermediary graph was not cached")
	}
	dependents := module.Dependents(viewsDir)
	if len(dependents) != 2 || dependents[0] != pagesDir || dependents[1] != viewsDir {
		t.Fatalf("Dependents(views) = %v, want only authoritative GSX-owned pages and views dirs", dependents)
	}
	module.SetOverride(filepath.Join(viewsDir, "card.gsx"), []byte("package views\ncomponent Card(title string, count int) { <div/> }\n"))
	module.applyDirty()
	if module.targetDeclTypes[bridgeDir] != nil || module.targetDeclTypes[viewsDir] != nil {
		t.Fatal("editing the GSX dependency did not invalidate the Go-only intermediary exact cache")
	}
}

func TestFailedExactImportChainRetainsPathProvenanceForRepair(t *testing.T) {
	root := t.TempDir()
	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, root, "go.mod", "module example.com/app\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")

	viewsDir := filepath.Join(root, "views")
	bridgeDir := filepath.Join(root, "bridge")
	pagesDir := filepath.Join(root, "pages")
	for _, dir := range []string{viewsDir, bridgeDir, pagesDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	viewsPath := filepath.Join(viewsDir, "card.gsx")
	writeFile(t, viewsDir, "card.gsx", "package views\ncomponent Card(title Missing) { <div/> }\n")
	writeFile(t, bridgeDir, "bridge.go", `package bridge

import "example.com/app/views"

var Card = views.Card
`)
	writeFile(t, pagesDir, "page.gsx", `package pages

import _ "example.com/app/bridge"

component Local() { <span/> }
component Page() { <Local/> }
`)

	module, err := Open(Options{ModuleRoot: root, ModulePath: "example.com/app"})
	if err != nil {
		t.Fatal(err)
	}
	first, err := module.Package(pagesDir)
	if err != nil {
		t.Fatalf("first Package: %v", err)
	}
	if hasError(first.Diags) {
		t.Fatalf("first shipping diagnostics = %v, want exact-target failure retained privately during the foundation phase", first.Diags)
	}
	if got := module.targetImports[pagesDir]; len(got) != 1 || got[0] != bridgeDir {
		t.Fatalf("failed exact graph pages->bridge = %v, want path provenance", got)
	}
	if got := module.targetImports[bridgeDir]; len(got) != 1 || got[0] != viewsDir {
		t.Fatalf("failed exact graph bridge->views = %v, want path provenance", got)
	}
	if module.targetDeclTypes[bridgeDir] != nil || module.targetDeclTypes[viewsDir] != nil {
		t.Fatal("failed exact chain published semantic packages")
	}
	if _, err := newComponentTargetImporter(module, module.ext).Import("example.com/app/views"); err == nil || !strings.Contains(err.Error(), "undefined: Missing") {
		t.Fatalf("invalid leaf exact import error = %v, want undefined Missing", err)
	}

	module.SetOverride(viewsPath, []byte("package views\ncomponent Card(title string) { <div/> }\n"))
	second, err := module.Package(pagesDir)
	if err != nil {
		t.Fatalf("repaired Package: %v", err)
	}
	if second == first {
		t.Fatal("leaf repair reused the cached failed consumer PackageResult")
	}
	if hasError(second.Diags) {
		t.Fatalf("repaired diagnostics = %v, want successful recomputation", second.Diags)
	}
	output, diagnostics, err := module.Generate(pagesDir)
	if err != nil {
		t.Fatalf("repaired Generate: %v", err)
	}
	if hasError(diagnostics) || len(output[filepath.Join(pagesDir, "page.gsx")]) == 0 {
		t.Fatalf("repaired Generate output=%v diagnostics=%v", keysOfGenerated(output), diagnostics)
	}
}

func TestFailedExactCheckReplacesStaleDirectPathProvenance(t *testing.T) {
	root := t.TempDir()
	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, root, "go.mod", "module example.com/app\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")

	oldDir := filepath.Join(root, "old")
	brokenDir := filepath.Join(root, "broken")
	pagesDir := filepath.Join(root, "pages")
	for _, dir := range []string{oldDir, brokenDir, pagesDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeFile(t, oldDir, "old.gsx", "package old\ncomponent Old() { <span/> }\n")
	writeFile(t, brokenDir, "broken.gsx", "package broken\ncomponent Broken(value Missing) { <span/> }\n")
	pagePath := filepath.Join(pagesDir, "page.gsx")
	writeFile(t, pagesDir, "page.gsx", `package pages

import _ "example.com/app/old"

component Local() { <span/> }
component Page() { <Local/> }
`)

	module, err := Open(Options{ModuleRoot: root, ModulePath: "example.com/app"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := module.Package(pagesDir); err != nil {
		t.Fatalf("initial Package: %v", err)
	}
	if got := module.targetImports[pagesDir]; len(got) != 1 || got[0] != oldDir || !module.targetImportedBy[oldDir][pagesDir] {
		t.Fatalf("initial exact edge = %v reverse=%v, want pages->old", got, module.targetImportedBy[oldDir])
	}

	module.SetOverride(pagePath, []byte(`package pages

import _ "example.com/app/broken"

component Local() { <span/> }
component Page() { <Local/> }
`))
	if _, err := module.Package(pagesDir); err != nil {
		t.Fatalf("failed-check Package infrastructure error: %v", err)
	}
	if got := module.targetImports[pagesDir]; len(got) != 1 || got[0] != brokenDir {
		t.Fatalf("failed-check exact edges = %v, want only current pages->broken", got)
	}
	if module.targetImportedBy[oldDir][pagesDir] {
		t.Fatalf("stale reverse edge pages->old survived failed exact check: %v", module.targetImportedBy[oldDir])
	}
	if !module.targetImportedBy[brokenDir][pagesDir] {
		t.Fatalf("current reverse edge pages->broken missing after failed exact check: %v", module.targetImportedBy[brokenDir])
	}
	if module.targetDeclTypes[brokenDir] != nil {
		t.Fatal("failed exact dependency published a semantic package")
	}
}

func exactTargetSignature(t *testing.T, pkg *types.Package, name string) *types.Signature {
	t.Helper()
	object, ok := pkg.Scope().Lookup(name).(*types.Func)
	if !ok {
		t.Fatalf("%s scope object = %T, want *types.Func", name, pkg.Scope().Lookup(name))
	}
	signature, ok := object.Type().(*types.Signature)
	if !ok {
		t.Fatalf("%s type = %T, want *types.Signature", name, object.Type())
	}
	return signature
}

func TestTargetDeclarationImporterUsesVerbatimSignatureAndHidesPairedOutputBeforeBuildSelection(t *testing.T) {
	root, uiDir := writeTargetDeclarationTestModule(t)
	// A paired generated output must be invisible before the Go command classifies the
	// directory. The deliberately wrong package clause makes the test
	// non-vacuous: filtering only after package loading would fail with mixed packages.
	writeFile(t, uiDir, "card.x.go", "package stale\nvar StaleTargetMarker = 1\n")

	module, err := Open(Options{ModuleRoot: root, ModulePath: "example.com/app"})
	if err != nil {
		t.Fatal(err)
	}
	importer := newComponentTargetImporter(module, targetTestImporter())
	pkg, err := importer.Import("example.com/app/ui")
	if err != nil {
		t.Fatal(err)
	}
	if pkg.Scope().Lookup("StaleTargetMarker") != nil {
		t.Fatal("paired stale output entered exact target package scope")
	}
	signature := exactTargetSignature(t, pkg, "Card")
	if signature.Params().Len() != 2 || signature.Params().At(0).Name() != "title" || signature.Params().At(1).Name() != "count" {
		t.Fatalf("Card params = %s, want (title string, count int)", signature.Params())
	}
	if _, exists := module.targetDeclTypes[uiDir]; !exists {
		t.Fatal("completed exact declaration package was not cached separately")
	}
	if _, exists := module.pkgTypes[uiDir]; exists {
		t.Fatal("exact declaration package leaked into shipping Props cache")
	}
}

func TestTargetDeclarationImporterFailsClosedOnMixedCompanionPackages(t *testing.T) {
	root, uiDir := writeTargetDeclarationTestModule(t)
	writeFile(t, uiDir, "model.go", "package other\ntype Model struct{}\n")

	module, err := Open(Options{ModuleRoot: root, ModulePath: "example.com/app"})
	if err != nil {
		t.Fatal(err)
	}
	external, err := module.externalImporter()
	if err != nil {
		t.Fatal(err)
	}
	_, err = newComponentTargetImporter(module, external).Import("example.com/app/ui")
	if err == nil {
		t.Fatal("mixed companion package was silently omitted")
	}
	if _, exists := module.targetDeclTypes[uiDir]; exists {
		t.Fatal("failed exact declaration package was cached")
	}
}

func TestTargetDeclarationImporterDoesNotCacheSkippedInvalidComponent(t *testing.T) {
	root, uiDir := writeTargetDeclarationTestModule(t)
	writeFile(t, uiDir, "card.gsx", "package ui\ncomponent Card(value !) { <div/> }\n")

	module, err := Open(Options{ModuleRoot: root, ModulePath: "example.com/app"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = newComponentTargetImporter(module, targetTestImporter()).Import("example.com/app/ui")
	if err == nil {
		t.Fatal("invalid exact component declaration was silently skipped")
	}
	if _, exists := module.targetDeclTypes[uiDir]; exists {
		t.Fatal("package with a skipped invalid component was cached")
	}
}

func TestAnalyzeDiscoversCrossPackageTargetFromExactCacheNotShippingProps(t *testing.T) {
	root, uiDir := writeTargetDeclarationTestModule(t)
	pagesDir := filepath.Join(root, "pages")
	if err := os.MkdirAll(pagesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, pagesDir, "home.gsx", `package pages

import "example.com/app/ui"

component Home() { <ui.Card title="hello" count={1}/> }
`)
	// Keep a valid but Props-shaped paired output on disk. Shipping analysis has
	// its own in-memory skeleton, while exact target discovery must ignore this
	// file and expose the two authored parameters.
	writeFile(t, uiDir, "card.x.go", `package ui

import "github.com/gsxhq/gsx"

type CardProps struct { Title string; Count int }
func Card(CardProps) gsx.Node { return nil }
`)

	bundle := testBundle(targetTestImporter(), funcTables{})
	module, err := Open(Options{ModuleRoot: root, ModulePath: "example.com/app", Bundle: bundle})
	if err != nil {
		t.Fatal(err)
	}
	analysis, err := module.analyze(pagesDir, &moduleImporter{m: module, external: bundle.importer(), seen: map[string]bool{}})
	if err != nil {
		t.Fatal(err)
	}
	if len(analysis.targetFacts) != 1 {
		t.Fatalf("target facts = %d, want one imported Card", len(analysis.targetFacts))
	}
	fact := analysis.targetFacts[analysis.callSites.records[0].id]
	if fact.provenance != targetPackageFunc || fact.raw == nil || len(fact.targetDiags) != 0 {
		t.Fatalf("imported Card fact = %+v", fact)
	}
	if fact.raw.Params().Len() != 2 || fact.raw.Params().At(0).Name() != "title" || fact.raw.Params().At(1).Name() != "count" {
		t.Fatalf("imported Card params = %s, want exact authored signature", fact.raw.Params())
	}
	shipping := module.pkgTypes[uiDir]
	exact := module.targetDeclTypes[uiDir]
	if shipping == nil || exact == nil || shipping == exact {
		t.Fatalf("shipping/exact package caches = %p/%p, want distinct populated packages", shipping, exact)
	}
	if got := exactTargetSignature(t, shipping, "Card").Params().Len(); got != 1 {
		t.Fatalf("shipping Card arity = %d, want pre-cutover Props arity 1", got)
	}
	if got := module.targetImports[pagesDir]; len(got) != 1 || got[0] != uiDir || !module.targetImportedBy[uiDir][pagesDir] {
		t.Fatalf("exact target graph pages->ui = forward %v reverse %v", got, module.targetImportedBy[uiDir])
	}
}

func TestTargetDeclarationCacheInvalidatesToCurrentOverride(t *testing.T) {
	root, uiDir := writeTargetDeclarationTestModule(t)
	module, err := Open(Options{ModuleRoot: root, ModulePath: "example.com/app"})
	if err != nil {
		t.Fatal(err)
	}
	importer := newComponentTargetImporter(module, targetTestImporter())
	before, err := importer.Import("example.com/app/ui")
	if err != nil {
		t.Fatal(err)
	}
	again, err := importer.Import("example.com/app/ui")
	if err != nil {
		t.Fatal(err)
	}
	if again != before {
		t.Fatal("warm exact declaration lookup missed its cache")
	}

	module.SetOverride(filepath.Join(uiDir, "card.gsx"), []byte(`package ui

component Card(title string, count int, featured bool) { <div/> }
`))
	module.Invalidate(uiDir)
	after, err := importer.Import("example.com/app/ui")
	if err != nil {
		t.Fatal(err)
	}
	if after == before {
		t.Fatal("exact declaration package survived source invalidation")
	}
	if got := exactTargetSignature(t, after, "Card").Params().Len(); got != 3 {
		t.Fatalf("rebuilt Card arity = %d, want current override arity 3", got)
	}
}

func TestTargetInvalidationTraversesAlternatingAnalysisGraphs(t *testing.T) {
	module, err := Open(Options{ModuleRoot: t.TempDir(), ModulePath: "example.com/app"})
	if err != nil {
		t.Fatal(err)
	}
	leaf := filepath.Join(module.opts.ModuleRoot, "leaf")
	middle := filepath.Join(module.opts.ModuleRoot, "middle")
	top := filepath.Join(module.opts.ModuleRoot, "top")
	module.targetImportedBy[leaf] = map[string]bool{middle: true}
	module.importedBy[middle] = map[string]bool{top: true}
	module.pkgTypes = map[string]*types.Package{}
	for _, dir := range []string{leaf, middle, top} {
		module.pkgTypes[dir] = types.NewPackage("example.com/app/"+filepath.Base(dir), filepath.Base(dir))
		module.targetDeclTypes[dir] = types.NewPackage("example.com/app/target/"+filepath.Base(dir), filepath.Base(dir))
		module.pkgResults[dir] = &PackageResult{}
	}

	dependents := module.Dependents(leaf)
	if len(dependents) != 3 {
		t.Fatalf("alternating-graph dependents = %v, want leaf, middle, top", dependents)
	}
	module.Invalidate(leaf)
	for _, dir := range []string{leaf, middle, top} {
		if module.pkgTypes[dir] != nil || module.targetDeclTypes[dir] != nil || module.pkgResults[dir] != nil {
			t.Errorf("%s retained a cache across alternating-graph invalidation", dir)
		}
	}
}

func TestTargetDeclarationImporterRejectsCyclesWithoutCachingPartialPackages(t *testing.T) {
	root := t.TempDir()
	aDir := filepath.Join(root, "a")
	bDir := filepath.Join(root, "b")
	for _, dir := range []string{aDir, bDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeFile(t, root, "go.mod", "module example.com/app\n\ngo 1.26.1\n")
	writeFile(t, aDir, "a.gsx", `package a

import "example.com/app/b"
type A struct { B b.B }
component RenderA(value A) { <div/> }
`)
	writeFile(t, bDir, "b.gsx", `package b

import "example.com/app/a"
type B struct { A a.A }
component RenderB(value B) { <div/> }
`)
	module, err := Open(Options{ModuleRoot: root, ModulePath: "example.com/app"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = newComponentTargetImporter(module, targetTestImporter()).Import("example.com/app/a")
	if err == nil || !strings.Contains(err.Error(), "import cycle") {
		t.Fatalf("cycle error = %v, want import cycle", err)
	}
	if module.targetDeclTypes[aDir] != nil || module.targetDeclTypes[bDir] != nil {
		t.Fatalf("cycle published partial exact packages: a=%p b=%p", module.targetDeclTypes[aDir], module.targetDeclTypes[bDir])
	}
}

func TestTargetDeclarationCacheClearsWithFileSetAndKeepsPathGraph(t *testing.T) {
	root, uiDir := writeTargetDeclarationTestModule(t)
	module, err := Open(Options{ModuleRoot: root, ModulePath: "example.com/app"})
	if err != nil {
		t.Fatal(err)
	}
	before, err := newComponentTargetImporter(module, targetTestImporter()).Import("example.com/app/ui")
	if err != nil {
		t.Fatal(err)
	}
	dep := filepath.Join(root, "dep")
	module.targetImports[uiDir] = []string{dep}
	module.targetImportedBy[dep] = map[string]bool{uiDir: true}
	oldFset := module.fset

	module.rebuildFset()
	if module.fset == oldFset {
		t.Fatal("FileSet was not rebuilt")
	}
	if module.targetDeclTypes[uiDir] != nil {
		t.Fatal("position-bearing exact declaration package survived FileSet rebuild")
	}
	if len(module.targetImports[uiDir]) != 1 || !module.targetImportedBy[dep][uiDir] {
		t.Fatal("path-only exact target graph was cleared with FileSet")
	}
	after, err := newComponentTargetImporter(module, targetTestImporter()).Import("example.com/app/ui")
	if err != nil {
		t.Fatal(err)
	}
	if after == before {
		t.Fatal("FileSet rebuild reused the old exact package object")
	}
	object := after.Scope().Lookup("Card")
	if object == nil || module.fset.File(object.Pos()) == nil {
		t.Fatalf("rebuilt Card position is not owned by the new FileSet: %v", object)
	}
}

func TestBundleRejectsGoOnlyPackageTransitivelyImportingProjectGsxAcrossSemanticImporters(t *testing.T) {
	root := t.TempDir()
	uiDir := filepath.Join(root, "ui")
	bridgeDir := filepath.Join(root, "bridge")
	pagesDir := filepath.Join(root, "pages")
	fastDir := filepath.Join(root, "fast")
	determinismDir := filepath.Join(root, "determinism")
	targetDir := filepath.Join(root, "target")
	rendererDir := filepath.Join(root, "renderer")
	for _, dir := range []string{uiDir, bridgeDir, pagesDir, fastDir, determinismDir, targetDir, rendererDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeFile(t, uiDir, "card.gsx", "package ui\ncomponent Card(title string) { <span>{title}</span> }\n")
	writeFile(t, bridgeDir, "bridge.go", "package bridge\nimport \"example.com/app/ui\"\nvar Card = ui.Card\n")
	writeFile(t, pagesDir, "page.gsx", "package pages\nimport \"example.com/app/bridge\"\ncomponent Page() { <bridge.Card title=\"fresh\"/> }\n")
	writeFile(t, fastDir, "fast.gsx", "package fast\nimport _ \"example.com/app/bridge\"\ncomponent Fast() { <main/> }\n")
	writeFile(t, determinismDir, "z.gsx", "package determinism\nimport _ \"example.com/app/bridge\"\ncomponent Z() { <main/> }\n")
	writeFile(t, determinismDir, "a.gsx", "package determinism\nimport _ \"example.com/app/bridge\"\ncomponent A() { <main/> }\n")
	writeFile(t, targetDir, "target.gsx", "package target\nimport _ \"example.com/app/bridge\"\ncomponent Target() { <main/> }\n")
	writeFile(t, rendererDir, "renderer.gsx", "package renderer\nimport _ \"example.com/app/bridge\"\ncomponent Renderer() { <main/> }\n")

	imports := targetTestImporter().(mapImporter)
	ui := types.NewPackage("example.com/app/ui", "ui")
	propsObject := types.NewTypeName(token.NoPos, ui, "CardProps", nil)
	propsType := types.NewNamed(propsObject, types.NewStruct(
		[]*types.Var{types.NewField(token.NoPos, ui, "Poison", types.Typ[types.Int], false)},
		[]string{""},
	), nil)
	ui.Scope().Insert(propsObject)
	nodeType := imports["github.com/gsxhq/gsx"].Scope().Lookup("Node").Type()
	staleSignature := types.NewSignatureType(
		nil, nil, nil,
		types.NewTuple(types.NewParam(token.NoPos, ui, "props", propsType)),
		types.NewTuple(types.NewParam(token.NoPos, ui, "", nodeType)),
		false,
	)
	ui.Scope().Insert(types.NewFunc(token.NoPos, ui, "Card", staleSignature))
	ui.MarkComplete()
	bridge := types.NewPackage("example.com/app/bridge", "bridge")
	bridge.Scope().Insert(types.NewVar(token.NoPos, bridge, "Card", staleSignature))
	bridge.SetImports([]*types.Package{ui})
	bridge.MarkComplete()
	imports[ui.Path()] = ui
	imports[bridge.Path()] = bridge
	module, err := Open(Options{
		ModuleRoot: root,
		ModulePath: "example.com/app",
		Bundle:     testBundle(imports, funcTables{}),
	})
	if err != nil {
		t.Fatal(err)
	}

	for name, importPackage := range map[string]func() (*types.Package, error){
		"shipping": func() (*types.Package, error) {
			return (&moduleImporter{m: module, external: imports, seen: map[string]bool{}}).Import(bridge.Path())
		},
		"exact target": func() (*types.Package, error) {
			return newComponentTargetImporter(module, imports).Import(bridge.Path())
		},
		"renderer": func() (*types.Package, error) {
			return newSourceDeclResolver(module, imports).Import(bridge.Path())
		},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := importPackage(); err == nil || !strings.Contains(err.Error(), "normal module resolver") {
				t.Fatalf("Import bridge error = %v, want transitive project-GSX fail-closed guidance", err)
			}
		})
	}
	for name, analyzePackage := range map[string]func() error{
		"exact target package": func() error {
			_, err := newComponentTargetImporter(module, imports).Import("example.com/app/target")
			return err
		},
		"renderer package": func() error {
			_, err := newSourceDeclResolver(module, imports).packageForDir(rendererDir)
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			err := analyzePackage()
			diagnostics, ok := diagnosticsFromSourceError(err)
			if !ok || len(diagnostics) != 1 || diagnostics[0].Code != "bundle-project-gsx-transitive" {
				t.Fatalf("analysis error = %T %v diagnostics=%+v, want one structured Bundle transitive diagnostic", err, err, diagnostics)
			}
		})
	}

	output, diagnostics, err := module.Generate(pagesDir)
	if err != nil {
		t.Fatalf("Generate infrastructure error = %v, want positioned Bundle diagnostic", err)
	}
	if len(output) != 0 {
		t.Fatalf("Bundle transitive import emitted files %v", keysOfGenerated(output))
	}
	if len(diagnostics) != 1 || diagnostics[0].Code != "bundle-project-gsx-transitive" || !strings.HasSuffix(diagnostics[0].Start.Filename, "page.gsx") {
		t.Fatalf("diagnostics = %+v, want one positioned bundle-project-gsx-transitive error", diagnostics)
	}
	fastOutput, fastDiagnostics, err := module.Generate(fastDir)
	if err != nil || len(fastOutput) != 0 || len(fastDiagnostics) != 1 || fastDiagnostics[0].Code != "bundle-project-gsx-transitive" {
		t.Fatalf("all-unique/no-call-site fast path output=%v diagnostics=%+v error=%v", keysOfGenerated(fastOutput), fastDiagnostics, err)
	}
	_, orderedDiagnostics, err := module.Generate(determinismDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(orderedDiagnostics) != 2 || !strings.HasSuffix(orderedDiagnostics[0].Start.Filename, "a.gsx") || !strings.HasSuffix(orderedDiagnostics[1].Start.Filename, "z.gsx") {
		t.Fatalf("Bundle diagnostics order = %+v, want authored order within sorted a.gsx then z.gsx", orderedDiagnostics)
	}
	if got := module.externalLoads(); got != 0 {
		t.Fatalf("Bundle external loads = %d, want zero", got)
	}
}
