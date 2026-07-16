package gen

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/gsxhq/gsx/internal/attrclass"
	"github.com/gsxhq/gsx/internal/codegen"
)

func TestLSPSemanticConfigIdentityTracksOnlyEffectiveModuleSemantics(t *testing.T) {
	base := config{
		filterPkgs: []string{"example.com/x/filters"},
		aliases: []codegen.FilterAlias{{
			Name: "shout", PkgPath: "example.com/x/filter", FuncName: "Shout",
		}},
		renderers: []codegen.RendererAlias{
			{TypeKey: "example.com/x.T", PkgPath: "example.com/x/render", FuncName: "Old"},
			{TypeKey: "example.com/x.T", PkgPath: "example.com/x/render", FuncName: "Current"},
		},
		classMerger: &codegen.ClassMergerRef{PkgPath: "example.com/x/merge", FuncName: "Keep"},
	}
	baseID := lspSemanticConfigIdentity(base)

	formatOnly := base
	formatOnly.printWidth = 132
	formatOnly.tabWidth = 8
	if got := lspSemanticConfigIdentity(formatOnly); got != baseID {
		t.Fatal("formatter-only settings changed Module semantic identity")
	}

	lastWinsEquivalent := base
	lastWinsEquivalent.renderers = []codegen.RendererAlias{
		{TypeKey: "example.com/x.T", PkgPath: "example.com/x/render", FuncName: "Current"},
	}
	if got := lspSemanticConfigIdentity(lastWinsEquivalent); got != baseID {
		t.Fatal("equivalent last-wins renderer table changed Module semantic identity")
	}

	effectiveRendererChanged := base
	effectiveRendererChanged.renderers = []codegen.RendererAlias{
		{TypeKey: "example.com/x.Other", PkgPath: "example.com/x/render", FuncName: "UnusedOrder"},
		{TypeKey: "example.com/x.T", PkgPath: "example.com/x/render", FuncName: "Current"},
	}
	// The extra effective key is semantic and must change the identity.
	if got := lspSemanticConfigIdentity(effectiveRendererChanged); got == baseID {
		t.Fatal("effective renderer table change did not change Module semantic identity")
	}

	aliasChanged := base
	aliasChanged.aliases = append([]codegen.FilterAlias(nil), base.aliases...)
	aliasChanged.aliases[0].FuncName = "Other"
	if got := lspSemanticConfigIdentity(aliasChanged); got == baseID {
		t.Fatal("alias target change did not change Module semantic identity")
	}

	classifierChanged := base
	classifierChanged.urlRules = []attrclass.Rule{{Name: "hx-get"}}
	if got := lspSemanticConfigIdentity(classifierChanged); got == baseID {
		t.Fatal("classifier rule change did not change Module semantic identity")
	}

	mergerChanged := base
	mergerChanged.classMerger = &codegen.ClassMergerRef{PkgPath: "example.com/x/merge", FuncName: "Other"}
	if got := lspSemanticConfigIdentity(mergerChanged); got == baseID {
		t.Fatal("ClassMerger change did not change Module semantic identity")
	}
}

