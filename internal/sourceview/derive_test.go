package sourceview

import (
	"path/filepath"
	"reflect"
	"slices"
	"testing"
)

// manifestView flattens every derived projection of a Manifest into a
// comparable value so a derived manifest can be checked for equality against a
// from-scratch Build of the same disk state.
func manifestView(t *testing.T, m *Manifest) map[string]any {
	t.Helper()
	sources := map[string]string{}
	for _, path := range m.SourcePaths() {
		source, ok := m.Source(path)
		if !ok {
			t.Fatalf("SourcePaths lists %s but Source misses it", path)
		}
		sources[path] = string(source)
	}
	overlay := map[string]string{}
	for path, source := range m.Overlay() {
		overlay[path] = string(source)
	}
	return map[string]any{
		"sources":     sources,
		"facts":       m.Facts(),
		"gsxDirs":     m.GSXDirs(),
		"packageDirs": m.PackageDirs(),
		"paired":      m.PairedOutputs(),
		"overlay":     overlay,
	}
}

// TestRefreshDirsMatchesFreshBuild pins the incremental-derivation contract:
// after arbitrary disk edits, RefreshDirs over the touched dirs must produce a
// manifest indistinguishable from a from-scratch Build of the same disk state
// — reused facts included. A stale reused fact (wrong package name, dropped
// import edge) shows up as a projection mismatch here.
func TestRefreshDirsMatchesFreshBuild(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "go.mod", "module example.com/app\n\ngo 1.26.1\n")
	writeTestFile(t, root, "a/a1.gsx", "package a\ncomponent A1() { <p/> }\n")
	writeTestFile(t, root, "a/a2.gsx", "package a\ncomponent A2() { <p/> }\n")
	writeTestFile(t, root, "b/b1.gsx", "package b\n\nimport \"example.com/app/a\"\n\ncomponent B1() { <a.A1/> }\n")
	writeTestFile(t, root, "b/helper.go", "package b\nfunc helper() {}\n")

	build := func() *Manifest {
		m, err := Build(BuildOptions{ModuleRoot: root, ModulePath: "example.com/app"})
		if err != nil {
			t.Fatal(err)
		}
		return m
	}
	base := build()

	// Edit one file, add one, remove nothing; refresh both dirs.
	writeTestFile(t, root, "a/a1.gsx", "package a\n\nimport \"strings\"\n\ncomponent A1() { <p>{strings.ToUpper(\"x\")}</p> }\n")
	writeTestFile(t, root, "b/b2.gsx", "package b\ncomponent B2() { <p/> }\n")
	refreshed, err := base.RefreshDirs([]string{filepath.Join(root, "a"), filepath.Join(root, "b")})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := manifestView(t, refreshed), manifestView(t, build()); !reflect.DeepEqual(got, want) {
		t.Fatalf("refreshed manifest diverges from fresh Build:\n got: %#v\nwant: %#v", got, want)
	}

	// The changed file's fact must reflect the new bytes, not the reused old
	// ones: the strings import is a new edge.
	fact, ok := refreshed.Fact(filepath.Join(root, "a", "a1.gsx"))
	if !ok || !slices.Contains(fact.Imports(), "strings") {
		t.Fatalf("refreshed manifest misses the edited file's new import edge; fact=%+v ok=%v", fact, ok)
	}

	// Refreshing an untouched dir must not re-parse the module: every fact is
	// reusable, so the Inspect count stays bounded by the refreshed dir's own
	// file count (re-reads are byte-identical).
	before := InspectCalls()
	again, err := refreshed.RefreshDirs([]string{filepath.Join(root, "a")})
	if err != nil {
		t.Fatal(err)
	}
	if delta := InspectCalls() - before; delta > 0 {
		t.Fatalf("byte-identical refresh re-parsed %d files; want 0 (fact reuse)", delta)
	}
	if got, want := manifestView(t, again), manifestView(t, refreshed); !reflect.DeepEqual(got, want) {
		t.Fatalf("no-op refresh changed the manifest:\n got: %#v\nwant: %#v", got, want)
	}
}

// TestWithFileSnapshotsEmptyReturnsReceiver pins the zero-derivation fast
// path: deriving with no snapshots is the identity, and manifests are
// immutable, so the receiver itself is the correct result.
func TestWithFileSnapshotsEmptyReturnsReceiver(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "go.mod", "module example.com/app\n\ngo 1.26.1\n")
	writeTestFile(t, root, "a/a.gsx", "package a\ncomponent A() { <p/> }\n")
	m, err := Build(BuildOptions{ModuleRoot: root, ModulePath: "example.com/app"})
	if err != nil {
		t.Fatal(err)
	}
	same, err := m.WithFileSnapshots(nil)
	if err != nil {
		t.Fatal(err)
	}
	if same != m {
		t.Fatalf("WithFileSnapshots(nil) allocated a new manifest; want the receiver")
	}
	same, err = m.WithFileSnapshots(map[string]FileSnapshot{})
	if err != nil {
		t.Fatal(err)
	}
	if same != m {
		t.Fatalf("WithFileSnapshots(empty) allocated a new manifest; want the receiver")
	}
}

// TestDerivedManifestsShareWithoutAliasing pins the immutability contract the
// slice sharing relies on: mutating what accessors return must never leak into
// any manifest, parent or derived.
func TestDerivedManifestsShareWithoutAliasing(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "go.mod", "module example.com/app\n\ngo 1.26.1\n")
	path := writeTestFile(t, root, "a/a.gsx", "package a\ncomponent A() { <p/> }\n")
	writeTestFile(t, root, "a/h.go", "package a\nfunc h() {}\n")
	parent, err := Build(BuildOptions{ModuleRoot: root, ModulePath: "example.com/app"})
	if err != nil {
		t.Fatal(err)
	}
	derived, err := parent.RefreshDirs([]string{filepath.Join(root, "a")})
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range []*Manifest{parent, derived} {
		source, ok := m.Source(path)
		if !ok {
			t.Fatal("source missing")
		}
		for i := range source {
			source[i] = 'X'
		}
		for _, v := range m.Overlay() {
			for i := range v {
				v[i] = 'X'
			}
		}
	}
	for _, m := range []*Manifest{parent, derived} {
		source, _ := m.Source(path)
		if string(source) != "package a\ncomponent A() { <p/> }\n" {
			t.Fatalf("caller mutation leaked into manifest source: %q", source)
		}
	}
}
