package sourceview

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"syscall"
	"testing"
)

func TestFileSnapshotsPreserveUnreadableStateAndGoOverrides(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "go.mod", "module example.com/app\n\ngo 1.26.1\n")
	page := writeTestFile(t, root, "page/page.gsx", "package page\ncomponent Page() { <p/> }\n")
	model := writeTestFile(t, root, "page/model.go", "package page\ntype Saved string\n")
	virtual := filepath.Join(root, "page", "virtual.go")

	manifest, err := Build(BuildOptions{
		ModuleRoot: root,
		ModulePath: "example.com/app",
		Overrides: map[string][]byte{
			model:   []byte("package page\ntype Buffer string\n"),
			virtual: []byte("package page\ntype Virtual string\n"),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := manifest.Overlay()[model]; string(got) != "package page\ntype Buffer string\n" {
		t.Fatalf("Go override = %q, want buffer bytes", got)
	}
	if got := manifest.Overlay()[virtual]; string(got) != "package page\ntype Virtual string\n" {
		t.Fatalf("unsaved-new Go override = %q, want virtual bytes", got)
	}
	packagesOverlay, err := manifest.PackagesOverlay()
	if err != nil {
		t.Fatal(err)
	}
	physicalModel := filepath.Join(manifest.PhysicalRoot(), "page", "model.go")
	if got := packagesOverlay[physicalModel]; string(got) != "package page\ntype Buffer string\n" {
		t.Fatalf("physical packages overlay = %q, want buffer bytes at %s", got, physicalModel)
	}

	readErr := errors.New("permission denied")
	saved, err := manifest.WithFileSnapshots(map[string]FileSnapshot{
		page:    UnreadableFile(readErr),
		model:   PresentFile([]byte("package page\ntype Saved string\n")),
		virtual: AbsentFile(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot, known := saved.FileSnapshot(page); !known || snapshot.State() != FileUnreadable || !errors.Is(snapshot.Err(), readErr) {
		t.Fatalf("saved page snapshot = (%v, %v, %v), want unreadable", snapshot.State(), snapshot.Err(), known)
	}
	if err := saved.CheckReadable(); err == nil || !errors.Is(err, readErr) {
		t.Fatalf("CheckReadable() = %v, want saved read error", err)
	}
	if got := saved.Overlay()[model]; string(got) != "package page\ntype Saved string\n" {
		t.Fatalf("saved Go source = %q", got)
	}
	if got := saved.Overlay()[virtual]; !bytes.Contains(got, []byte("//go:build")) {
		t.Fatalf("saved absent Go source projection = %q, want excluded source", got)
	}

	masked, err := saved.WithOverrides(map[string][]byte{
		page:    []byte("package page\ncomponent Buffer() { <strong/> }\n"),
		virtual: []byte("package page\ntype VirtualAgain string\n"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := masked.CheckReadable(); err != nil {
		t.Fatalf("override did not mask unreadable saved source: %v", err)
	}
	if source, ok := masked.Source(page); !ok || !bytes.Contains(source, []byte("component Buffer")) {
		t.Fatalf("masked page source = %q, %v", source, ok)
	}
	if got := masked.Overlay()[virtual]; string(got) != "package page\ntype VirtualAgain string\n" {
		t.Fatalf("masked absent Go source = %q", got)
	}
	if err := saved.CheckReadable(); err == nil || !errors.Is(err, readErr) {
		t.Fatalf("derivation mutated unreadable saved view: %v", err)
	}
}

func writeTestFile(t *testing.T, root, rel, source string) string {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(source), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestBuildOwnsGsxMembershipImportsAndOverlay(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "go.mod", "module example.com/app\n\ngo 1.26.1\n")
	card := writeTestFile(t, root, "ui/card.gsx", `package ui

import (
	"example.com/app/model"
	_ "example.com/sideeffect"
	"C"
)

component Card() { <p>card</p> }
`)
	paired := writeTestFile(t, root, "ui/card.x.go", "package poison\nfunc (\n")
	only := writeTestFile(t, root, "only/page.gsx", "package only\ncomponent Page() { <p>only</p> }\n")
	writeTestFile(t, root, "vendor/example.com/hidden/hidden.gsx", "package hidden\ncomponent Hidden() { <p/> }\n")
	writeTestFile(t, root, "nested/go.mod", "module example.com/nested\n\ngo 1.26.1\n")
	writeTestFile(t, root, "nested/hidden/hidden.gsx", "package hidden\ncomponent Hidden() { <p/> }\n")

	manifest, err := Build(BuildOptions{ModuleRoot: root, ModulePath: "example.com/app"})
	if err != nil {
		t.Fatal(err)
	}

	wantSources := []string{card, only}
	sort.Strings(wantSources)
	if got := manifest.SourcePaths(); !reflect.DeepEqual(got, wantSources) {
		t.Fatalf("SourcePaths() = %v, want %v", got, wantSources)
	}
	if got := manifest.LoadRoots(); !reflect.DeepEqual(got, []string{
		"example.com/app/model",
		"example.com/app/only",
		"example.com/app/ui",
		"example.com/sideeffect",
	}) {
		t.Fatalf("LoadRoots() = %v", got)
	}
	if dir, ok := manifest.PackageDir("example.com/app/ui"); !ok || dir != filepath.Join(root, "ui") {
		t.Fatalf("PackageDir(ui) = %q, %v", dir, ok)
	}
	if _, ok := manifest.PackageDir("example.com/app/nested/hidden"); ok {
		t.Fatal("nested module package entered parent manifest")
	}
	wantPaired := []string{paired, strings.TrimSuffix(only, ".gsx") + ".x.go"}
	sort.Strings(wantPaired)
	if got := manifest.PairedOutputs(); !reflect.DeepEqual(got, wantPaired) {
		t.Fatalf("PairedOutputs() = %v", got)
	}

	overlay := manifest.Overlay()
	if got := overlay[paired]; !bytes.Contains(got, []byte("//go:build")) {
		t.Fatalf("paired replacement = %q", got)
	}
	if absent := strings.TrimSuffix(only, ".gsx") + ".x.go"; !bytes.Contains(overlay[absent], []byte("//go:build")) {
		t.Fatalf("absent paired replacement = %q", overlay[absent])
	}
	sentinels := manifest.SentinelFiles()
	if len(sentinels) != 2 {
		t.Fatalf("SentinelFiles() = %v, want two", sentinels)
	}
	for _, sentinel := range sentinels {
		if len(overlay[sentinel]) == 0 {
			t.Fatalf("sentinel %s absent from overlay", sentinel)
		}
	}

	// Accessors are immutable snapshots: a consumer cannot mutate the manifest
	// seen by the other consumer.
	overlay[paired][0] = 'X'
	overlay["invented.go"] = []byte("package invented\n")
	if got := manifest.Overlay()[paired]; bytes.HasPrefix(got, []byte("X")) {
		t.Fatal("Overlay returned manifest-owned bytes")
	}
	if _, ok := manifest.Overlay()["invented.go"]; ok {
		t.Fatal("Overlay returned manifest-owned map")
	}
}

func TestManifestSelectedLoadRoots(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "go.mod", "module example.com/app\n\ngo 1.26.1\n")
	pagesDir := filepath.Join(root, "pages")
	writeTestFile(t, root, "pages/home.gsx", "package pages\nimport \"example.com/app/ui\"\ncomponent Home() { <ui.Card/> }\n")
	writeTestFile(t, root, "ui/card.gsx", "package ui\nimport \"example.com/model\"\ncomponent Card() { <p/> }\n")
	writeTestFile(t, root, "admin/admin.gsx", "package admin\ncomponent Admin() { <p/> }\n")
	writeTestFile(t, root, "nested/go.mod", "module example.com/nested\n\ngo 1.26.1\n")
	writeTestFile(t, root, "nested/hidden/hidden.gsx", "package hidden\ncomponent Hidden() { <p/> }\n")

	manifest, err := Build(BuildOptions{ModuleRoot: root, ModulePath: "example.com/app"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := manifest.SelectedLoadRoots([]string{pagesDir})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"example.com/app/pages",
		"example.com/app/ui",
		"example.com/model",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("SelectedLoadRoots() = %v, want %v", got, want)
	}
}

func TestBuildUsesOverridesAsAuthoritativeSources(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "go.mod", "module example.com/app\n\ngo 1.26.1\n")
	page := writeTestFile(t, root, "page/page.gsx", "package page\nimport \"example.com/old\"\ncomponent Page() { <p/> }\n")
	virtual := filepath.Join(root, "virtual", "card.gsx")

	manifest, err := Build(BuildOptions{
		ModuleRoot: root,
		ModulePath: "example.com/app",
		Overrides: map[string][]byte{
			page:    []byte("package page\nimport \"example.com/new\"\ncomponent Page() { <p/> }\n"),
			virtual: []byte("package virtual\ncomponent Card() { <p/> }\n"),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := manifest.LoadRoots(); !reflect.DeepEqual(got, []string{
		"example.com/app/page",
		"example.com/app/virtual",
		"example.com/new",
	}) {
		t.Fatalf("LoadRoots() = %v", got)
	}
	if source, ok := manifest.Source(page); !ok || bytes.Contains(source, []byte("example.com/old")) {
		t.Fatalf("Source(page) = %q, %v", source, ok)
	}
	if _, ok := manifest.Source(virtual); !ok {
		t.Fatal("override-only GSX source absent")
	}
}

func TestSavedManifestChangesOnlyThroughExplicitRefresh(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "go.mod", "module example.com/app\n\ngo 1.26.1\n")
	oldCard := "package ui\ncomponent Card() { <p>old</p> }\n"
	card := writeTestFile(t, root, "ui/card.gsx", oldCard)
	paired := writeTestFile(t, root, "ui/card.x.go", "package poison\nfunc (\n")
	other := writeTestFile(t, root, "other/view.gsx", "package other\ncomponent View() { <p>old</p> }\n")
	manifest, err := Build(BuildOptions{ModuleRoot: root, ModulePath: "example.com/app"})
	if err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, root, "ui/card.gsx", "package ui\ncomponent Card() { <p>disk-new</p> }\n")
	added := writeTestFile(t, root, "ui/added.gsx", "package ui\ncomponent Added() { <p/> }\n")
	writeTestFile(t, root, "other/view.gsx", "package other\ncomponent View() { <p>unrefreshed</p> }\n")
	if err := os.Remove(paired); err != nil {
		t.Fatal(err)
	}

	effective, err := manifest.WithOverrides(map[string][]byte{
		card: []byte("package ui\ncomponent Card() { <strong>override</strong> }\n"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := effective.Source(added); ok {
		t.Fatal("concurrently added GSX source entered the saved snapshot without refresh")
	}
	if _, ok := effective.Overlay()[paired]; !ok {
		t.Fatal("paired-output removal changed the saved snapshot without refresh")
	}

	refreshed, err := manifest.RefreshDirs([]string{filepath.Join(root, "ui")})
	if err != nil {
		t.Fatal(err)
	}
	if source, ok := refreshed.Source(card); !ok || string(source) != "package ui\ncomponent Card() { <p>disk-new</p> }\n" {
		t.Fatalf("refreshed card source = %q, %v", source, ok)
	}
	if _, ok := refreshed.Source(added); !ok {
		t.Fatal("explicit directory refresh did not publish added GSX source")
	}
	if refreshed.pairedPresent[paired] {
		t.Fatal("explicit directory refresh retained removed paired-output presence")
	}
	if _, ok := refreshed.Overlay()[paired]; !ok {
		t.Fatal("paired exclusion disappeared after the output was removed")
	}
	if source, ok := refreshed.Source(other); !ok || string(source) != "package other\ncomponent View() { <p>old</p> }\n" {
		t.Fatalf("refresh of ui observed unrelated other-dir change: %q, %v", source, ok)
	}
	if source, ok := manifest.Source(card); !ok || string(source) != oldCard {
		t.Fatalf("base manifest mutated after derivation: %q, %v", source, ok)
	}
}

func TestReloadReasonComposesAgainstPublishedFact(t *testing.T) {
	path := filepath.Join(t.TempDir(), "page.gsx")
	published := Inspect(path, []byte("package page\nimport \"example.com/old\"\ncomponent Page() { <p/> }\n"), true)
	changed := Inspect(path, []byte("package page\nimport (\"example.com/old\"; \"example.com/new\")\ncomponent Page() { <p/> }\n"), true)
	reverted := Inspect(path, []byte("package page\nimport \"example.com/old\"\ncomponent Page() { <p>body changed</p> }\n"), true)

	if got := ReloadReasonFor(published, changed, map[string]bool{"example.com/old": true}); got != ReloadImports {
		t.Fatalf("new unavailable import reason = %v, want ReloadImports", got)
	}
	if got := ReloadReasonFor(published, changed, map[string]bool{"example.com/old": true, "example.com/new": true}); got != ReloadNone {
		t.Fatalf("already-published import reason = %v, want none", got)
	}
	if got := ReloadReasonFor(published, reverted, map[string]bool{"example.com/old": true}); got != ReloadNone {
		t.Fatalf("reverted structural fact reason = %v, want none", got)
	}
	if got := ReloadReasonFor(published, Inspect(path, nil, false), nil); got != ReloadMembership {
		t.Fatalf("removal reason = %v, want membership", got)
	}
	if got := ReloadReasonFor(published, Inspect(path, []byte("package renamed\ncomponent Page() { <p/> }\n"), true), nil); got != ReloadPackage {
		t.Fatalf("package rename reason = %v, want package", got)
	}
}

func TestMaterializeGoOverlayPreservesLogicalManifestBytes(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "go.mod", "module example.com/app\n\ngo 1.26.1\n")
	writeTestFile(t, root, "page/page.gsx", "package page\ncomponent Page() { <p/> }\n")
	writeTestFile(t, root, "page/page.x.go", "package stale\n")
	manifest, err := Build(BuildOptions{ModuleRoot: root, ModulePath: "example.com/app"})
	if err != nil {
		t.Fatal(err)
	}
	materialized, err := manifest.MaterializeGoOverlay()
	if err != nil {
		t.Fatal(err)
	}
	path := materialized.Path()
	t.Cleanup(func() {
		if err := materialized.Close(); err != nil {
			t.Error(err)
		}
	})

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		Replace map[string]string
	}
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	want := manifest.Overlay()
	if len(config.Replace) != len(want) {
		t.Fatalf("materialized replacements = %v, want %v", config.Replace, want)
	}
	logicalPaths := make([]string, 0, len(config.Replace))
	for logical, source := range want {
		rel, err := filepath.Rel(manifest.ModuleRoot(), logical)
		if err != nil {
			t.Fatal(err)
		}
		transport := filepath.Join(manifest.PhysicalRoot(), rel)
		backing, ok := config.Replace[transport]
		if !ok {
			t.Fatalf("materialized replacements missing physical target %s: %v", transport, config.Replace)
		}
		logicalPaths = append(logicalPaths, logical)
		if transport == backing {
			t.Fatalf("transport path %s used as temp backing", transport)
		}
		got, err := os.ReadFile(backing)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, source) {
			t.Fatalf("backing bytes for %s = %q, want %q", logical, got, source)
		}
	}
	sort.Strings(logicalPaths)
	if got := manifest.OverlayPaths(); !reflect.DeepEqual(logicalPaths, got) {
		t.Fatalf("materialized logical paths = %v, manifest = %v", logicalPaths, got)
	}
}

func TestBuildFailsClosedAtInvalidModuleBoundary(t *testing.T) {
	for _, test := range []struct {
		name  string
		setup func(*testing.T, string)
	}{
		{
			name: "go.mod directory",
			setup: func(t *testing.T, nested string) {
				t.Helper()
				if err := os.MkdirAll(filepath.Join(nested, "go.mod"), 0o755); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "broken go.mod symlink",
			setup: func(t *testing.T, nested string) {
				t.Helper()
				if err := os.Symlink(filepath.Join(nested, "missing.mod"), filepath.Join(nested, "go.mod")); err != nil {
					t.Skipf("symlink unavailable: %v", err)
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			writeTestFile(t, root, "go.mod", "module example.com/app\n\ngo 1.26.1\n")
			nested := filepath.Join(root, "nested")
			if err := os.MkdirAll(nested, 0o755); err != nil {
				t.Fatal(err)
			}
			test.setup(t, nested)
			writeTestFile(t, root, "nested/page/page.gsx", "package page\ncomponent Page() { <p/> }\n")
			if _, err := Build(BuildOptions{ModuleRoot: root, ModulePath: "example.com/app"}); err == nil {
				t.Fatal("invalid nested go.mod was treated as ordinary parent-module source")
			}
		})
	}
}

func TestBuildRejectsOverrideThroughEscapingSymlink(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	writeTestFile(t, root, "go.mod", "module example.com/app\n\ngo 1.26.1\n")
	link := filepath.Join(root, "linked")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	logical := filepath.Join(link, "not-created-yet", "card.gsx")
	if owned, err := OwnsPath(root, logical); err != nil || owned {
		t.Fatalf("OwnsPath(escaping nonexistent leaf) = %v, %v; want false, nil", owned, err)
	}
	manifest, err := Build(BuildOptions{
		ModuleRoot: root,
		ModulePath: "example.com/app",
		Overrides:  map[string][]byte{logical: []byte("package card\ncomponent Card() { <p/> }\n")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := manifest.SourcePaths(); len(got) != 0 {
		t.Fatalf("escaping override entered manifest: %v", got)
	}
}

func TestBuildWalksSymlinkModuleRootWithoutChangingLogicalPaths(t *testing.T) {
	parent := t.TempDir()
	physical := filepath.Join(parent, "physical")
	logical := filepath.Join(parent, "logical")
	writeTestFile(t, physical, "go.mod", "module example.com/app\n\ngo 1.26.1\n")
	writeTestFile(t, physical, "ui/card.gsx", "package ui\ncomponent Card() { <p/> }\n")
	if err := os.Symlink(physical, logical); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	manifest, err := Build(BuildOptions{ModuleRoot: logical, ModulePath: "example.com/app"})
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(logical, "ui", "card.gsx")
	if got := manifest.SourcePaths(); !reflect.DeepEqual(got, []string{want}) {
		t.Fatalf("SourcePaths() = %v, want logical path %s", got, want)
	}
	wantPhysical, err := filepath.EvalSymlinks(physical)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.ModuleRoot() != logical || manifest.PhysicalRoot() != wantPhysical {
		t.Fatalf("roots = logical %q physical %q", manifest.ModuleRoot(), manifest.PhysicalRoot())
	}
	materialized, err := manifest.MaterializeGoOverlay()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := materialized.Close(); err != nil {
			t.Error(err)
		}
	})
	data, err := os.ReadFile(materialized.Path())
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		Replace map[string]string
	}
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	for logicalPath := range manifest.Overlay() {
		rel, err := filepath.Rel(logical, logicalPath)
		if err != nil {
			t.Fatal(err)
		}
		transport := filepath.Join(wantPhysical, rel)
		if _, ok := config.Replace[transport]; !ok {
			t.Fatalf("materialized overlay keys = %v, want physical target %s for logical path %s", config.Replace, transport, logicalPath)
		}
	}
}

func TestOwnsDirHandlesRootAndDirectoryBoundaries(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "go.mod", "module example.com/app\n\ngo 1.26.1\n")
	owned := filepath.Join(root, "ui")
	if err := os.MkdirAll(owned, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{root, owned, filepath.Join(owned, "missing-child")} {
		if ok, err := OwnsDir(root, dir); err != nil || !ok {
			t.Fatalf("OwnsDir(%s) = %v, %v; want true, nil", dir, ok, err)
		}
	}
	nested := filepath.Join(root, "nested")
	writeTestFile(t, nested, "go.mod", "module example.com/nested\n\ngo 1.26.1\n")
	if ok, err := OwnsDir(root, nested); err != nil || ok {
		t.Fatalf("OwnsDir(nested module) = %v, %v; want false, nil", ok, err)
	}
}

// TestPathAbsentToleratesENOSYS pins the browser/js-wasm playground fix: a
// filesystem-probe returning ENOSYS ("not implemented on js" — no filesystem)
// must be treated as an absent path, exactly like ENOENT. The bundled resolver
// serves an in-memory virtual module rooted at a path that never exists on
// disk; on a native server EvalSymlinks/Lstat report ENOENT, but in a browser
// they report ENOSYS. Without ENOSYS tolerance, OwnsPath aborts and the WASM
// transform silently produces zero files. See manifest.go pathAbsent.
func TestPathAbsentToleratesENOSYS(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"enoent", os.ErrNotExist, true},
		{"enosys", syscall.ENOSYS, true},
		{"wrapped-enosys", &os.PathError{Op: "lstat", Path: "/__gsxmem__", Err: syscall.ENOSYS}, true},
		{"eacces", syscall.EACCES, false},
		{"broken-symlink-style", errors.New("some other error"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := pathAbsent(tc.err); got != tc.want {
				t.Fatalf("pathAbsent(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// TestResolvePathAllowMissingLexicalWithoutFilesystem verifies the ownership
// resolver falls back to a lexical path when the whole path is absent, the
// behavior the virtual "/__gsxmem__" root depends on. The path is a fully
// nonexistent absolute root (no real prefix to resolve symlinks against), the
// same shape the bundled resolver feeds it; a native run reports ENOENT for
// every component, the browser reports ENOSYS (see TestPathAbsentToleratesENOSYS).
func TestResolvePathAllowMissingLexicalWithoutFilesystem(t *testing.T) {
	missing := "/__gsxmem_test_does_not_exist__/views"
	got, err := resolvePathAllowMissing(missing)
	if err != nil {
		t.Fatalf("resolvePathAllowMissing(%q) returned error: %v", missing, err)
	}
	if got != filepath.Clean(missing) {
		t.Fatalf("resolvePathAllowMissing(%q) = %q, want lexical %q", missing, got, filepath.Clean(missing))
	}
}