func TestLSPConfigReloadReplacesModuleAndReplaysOpenBuffers(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping module-resolution test in short mode")
	}
	dir, must := lspFilterModule(t)
	firstDir := filepath.Join(dir, "first")
	secondDir := filepath.Join(dir, "second")
	firstPath := filepath.Join(firstDir, "first.gsx")
	secondPath := filepath.Join(secondDir, "second.gsx")
	must("first/first.gsx", "package first\n\ncomponent First(name string) { <p>{name}</p> }\n")
	must("second/second.gsx", "package second\n\ncomponent Disk() { <p>disk</p> }\n")
	firstBuffer := []byte("package first\n\ncomponent First(name string) { <p>{name |> shout}</p> }\n")
	secondBuffer := []byte("package second\n\ncomponent Buffered() { <p>buffer</p> }\n")

	a := newLSPAnalyzer(config{}, nil)
	if _, err := a.SetOverride(firstPath, firstBuffer); err != nil {
		t.Fatal(err)
	}
	if _, err := a.SetOverride(secondPath, secondBuffer); err != nil {
		t.Fatal(err)
	}
	before, err := a.Analyze(firstDir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !hasUnknownFilter(before, "shout") {
		t.Fatalf("precondition: shout unexpectedly resolved before config edit: %v", before.Diags)
	}
	oldModule := lspModuleForRoot(a, dir)

	must("gsx.toml", "[filters]\nshout = \"example.com/x/myf.Shout\"\n")
	affected, err := a.SetOverride(firstPath, firstBuffer)
	if err != nil {
		t.Fatal(err)
	}
	wantAffected := []string{firstDir, secondDir}
	slices.Sort(wantAffected)
	if !reflect.DeepEqual(affected, wantAffected) {
		t.Fatalf("config transition affected = %v, want every open package %v", affected, wantAffected)
	}
	newModule := lspModuleForRoot(a, dir)
	if newModule == oldModule {
		t.Fatal("semantic config edit reused the old codegen.Module")
	}
	if owner := lspOverrideModule(a, secondPath); owner != newModule {
		t.Fatal("semantic config edit did not replay the other open buffer into the new Module")
	}

	after, err := a.Analyze(firstDir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if hasUnknownFilter(after, "shout") {
		t.Fatalf("new alias was not applied after live config reload: %v", after.Diags)
	}
	second, err := a.Analyze(secondDir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if second.Types == nil || second.Types.Scope().Lookup("Buffered") == nil {
		t.Fatal("replayed second buffer was not authoritative after config reload")
	}

	if got, clearErr := oldModule.ClearOverride(firstPath); got != nil || clearErr != nil {
		t.Fatalf("old Module retained first buffer authority: %v, %v", got, clearErr)
	}
	if got, clearErr := oldModule.ClearOverride(secondPath); got != nil || clearErr != nil {
		t.Fatalf("old Module retained second buffer authority: %v, %v", got, clearErr)
	}
}

func TestLSPConfigReloadChangesClassMergerGeneration(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping module-resolution test in short mode")
	}
	dir, must := lspFilterModule(t)
	must("merge/merge.go", "package merge\n\nfunc KeepA(values []string) string { return \"\" }\nfunc KeepB(values []string) string { return \"\" }\n")
	source := []byte("package x\n\nimport \"github.com/gsxhq/gsx\"\n\ncomponent Card(attrs gsx.Attrs) { <div class=\"card\" { attrs... }/> }\n")
	path := filepath.Join(dir, "card.gsx")
	if err := os.WriteFile(path, source, 0o644); err != nil {
		t.Fatal(err)
	}
	must("gsx.toml", "class_merger = \"example.com/x/merge.KeepA\"\n")

	a := newLSPAnalyzer(config{}, nil)
	if _, err := a.SetOverride(path, source); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Analyze(dir, nil); err != nil {
		t.Fatal(err)
	}
	oldModule := concreteLSPModuleForRoot(a, dir)
	if got := generatedForLSPConfigTest(t, oldModule, dir); !strings.Contains(got, "_gsxcm.KeepA") {
		t.Fatalf("initial generation did not use KeepA:\n%s", got)
	}

	must("gsx.toml", "class_merger = \"example.com/x/merge.KeepB\"\n")
	affected, err := a.SetOverride(path, source)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(affected, []string{dir}) {
		t.Fatalf("ClassMerger-only transition affected = %v, want [%s]", affected, dir)
	}
	newModule := concreteLSPModuleForRoot(a, dir)
	if newModule == oldModule {
		t.Fatal("ClassMerger-only edit reused the old codegen.Module")
	}
	if got := generatedForLSPConfigTest(t, newModule, dir); !strings.Contains(got, "_gsxcm.KeepB") || strings.Contains(got, "_gsxcm.KeepA") {
		t.Fatalf("generation after reload did not exclusively use KeepB:\n%s", got)
	}
}

func TestLSPConfigReloadChangesAliasAndRendererGeneration(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping module-resolution test in short mode")
	}
	dir, must := lspFilterModule(t)
	must("myf/myf.go", `package myf

type Label string

func ShoutA(value string) string { return value }
func ShoutB(value string) string { return value }
func RenderA(value Label) string { return string(value) }
func RenderB(value Label) string { return string(value) }
`)
	source := []byte(`package x

import "example.com/x/myf"

component Card(name string, label myf.Label) {
	<p>{name |> shout}{label}</p>
}
`)
	path := filepath.Join(dir, "card.gsx")
	if err := os.WriteFile(path, source, 0o644); err != nil {
		t.Fatal(err)
	}
	writeConfig := func(filter, renderer string) {
		t.Helper()
		must("gsx.toml", "[filters]\nshout = \"example.com/x/myf."+filter+"\"\n\n[renderers]\n\"example.com/x/myf.Label\" = \"example.com/x/myf."+renderer+"\"\n")
	}
	writeConfig("ShoutA", "RenderA")

	a := newLSPAnalyzer(config{}, nil)
	if _, err := a.SetOverride(path, source); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Analyze(dir, nil); err != nil {
		t.Fatal(err)
	}
	before := concreteLSPModuleForRoot(a, dir)
	generatedA := generatedForLSPConfigTest(t, before, dir)
	if !strings.Contains(generatedA, ".ShoutA") || !strings.Contains(generatedA, ".RenderA") {
		t.Fatalf("initial generation did not use alias/renderer A:\n%s", generatedA)
	}

	writeConfig("ShoutB", "RenderB")
	affected, err := a.SetOverride(path, source)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(affected, []string{dir}) {
		t.Fatalf("alias/renderer transition affected = %v, want [%s]", affected, dir)
	}
	after := concreteLSPModuleForRoot(a, dir)
	if after == before {
		t.Fatal("alias/renderer edit reused the old codegen.Module")
	}
	generatedB := generatedForLSPConfigTest(t, after, dir)
	if !strings.Contains(generatedB, ".ShoutB") || !strings.Contains(generatedB, ".RenderB") ||
		strings.Contains(generatedB, ".ShoutA") || strings.Contains(generatedB, ".RenderA") {
		t.Fatalf("generation after reload did not exclusively use alias/renderer B:\n%s", generatedB)
	}
}

func concreteLSPModuleForRoot(a lspAnalyzer, root string) *codegen.Module {
	a.mods.mu.Lock()
	defer a.mods.mu.Unlock()
	return a.mods.byRoot[filepath.Clean(root)]
}

func generatedForLSPConfigTest(t *testing.T, module *codegen.Module, dir string) string {
	t.Helper()
	if module == nil {
		t.Fatal("no cached codegen.Module")
	}
	files, diagnostics, err := module.Generate(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, diagnostic := range diagnostics {
		if diagnostic.Severity.String() == "error" {
			t.Fatalf("generation diagnostics: %v", diagnostics)
		}
	}
	var generated string
	for _, source := range files {
		generated += string(source)
	}
	if generated == "" {
		t.Fatal("generation produced no files")
	}
	return generated
}
