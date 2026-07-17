package codegen

import (
	"go/types"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestOverrideTransitionsReturnExactAffectedDirectories(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping module-resolution test in short mode")
	}
	m, root := setupChainModule(t)
	utilDir := filepath.Join(root, "util")
	componentsDir := filepath.Join(root, "components")
	pagesDir := filepath.Join(root, "pages")
	soloDir := filepath.Join(root, "solo")
	if _, err := m.Package(pagesDir); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Package(soloDir); err != nil {
		t.Fatal(err)
	}

	utilPath := filepath.Join(utilDir, "util.gsx")
	edited := []byte("package util\n\ncomponent Y(label string) { <strong>{label}</strong> }\n")
	wantChain := []string{componentsDir, pagesDir, utilDir}
	slices.Sort(wantChain)
	if got := m.SetOverride(utilPath, edited); !reflect.DeepEqual(got, wantChain) {
		t.Fatalf("SetOverride affected = %v, want %v", got, wantChain)
	}
	if got := m.SetOverride(utilPath, edited); got != nil {
		t.Fatalf("same-byte SetOverride affected = %v, want nil", got)
	}
	if got, err := m.ClearOverride(utilPath); err != nil || !reflect.DeepEqual(got, wantChain) {
		t.Fatalf("ClearOverride affected, err = %v, %v; want %v, nil", got, err, wantChain)
	}
	if got, err := m.ClearOverride(utilPath); err != nil || got != nil {
		t.Fatalf("second ClearOverride affected, err = %v, %v; want nil, nil", got, err)
	}

	// Empty is still present. Adding and clearing an empty unsaved-new source is
	// a membership transition, not the old len(source)>0 dirtiness heuristic.
	emptyPath := filepath.Join(soloDir, "empty.gsx")
	if got := m.SetOverride(emptyPath, []byte{}); !reflect.DeepEqual(got, []string{soloDir}) {
		t.Fatalf("empty unsaved-new SetOverride affected = %v, want [%s]", got, soloDir)
	}
	if got, err := m.ClearOverride(emptyPath); err != nil || !reflect.DeepEqual(got, []string{soloDir}) {
		t.Fatalf("empty unsaved-new ClearOverride affected, err = %v, %v", got, err)
	}
}

func TestAffectedLockedOwnsWholeConfiguredSourceInvalidation(t *testing.T) {
	root := t.TempDir()
	seed := filepath.Join(root, "renderer")
	importer := filepath.Join(root, "page")
	unrelated := filepath.Join(root, "unrelated")
	unsaved := filepath.Join(root, "unsaved")
	cold := filepath.Join(root, "cold")
	goOnly := filepath.Join(root, "bridge")
	coldFact, _ := inspectGsxSourceInventory(filepath.Join(cold, "cold.gsx"), []byte("package cold\n"), true)

	m := &Module{
		opts:                 Options{ModuleRoot: root},
		overrides:            map[string][]byte{filepath.Join(unsaved, "new.gsx"): []byte("package unsaved\n")},
		sourceGsxDirs:        map[string]bool{seed: true, importer: true, unrelated: true},
		sourceInventoryFacts: map[string]gsxSourceInventoryFact{filepath.Join(cold, "cold.gsx"): coldFact},
		rendererDirs:         map[string]bool{seed: true},
		configuredSourceDirs: map[string]bool{},
		importedBy:           map[string]map[string]bool{seed: {importer: true}},
		targetImportedBy:     map[string]map[string]bool{},
		sourceDeclImportedBy: map[string]map[string]bool{},
		imports:              map[string][]string{},
		targetImports:        map[string][]string{},
		sourceDeclImports:    map[string][]string{},
		pkgTypes:             map[string]*types.Package{goOnly: nil},
		targetDeclTypes:      map[string]*types.Package{},
		configuredDeclTypes:  map[string]*types.Package{},
		pkgResults:           map[string]*PackageResult{},
		dirFuncTbls:          map[string]funcTables{},
	}

	scope := m.affectedLocked([]string{seed})
	if !scope.whole {
		t.Fatal("renderer source did not select whole-cache invalidation")
	}
	want := []string{cold, goOnly, importer, seed, unrelated, unsaved}
	slices.Sort(want)
	if got := scope.sorted(); !reflect.DeepEqual(got, want) {
		t.Fatalf("affectedLocked = %v, want authoritative GSX inventory plus retained graph %v", got, want)
	}
	if got := m.invalidateLocked([]string{seed}); !reflect.DeepEqual(got, want) {
		t.Fatalf("invalidateLocked affected = %v, want exact affectedLocked result %v", got, want)
	}
	if len(m.pkgTypes) != 0 {
		t.Fatalf("whole invalidation retained package types: %v", m.pkgTypes)
	}
}

