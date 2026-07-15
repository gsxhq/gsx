package codegen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func configuredSourceGraphModule(t *testing.T) (root, leafDir, filterDir, mergerDir, pageDir string) {
	t.Helper()
	root = t.TempDir()
	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, root, "go.mod", "module example.com/app\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	leafDir = filepath.Join(root, "leaf")
	filterDir = filepath.Join(root, "filters")
	mergerDir = filepath.Join(root, "merger")
	pageDir = filepath.Join(root, "page")
	for _, dir := range []string{leafDir, filterDir, mergerDir, pageDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeFile(t, leafDir, "leaf.gsx", `package leaf

import "context"

type FilterContext = context.Context
type Classes = []string

component Card(title string) { <span>{title}</span> }
`)
	writeFile(t, leafDir, "leaf.x.go", `package leaf

type FilterContext = string
type Classes = []byte
`)
	writeFile(t, filterDir, "filters.go", `package filters

import "example.com/app/leaf"

func Fresh(ctx leaf.FilterContext, value string) string { return value }
`)
	writeFile(t, mergerDir, "merger.go", `package merger

import "example.com/app/leaf"

func Merge(classes leaf.Classes) string { return "" }
`)
	writeFile(t, pageDir, "page.gsx", `package page

component Page(value string) { <p>{value |> fresh}</p> }
`)
	return root, leafDir, filterDir, mergerDir, pageDir
}

func TestLocalFilterUsesAuthoritativeSourceGraphAndInvalidatesWarm(t *testing.T) {
	root, leafDir, _, _, pageDir := configuredSourceGraphModule(t)
	m, err := Open(Options{
		ModuleRoot: root,
		ModulePath: "example.com/app",
		FilterPkgs: []string{"example.com/app/filters"},
	})
	if err != nil {
		t.Fatal(err)
	}
	output, diagnostics, err := m.Generate(pageDir)
	if err != nil {
		t.Fatal(err)
	}
	if hasDiagErrors(diagnostics) {
		t.Fatalf("cold diagnostics = %v, want authoritative filter signature", diagnostics)
	}
	if generated := string(output[filepath.Join(pageDir, "page.gsx")]); !strings.Contains(generated, ".Fresh(ctx, (value))") {
		t.Fatalf("cold output did not classify FilterContext as context.Context:\n%s", generated)
	}
	key := "example.com/app/page\x00" + strings.Join(dedupFilterPkgs([]string{"example.com/app/filters"}), "\x00")
	if !m.dirFuncTbls[key].filters["fresh"].wantsCtx {
		t.Fatal("cold filter table did not retain ctx-taking classification")
	}
	var watchesPage bool
	for _, dependent := range m.Dependents(leafDir) {
		watchesPage = watchesPage || dependent == pageDir
	}
	if !watchesPage {
		t.Fatalf("Dependents(%s) omitted configured filter consumer %s", leafDir, pageDir)
	}

	m.SetOverride(filepath.Join(leafDir, "leaf.gsx"), []byte(`package leaf

type FilterContext = string
type Classes = []string

component Card(title string) { <span>{title}</span> }
`))
	_, _, _ = m.Generate(pageDir)
	if m.dirFuncTbls[key].filters["fresh"].wantsCtx {
		t.Fatal("leaf edit retained the stale ctx-taking filter table through the Go-only package")
	}
	if got := m.externalLoads(); got != 1 {
		t.Fatalf("external loads after warm filter edit = %d, want one", got)
	}
}

func TestLocalFilterAliasUsesAuthoritativeSourceGraphAndInvalidatesWarm(t *testing.T) {
	root, leafDir, _, _, pageDir := configuredSourceGraphModule(t)
	m, err := Open(Options{
		ModuleRoot: root,
		ModulePath: "example.com/app",
		Aliases: []FilterAlias{{
			Name:     "fresh",
			PkgPath:  "example.com/app/filters",
			FuncName: "Fresh",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	output, diagnostics, err := m.Generate(pageDir)
	if err != nil {
		t.Fatal(err)
	}
	if hasDiagErrors(diagnostics) {
		t.Fatalf("cold diagnostics = %v, want authoritative alias signature", diagnostics)
	}
	if generated := string(output[filepath.Join(pageDir, "page.gsx")]); !strings.Contains(generated, ".Fresh(ctx, (value))") {
		t.Fatalf("cold output did not classify aliased FilterContext as context.Context:\n%s", generated)
	}

	m.SetOverride(filepath.Join(leafDir, "leaf.gsx"), []byte(`package leaf

type FilterContext = string
type Classes = []string

component Card(title string) { <span>{title}</span> }
`))
	_, _, _ = m.Generate(pageDir)
	key := "example.com/app/page\x00" + strings.Join(dedupFilterPkgs(nil), "\x00")
	if m.dirFuncTbls[key].filters["fresh"].wantsCtx {
		t.Fatal("leaf edit retained stale ctx classification for explicit alias")
	}
	if got := m.externalLoads(); got != 1 {
		t.Fatalf("external loads after warm alias edit = %d, want one", got)
	}
}

func TestValidateClassMergerUsesAuthoritativeSourceGraph(t *testing.T) {
	root, _, _, _, _ := configuredSourceGraphModule(t)
	err := ValidateClassMerger(root, &ClassMergerRef{PkgPath: "example.com/app/merger", FuncName: "Merge"})
	if err != nil {
		t.Fatalf("ValidateClassMerger rejected current GSX-backed signature: %v", err)
	}
}

func TestModuleClassMergerUsesAuthoritativeSourceGraphAndInvalidatesWarm(t *testing.T) {
	root, leafDir, _, _, pageDir := configuredSourceGraphModule(t)
	writeFile(t, pageDir, "page.gsx", "package page\n\ncomponent Page() { <p class=\"x\"/> }\n")
	m, err := Open(Options{
		ModuleRoot:  root,
		ModulePath:  "example.com/app",
		ClassMerger: &ClassMergerRef{PkgPath: "example.com/app/merger", FuncName: "Merge"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, diagnostics, err := m.Generate(pageDir); err != nil || hasDiagErrors(diagnostics) {
		t.Fatalf("cold Generate error=%v diagnostics=%v", err, diagnostics)
	}
	m.SetOverride(filepath.Join(leafDir, "leaf.gsx"), []byte(`package leaf

type FilterContext = string
type Classes = []byte

component Card(title string) { <span>{title}</span> }
`))
	if _, _, err := m.Generate(pageDir); err == nil || !strings.Contains(err.Error(), "func([]string) string") {
		t.Fatalf("warm Generate merger error = %v, want current invalid signature", err)
	}
	if got := m.externalLoads(); got != 1 {
		t.Fatalf("external loads after warm merger edit = %d, want one", got)
	}
}

func TestGenerateDirsValidatesModuleClassMergerFromAuthoritativeSource(t *testing.T) {
	root, _, _, _, pageDir := configuredSourceGraphModule(t)
	writeFile(t, pageDir, "page.gsx", "package page\n\ncomponent Page() { <p class=\"x\"/> }\n")
	result, err := GenerateDirs(root, []string{pageDir}, Options{
		ClassMerger: &ClassMergerRef{PkgPath: "example.com/app/merger", FuncName: "Merge"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if hasDiagErrors(result[pageDir].Diags) {
		t.Fatalf("GenerateDirs diagnostics = %v", result[pageDir].Diags)
	}
}

func TestConfiguredLocalGsxProviderUsesOnlyActiveCompanionSyntax(t *testing.T) {
	root := t.TempDir()
	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, root, "go.mod", "module example.com/app\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	filterDir := filepath.Join(root, "filters")
	pageDir := filepath.Join(root, "page")
	writeFile(t, filterDir, "filters.gsx", `package filters

import "context"

func Fresh(ctx context.Context, value string) string { return value }

component Probe() { <Helper/> }
`)
	writeFile(t, filterDir, "active.go", `package filters

import "github.com/gsxhq/gsx"

type HelperProps struct{}

func Helper(HelperProps) gsx.Node { return nil }
`)
	writeFile(t, filterDir, "excluded.go", `//go:build never_active_gsx_test

package filters

import "github.com/gsxhq/gsx"

func Helper() gsx.Node { return nil }
`)
	writeFile(t, pageDir, "page.gsx", `package page

component Page(value string) { <p>{value |> fresh}</p> }
`)
	m, err := Open(Options{
		ModuleRoot: root,
		ModulePath: "example.com/app",
		FilterPkgs: []string{"example.com/app/filters"},
	})
	if err != nil {
		t.Fatal(err)
	}
	output, diagnostics, err := m.Generate(pageDir)
	if err != nil {
		t.Fatal(err)
	}
	if hasDiagErrors(diagnostics) {
		t.Fatalf("active-companion diagnostics = %v", diagnostics)
	}
	if generated := string(output[filepath.Join(pageDir, "page.gsx")]); !strings.Contains(generated, ".Fresh(ctx, (value))") {
		t.Fatalf("configured provider was not harvested from active source:\n%s", generated)
	}
}

func TestConfiguredLocalGsxFilterRejectsBrokenGoBody(t *testing.T) {
	root := t.TempDir()
	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, root, "go.mod", "module example.com/app\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	filterDir := filepath.Join(root, "filters")
	pageDir := filepath.Join(root, "page")
	writeFile(t, filterDir, "filters.gsx", `package filters

func Fresh(value string) string { return missing(value) }
`)
	writeFile(t, pageDir, "page.gsx", `package page

component Page(value string) { <p>{value |> fresh}</p> }
`)
	m, err := Open(Options{
		ModuleRoot: root,
		ModulePath: "example.com/app",
		FilterPkgs: []string{"example.com/app/filters"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := m.Generate(pageDir); err == nil || !strings.Contains(err.Error(), "undefined: missing") {
		t.Fatalf("Generate error = %v, want configured GSX function body failure", err)
	}
}
