package codegen

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"github.com/gsxhq/gsx/internal/sourceview"
)

func writeDiskRefreshTestModule(t *testing.T) (root, pageDir, pagePath string) {
	t.Helper()
	root = t.TempDir()
	pageDir = filepath.Join(root, "page")
	depRoot := filepath.Join(root, "dep")
	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, root, "go.mod", "module example.com/app\n\ngo 1.26.1\n\nrequire (\n\tgithub.com/gsxhq/gsx v0.0.0\n\texample.com/dep v0.0.0\n)\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\nreplace example.com/dep => ./dep\n")
	writeFile(t, depRoot, "go.mod", "module example.com/dep\n\ngo 1.26.1\n")
	writeFile(t, depRoot, "dep.go", "package dep\n\ntype Value string\n")
	pagePath = filepath.Join(pageDir, "page.gsx")
	writeFile(t, pageDir, "page.gsx", "package page\n\ncomponent Page(value string) { <p>{value}</p> }\n")
	return root, pageDir, pagePath
}

func TestRefreshDiskSourcesReloadsPreviouslyUnseenImport(t *testing.T) {
	root, pageDir, pagePath := writeDiskRefreshTestModule(t)
	module, err := Open(Options{ModuleRoot: root, ModulePath: "example.com/app"})
	if err != nil {
		t.Fatal(err)
	}
	if _, diagnostics, err := module.Generate(pageDir); err != nil || len(diagnostics) != 0 {
		t.Fatalf("cold Generate error=%v diagnostics=%v", err, diagnostics)
	}
	if module.externalImportPaths["example.com/dep"] {
		t.Fatal("future import unexpectedly present in cold importer")
	}

	updated := []byte(`package page

import "example.com/dep"

component Page(value dep.Value) { <p>{value}</p> }
`)
	if err := os.WriteFile(pagePath, updated, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := module.RefreshDiskSources(pageDir); err != nil {
		t.Fatal(err)
	}
	module.Invalidate(pageDir)
	output, diagnostics, err := module.Generate(pageDir)
	if err != nil || len(diagnostics) != 0 {
		t.Fatalf("warm Generate error=%v diagnostics=%v", err, diagnostics)
	}
	if !bytes.Contains(output[pagePath], []byte("dep.Value")) {
		t.Fatalf("generated output did not use refreshed import:\n%s", output[pagePath])
	}
	if got := module.externalLoads(); got != 2 {
		t.Fatalf("external loads = %d, want cold load plus required manifest reload", got)
	}
}

func TestRefreshDiskSourcesKeepsBodyOnlyEditWarm(t *testing.T) {
	root, pageDir, pagePath := writeDiskRefreshTestModule(t)
	module, err := Open(Options{ModuleRoot: root, ModulePath: "example.com/app"})
	if err != nil {
		t.Fatal(err)
	}
	if _, diagnostics, err := module.Generate(pageDir); err != nil || len(diagnostics) != 0 {
		t.Fatalf("cold Generate error=%v diagnostics=%v", err, diagnostics)
	}
	if err := os.WriteFile(pagePath, []byte("package page\n\ncomponent Page(value string) { <strong>{value}</strong> }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := module.RefreshDiskSources(pageDir); err != nil {
		t.Fatal(err)
	}
	module.Invalidate(pageDir)
	output, diagnostics, err := module.Generate(pageDir)
	if err != nil || len(diagnostics) != 0 {
		t.Fatalf("warm Generate error=%v diagnostics=%v", err, diagnostics)
	}
	if !bytes.Contains(output[pagePath], []byte("strong")) {
		t.Fatalf("generated output did not observe body edit:\n%s", output[pagePath])
	}
	if got := module.externalLoads(); got != 1 {
		t.Fatalf("external loads = %d, want body-only edit to stay warm", got)
	}
}

func TestRefreshDiskSourcesAndInvalidateIsOneExactAnalysisTransition(t *testing.T) {
	m, root := setupChainModule(t)
	utilDir := filepath.Join(root, "util")
	pagesDir := filepath.Join(root, "pages")
	soloDir := filepath.Join(root, "solo")
	if _, err := m.Package(pagesDir); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Package(soloDir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(utilDir, "util.gsx"), []byte("package util\ncomponent Y(label string) { <strong>{label}</strong> }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	affected, _, err := m.RefreshDiskSourcesAndInvalidate(utilDir)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{utilDir, filepath.Join(root, "components"), pagesDir}
	slices.Sort(want)
	if !reflect.DeepEqual(affected, want) {
		t.Fatalf("affected = %v, want exact reverse closure %v", affected, want)
	}
	if got := m.cachedDirs(); !reflect.DeepEqual(got, []string{soloDir}) {
		t.Fatalf("cached dirs after atomic refresh = %v, want unrelated package only", got)
	}
}

// TestRefreshGoSourcesAndInvalidateWarmTransitions drives every warm
// authored-Go transition through ONE shared module (a codegen.Module open is
// the unit of test cost — see CLAUDE.md "Test performance"):
//
//  1. exact pre-change reverse-closure projection through a Go-only bridge;
//  2. an existing-active-file signature edit refreshing retained syntax with
//     zero reloads, observable through fresh diagnostics;
//  3. the save-all ordering hole: a .gsx-path RefreshDiskSources that commits
//     new helper-Go bytes must refresh retained syntax itself, and the later
//     Go-path event must stay a safe no-op;
//  4. a metadata-error baseline that must NOT be repaired warm;
//  5. creating a package the cold inventory has never seen repairs a GSX
//     package poisoned by importing it.
func TestRefreshGoSourcesAndInvalidateWarmTransitions(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "go.mod", "module example.com/app\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot(t)+"\n")
	writeFile(t, root, "model/model.go", "package model\n\ntype Label string\n")
	writeFile(t, root, "bridge/bridge.go", "package bridge\n\nimport \"example.com/app/model\"\n\ntype Label = model.Label\n")
	writeFile(t, root, "blog/page.gsx", "package blog\n\nimport \"example.com/app/bridge\"\n\ncomponent Page(label bridge.Label) { <p>{label}</p> }\n")
	writeFile(t, root, "site/home.gsx", "package site\n\nimport (\n\t\"example.com/app/blog\"\n\t\"example.com/app/bridge\"\n)\n\ncomponent Home(label bridge.Label) { <blog.Page label={label}/> }\n")
	writeFile(t, root, "unrelated/aside.gsx", "package unrelated\n\ncomponent Aside() { <aside>unrelated</aside> }\n")

	module, err := Open(Options{ModuleRoot: root, ModulePath: "example.com/app"})
	if err != nil {
		t.Fatal(err)
	}
	blogDir := filepath.Join(root, "blog")
	siteDir := filepath.Join(root, "site")
	unrelatedDir := filepath.Join(root, "unrelated")
	modelDir := filepath.Join(root, "model")
	bridgeDir := filepath.Join(root, "bridge")
	bridgePath := filepath.Join(bridgeDir, "bridge.go")
	bridgeOriginal := "package bridge\n\nimport \"example.com/app/model\"\n\ntype Label = model.Label\n"
	bridgeRenamed := "package bridge\n\nimport \"example.com/app/model\"\n\ntype Renamed = model.Label\n"
	for _, dir := range []string{siteDir, unrelatedDir} {
		if _, diagnostics, err := module.Generate(dir); err != nil || len(diagnostics) != 0 {
			t.Fatalf("cold Generate(%s) error=%v diagnostics=%v", dir, err, diagnostics)
		}
	}
	loads := module.externalLoads()
	generateClean := func(dirs ...string) {
		t.Helper()
		for _, dir := range dirs {
			if _, diagnostics, err := module.Generate(dir); err != nil || len(diagnostics) != 0 {
				t.Fatalf("Generate(%s) error=%v diagnostics=%v", dir, err, diagnostics)
			}
		}
	}
	wantLoads := func(context string) {
		t.Helper()
		if got := module.externalLoads(); got != loads {
			t.Fatalf("external loads after %s = %d, want retained %d", context, got, loads)
		}
	}

	// Act 1 — a transitive Go edit projects the pre-change closure through the
	// Go-only bridge package to exactly blog and site.
	if err := os.WriteFile(filepath.Join(modelDir, "model.go"), []byte("package model\n\ntype Label int\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	affected, verdict, err := module.RefreshGoSourcesAndInvalidate(modelDir)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{blogDir, siteDir}
	if !slices.Equal(affected, want) {
		t.Fatalf("affected = %v, want exact GSX projection %v", affected, want)
	}
	if verdict.WorldReloadPending || verdict.Describe() != "" {
		t.Fatalf("transitive Go edit verdict = %+v, want warm (fast-path syntax swap)", verdict)
	}
	if got := module.cachedDirs(); !slices.Equal(got, []string{unrelatedDir}) {
		t.Fatalf("cached dirs after Go refresh = %v, want unrelated GSX package only", got)
	}
	generateClean(affected...)
	wantLoads("transitive Go edit")

	// Act 2 — an existing-active-file edit swaps retained syntax in place: the
	// removed alias must surface as fresh diagnostics with zero reloads.
	if err := os.WriteFile(bridgePath, []byte(bridgeRenamed), 0o644); err != nil {
		t.Fatal(err)
	}
	affected, verdict, err = module.RefreshGoSourcesAndInvalidate(bridgeDir)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(affected, want) {
		t.Fatalf("affected after bridge edit = %v, want %v", affected, want)
	}
	if verdict.WorldReloadPending {
		t.Fatalf("bridge signature edit verdict = %+v, want warm", verdict)
	}
	if _, diagnostics, err := module.Generate(blogDir); err != nil || len(diagnostics) == 0 {
		t.Fatalf("warm Generate did not observe refreshed Go signature: error=%v diagnostics=%v", err, diagnostics)
	}
	wantLoads("existing-active-file edit")
	if err := os.WriteFile(bridgePath, []byte(bridgeOriginal), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := module.RefreshGoSourcesAndInvalidate(bridgeDir); err != nil {
		t.Fatal(err)
	}
	generateClean(blogDir, siteDir)
	wantLoads("restoring bridge alias")

	// Act 3 — a .gsx-path directory refresh (regenDir's exact sequence) that
	// commits new helper-Go bytes must not leave retained syntax stale: the
	// rename must be observable immediately, and the later Go-path event for
	// the same bytes must remain a warm no-op with the same projection.
	if err := os.WriteFile(bridgePath, []byte(bridgeRenamed), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := module.RefreshDiskSources(bridgeDir); err != nil {
		t.Fatal(err)
	}
	module.Invalidate(bridgeDir)
	if _, diagnostics, err := module.Generate(blogDir); err != nil || len(diagnostics) == 0 {
		t.Fatalf("gsx-path refresh left retained bridge syntax stale: error=%v diagnostics=%v", err, diagnostics)
	}
	affected, _, err = module.RefreshGoSourcesAndInvalidate(bridgeDir)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(affected, want) {
		t.Fatalf("affected after already-committed Go edit = %v, want %v", affected, want)
	}
	if _, diagnostics, err := module.Generate(blogDir); err != nil || len(diagnostics) == 0 {
		t.Fatalf("Go-path no-op after committed refresh regressed diagnostics: error=%v diagnostics=%v", err, diagnostics)
	}
	wantLoads("gsx-path helper commit")
	if err := os.WriteFile(bridgePath, []byte(bridgeOriginal), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := module.RefreshGoSourcesAndInvalidate(bridgeDir); err != nil {
		t.Fatal(err)
	}
	generateClean(blogDir, siteDir)
	wantLoads("restoring bridge alias again")

	// Act 4 — a syntax-broken authored-Go save publishes an authoritative
	// package carrying metadata errors. Repair must force another cmd/go reload;
	// retaining that broken package and replacing only its syntax would leave the
	// old metadata errors attached forever.
	if err := os.WriteFile(bridgePath, []byte("package bridge\n\nfunc Broken("), 0o644); err != nil {
		t.Fatal(err)
	}
	_, verdict, err = module.RefreshGoSourcesAndInvalidate(bridgeDir)
	if err != nil {
		t.Fatal(err)
	}
	if !verdict.WorldReloadPending || verdict.Reason != sourceview.ReloadGoSource {
		t.Fatalf("unparsable Go save verdict = %+v, want a pending ReloadGoSource", verdict)
	}
	if _, diagnostics, err := module.Generate(blogDir); err == nil && len(diagnostics) == 0 {
		t.Fatal("syntax-broken Go save unexpectedly generated cleanly")
	}
	if got := module.externalLoads(); got != loads+1 {
		t.Fatalf("external loads after broken Go save = %d, want authoritative reload %d", got, loads+1)
	}
	if err := os.WriteFile(bridgePath, []byte(bridgeOriginal), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := module.RefreshGoSourcesAndInvalidate(bridgeDir); err != nil {
		t.Fatal(err)
	}
	generateClean(blogDir, siteDir)
	if got := module.externalLoads(); got != loads+2 {
		t.Fatalf("external loads after repairing broken Go save = %d, want fresh authoritative reload %d", got, loads+2)
	}
	loads += 2

	// Act 5 — creating a Go package the cold inventory has never seen cannot be
	// projected through the retained graph (the poisoned importer recorded no
	// edge to it), so the conservative affected set is every GSX dir, and the
	// scheduled authoritative reload repairs the poisoned package.
	repairDir := filepath.Join(root, "repair")
	writeFile(t, root, "repair/fix.gsx", "package repair\n\nimport \"example.com/app/svc\"\n\ncomponent Fix() { <p>{svc.Text()}</p> }\n")
	if err := module.RefreshDiskSources(repairDir); err != nil {
		t.Fatal(err)
	}
	module.Invalidate(repairDir)
	if _, diagnostics, err := module.Generate(repairDir); err != nil || len(diagnostics) == 0 {
		t.Fatalf("Generate of package importing a missing package: error=%v diagnostics=%v", err, diagnostics)
	}
	loads = module.externalLoads() // new-package membership reload consumed above
	svcDir := filepath.Join(root, "svc")
	writeFile(t, root, "svc/svc.go", "package svc\n\nfunc Text() string { return \"fixed\" }\n")
	affected, _, err = module.RefreshGoSourcesAndInvalidate(svcDir)
	if err != nil {
		t.Fatal(err)
	}
	wantAll := []string{blogDir, repairDir, siteDir, unrelatedDir}
	if !slices.Equal(affected, wantAll) {
		t.Fatalf("affected after new-package creation = %v, want every GSX dir %v", affected, wantAll)
	}
	if _, diagnostics, err := module.Generate(repairDir); err != nil || len(diagnostics) != 0 {
		t.Fatalf("new package did not repair poisoned importer: error=%v diagnostics=%v", err, diagnostics)
	}
	if got := module.externalLoads(); got != loads+1 {
		t.Fatalf("external loads after new-package creation = %d, want one authoritative reload (%d)", got, loads+1)
	}
}

// TestRefreshGoSourcesAndInvalidateBeforeInventoryReturnsClosure pins the
// pre-inventory fallback: with no published cold inventory there is no
// authoritative GSX classification to project through, so the unfiltered
// closure (here the seed itself) is returned rather than an empty projection
// that would silently commit the invalidation away. Deliberately no Generate:
// this path must not require a packages.Load.
func TestRefreshGoSourcesAndInvalidateBeforeInventoryReturnsClosure(t *testing.T) {
	root, _, _ := writeDiskRefreshTestModule(t)
	utilDir := filepath.Join(root, "util")
	writeFile(t, root, "util/util.go", "package util\n\nfunc Label() string { return \"x\" }\n")
	module, err := Open(Options{ModuleRoot: root, ModulePath: "example.com/app"})
	if err != nil {
		t.Fatal(err)
	}
	affected, _, err := module.RefreshGoSourcesAndInvalidate(utilDir)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(affected, []string{utilDir}) {
		t.Fatalf("pre-inventory affected = %v, want unfiltered closure [%s]", affected, utilDir)
	}
}

func TestGoSourceFastPathRejectsBuildConstraintPlacementChanges(t *testing.T) {
	ignoredLegacyConstraint := []byte("// +build linux\npackage probe\n")
	activeLegacyConstraint := []byte("// +build linux\n\npackage probe\n")
	if equalBuildConstraints(ignoredLegacyConstraint, activeLegacyConstraint) ||
		equalBuildConstraints(activeLegacyConstraint, ignoredLegacyConstraint) {
		t.Fatal("build-constrained files must use cmd/go selection even when directive text is unchanged")
	}
	// go/build recognizes both of these as effective constraints: the directive
	// after a /* */ block whose text mentions "package", and the legacy form
	// with tab separators. A line scan that stops at "package " or matches only
	// the literal "// +build" treats them as unconstrained and keeps the warm
	// fast path for files cmd/go may deselect.
	unconstrained := []byte("package probe\n")
	for name, constrained := range map[string][]byte{
		"directive after block comment": []byte("/*\npackage probe helpers\n*/\n//go:build windows\n\npackage probe\n"),
		"tab-separated legacy":          []byte("//\t+build linux\n\npackage probe\n"),
	} {
		if equalBuildConstraints(constrained, constrained) ||
			equalBuildConstraints(unconstrained, constrained) ||
			equalBuildConstraints(constrained, unconstrained) {
			t.Fatalf("%s: constrained file transition must use cmd/go selection", name)
		}
	}
	if !equalBuildConstraints(unconstrained, []byte("package probe\n\nfunc probe() {}\n")) {
		t.Fatal("unconstrained files on both sides must stay on the warm fast path")
	}
}

func TestRefreshDiskSourcesRebuildsNewPackageAndPackageClause(t *testing.T) {
	t.Run("new package", func(t *testing.T) {
		root, pageDir, _ := writeDiskRefreshTestModule(t)
		module, err := Open(Options{ModuleRoot: root, ModulePath: "example.com/app"})
		if err != nil {
			t.Fatal(err)
		}
		if _, diagnostics, err := module.Generate(pageDir); err != nil || len(diagnostics) != 0 {
			t.Fatalf("cold Generate error=%v diagnostics=%v", err, diagnostics)
		}
		widgetDir := filepath.Join(root, "widget")
		widgetPath := filepath.Join(widgetDir, "card.gsx")
		writeFile(t, widgetDir, "card.gsx", "package widget\n\ncomponent Card(label string) { <p>{label}</p> }\n")
		if err := module.RefreshDiskSources(widgetDir); err != nil {
			t.Fatal(err)
		}
		module.Invalidate(widgetDir)
		output, diagnostics, err := module.Generate(widgetDir)
		if err != nil || len(diagnostics) != 0 || len(output[widgetPath]) == 0 {
			t.Fatalf("new-package Generate output=%v error=%v diagnostics=%v", output, err, diagnostics)
		}
		if got := module.externalLoads(); got != 2 {
			t.Fatalf("external loads = %d, want new-package manifest reload", got)
		}
	})

	t.Run("package clause", func(t *testing.T) {
		root, pageDir, pagePath := writeDiskRefreshTestModule(t)
		module, err := Open(Options{ModuleRoot: root, ModulePath: "example.com/app"})
		if err != nil {
			t.Fatal(err)
		}
		if _, diagnostics, err := module.Generate(pageDir); err != nil || len(diagnostics) != 0 {
			t.Fatalf("cold Generate error=%v diagnostics=%v", err, diagnostics)
		}
		if err := os.WriteFile(pagePath, []byte("package renamed\n\ncomponent Page(value string) { <p>{value}</p> }\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := module.RefreshDiskSources(pageDir); err != nil {
			t.Fatal(err)
		}
		module.Invalidate(pageDir)
		output, diagnostics, err := module.Generate(pageDir)
		if err != nil || len(diagnostics) != 0 {
			t.Fatalf("package-clause Generate error=%v diagnostics=%v", err, diagnostics)
		}
		if !bytes.Contains(output[pagePath], []byte("package renamed")) {
			t.Fatalf("generated output retained old package clause:\n%s", output[pagePath])
		}
		if got := module.externalLoads(); got != 2 {
			t.Fatalf("external loads = %d, want package-clause manifest reload", got)
		}
	})
}