func TestEqualByteFirstOverrideAdvancesSnapshotAuthorityWithoutInvalidation(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "page.gsx")
	source := []byte("package page\ncomponent Page() { <p/> }\n")
	if err := os.WriteFile(path, source, 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := Open(Options{ModuleRoot: root, ModulePath: "example.com/app"})
	if err != nil {
		t.Fatal(err)
	}

	before := m.sourceSnapshotEpoch
	if got := m.SetOverride(path, source); got != nil {
		t.Fatalf("equal-byte first SetOverride affected = %v, want nil", got)
	}
	if got := m.sourceSnapshotEpoch; got != before+1 {
		t.Fatalf("equal-byte first SetOverride snapshot epoch = %d, want %d", got, before+1)
	}
	if got := m.dirtyDirs(); len(got) != 0 {
		t.Fatalf("equal-byte first SetOverride dirtied = %v, want empty", got)
	}

	if got := m.SetOverride(path, source); got != nil {
		t.Fatalf("same override SetOverride affected = %v, want nil", got)
	}
	if got := m.sourceSnapshotEpoch; got != before+1 {
		t.Fatalf("same override SetOverride snapshot epoch = %d, want unchanged %d", got, before+1)
	}

	if got, err := m.ClearOverride(path); err != nil || got != nil {
		t.Fatalf("equal-byte ClearOverride affected, err = %v, %v; want nil, nil", got, err)
	}
	if got := m.sourceSnapshotEpoch; got != before+2 {
		t.Fatalf("equal-byte ClearOverride snapshot epoch = %d, want %d", got, before+2)
	}
	if got := m.dirtyDirs(); len(got) != 0 {
		t.Fatalf("equal-byte ClearOverride dirtied = %v, want empty", got)
	}
}

func TestClearOverridePublishesUnreadableSavedStateAndFailsClosed(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping module-resolution test in short mode")
	}
	m, root := setupChainModule(t)
	utilDir := filepath.Join(root, "util")
	componentsDir := filepath.Join(root, "components")
	pagesDir := filepath.Join(root, "pages")
	brokenPath := filepath.Join(utilDir, "broken.gsx")
	if err := os.Mkdir(brokenPath, 0o755); err != nil {
		t.Fatal(err)
	}

	m.SetOverride(brokenPath, []byte("package util\n\ncomponent Buffered() { <i>buffer</i> }\n"))
	if _, err := m.Package(pagesDir); err != nil {
		t.Fatalf("override did not mask unreadable saved source: %v", err)
	}
	want := []string{componentsDir, pagesDir, utilDir}
	slices.Sort(want)
	got, err := m.ClearOverride(brokenPath)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unreadable ClearOverride affected = %v, want old closure %v", got, want)
	}
	if err == nil || !strings.Contains(err.Error(), "read saved source") {
		t.Fatalf("unreadable ClearOverride error = %v", err)
	}
	m.mu.Lock()
	_, stillOverridden := m.overrides[brokenPath]
	m.mu.Unlock()
	if stillOverridden {
		t.Fatal("unreadable ClearOverride retained buffer authority")
	}
	if _, found := m.source(brokenPath); found {
		t.Fatal("unreadable ClearOverride retained closed buffer bytes")
	}
	if _, err := m.Package(pagesDir); err == nil || !strings.Contains(err.Error(), "read saved source") {
		t.Fatalf("analysis after unreadable ClearOverride = %v, want fail-closed saved-source error", err)
	}
	if got, err := m.ClearOverride(brokenPath); got != nil || err != nil {
		t.Fatalf("second ClearOverride = %v, %v; want nil, nil", got, err)
	}
	if got := m.SetOverride(brokenPath, []byte("package util\n\ncomponent BufferedAgain() { <b/> }\n")); !reflect.DeepEqual(got, want) {
		t.Fatalf("SetOverride masking unreadable state affected = %v, want %v", got, want)
	}
	if _, err := m.Package(pagesDir); err != nil {
		t.Fatalf("new override did not mask retained unreadable saved state: %v", err)
	}
}

func TestGoOverrideParticipatesInAuthoritativeColdLoad(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping module-resolution test in short mode")
	}
	m, root := setupChainModule(t)
	utilDir := filepath.Join(root, "util")
	goPath := filepath.Join(utilDir, "buffer.go")
	gsxPath := filepath.Join(utilDir, "util.gsx")
	if err := os.WriteFile(goPath, []byte("package util\ntype DiskLabel string\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Open preceded the file write, but the first cold load still snapshots disk.
	if _, err := m.Package(utilDir); err != nil {
		t.Fatal(err)
	}
	loads := m.externalLoads()

	virtualGo := filepath.Join(utilDir, "virtual.go")
	m.SetOverride(goPath, []byte("package util\ntype BufferLabel string\n"))
	m.SetOverride(virtualGo, []byte("package util\ntype VirtualLabel string\n"))
	m.SetOverride(gsxPath, []byte("package util\n\ncomponent Y(label BufferLabel, other VirtualLabel) { <span>{label}{other}</span> }\n"))
	result, err := m.Package(utilDir)
	if err != nil {
		t.Fatal(err)
	}
	if hasDiagErrors(result.Diags) {
		t.Fatalf("Go overrides were not used by package selection/type checking: %v", result.Diags)
	}
	if got := m.externalLoads(); got != loads+1 {
		t.Fatalf("Go override external loads = %d, want one authoritative cold reload after %d", got, loads)
	}
}

func TestClearOverrideRestoresFrozenSavedStateUntilExplicitRefresh(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/app\n\ngo 1.26.1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "page")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	for _, extension := range []string{".gsx", ".go"} {
		t.Run(extension, func(t *testing.T) {
			path := filepath.Join(dir, "saved"+extension)
			oldSaved := []byte("package page\n// old saved\n")
			if err := os.WriteFile(path, oldSaved, 0o644); err != nil {
				t.Fatal(err)
			}
			m, err := Open(Options{ModuleRoot: root, ModulePath: "example.com/app"})
			if err != nil {
				t.Fatal(err)
			}
			m.SetOverride(path, []byte("package page\n// buffer\n"))
			if err := os.WriteFile(path, []byte("package page\n// concurrent live disk\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := m.ClearOverride(path); err != nil {
				t.Fatal(err)
			}
			if got, ok := m.source(path); !ok || !reflect.DeepEqual(got, oldSaved) {
				t.Fatalf("source after Clear = %q, %v; want frozen pre-buffer saved bytes", got, ok)
			}
		})
	}

	absentPath := filepath.Join(dir, "absent.gsx")
	m, err := Open(Options{ModuleRoot: root, ModulePath: "example.com/app"})
	if err != nil {
		t.Fatal(err)
	}
	m.SetOverride(absentPath, []byte{})
	if err := os.WriteFile(absentPath, []byte("package page\ncomponent Appeared() { <p/> }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := m.ClearOverride(absentPath); err != nil {
		t.Fatal(err)
	}
	if _, ok := m.source(absentPath); ok {
		t.Fatal("Clear observed a live-disk file that appeared after the saved-absence snapshot")
	}
}

func TestRefreshDiskSourcesAtomicallyReplacesSavedStateBeneathOverride(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/app\n\ngo 1.26.1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "page")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "page.gsx")
	oldSaved := []byte("package page\ncomponent Old() { <p/> }\n")
	newSaved := []byte("package page\ncomponent New() { <p/> }\n")
	if err := os.WriteFile(path, oldSaved, 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := Open(Options{ModuleRoot: root, ModulePath: "example.com/app"})
	if err != nil {
		t.Fatal(err)
	}
	m.SetOverride(path, []byte("package page\ncomponent Buffer() { <p/> }\n"))
	if err := os.WriteFile(path, newSaved, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := m.RefreshDiskSources(dir); err != nil {
		t.Fatal(err)
	}
	if got, ok := m.source(path); !ok || !strings.Contains(string(got), "component Buffer") {
		t.Fatalf("refresh displaced active override: %q, %v", got, ok)
	}
	if _, err := m.ClearOverride(path); err != nil {
		t.Fatal(err)
	}
	if got, ok := m.source(path); !ok || !reflect.DeepEqual(got, newSaved) {
		t.Fatalf("source after refreshed Clear = %q, %v; want refreshed saved bytes", got, ok)
	}
}
