package gen

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/gsxhq/gsx/internal/sourceview"
)

const graphTestModule = "example.com/app"

// graphFixture writes a small module under a fresh temp dir and builds its
// manifest.
func graphFixture(t *testing.T) (string, *sourceview.Manifest) {
	t.Helper()
	root := t.TempDir()
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	writeTestFile(t, filepath.Join(root, "go.mod"), "module "+graphTestModule+"\n\ngo 1.26.1\n")
	writeTestFile(t, filepath.Join(root, "a", "a.go"), "package a\n\nimport \"strings\"\n\nvar _ = strings.ToUpper\n")
	writeTestFile(t, filepath.Join(root, "a", "data.txt"), "data\n")
	writeTestFile(t, filepath.Join(root, "b", "b.go"), "package b\n\nimport _ \"example.com/app/a\"\n")
	return root, buildGraphManifest(t, root)
}

func buildGraphManifest(t *testing.T, root string) *sourceview.Manifest {
	t.Helper()
	manifest, err := sourceview.Build(sourceview.BuildOptions{ModuleRoot: root, ModulePath: graphTestModule})
	if err != nil {
		t.Fatal(err)
	}
	return manifest
}

// graphAt is a synthetic go list graph for the fixture module checked out at
// root, in the shape cmd/go reports it.
func graphAt(root string) sourceview.Graph {
	mainModule := func() *sourceview.ModuleMetadata {
		return &sourceview.ModuleMetadata{Path: graphTestModule, Dir: root, GoMod: filepath.Join(root, "go.mod"), GoVersion: "1.26.1", Main: true}
	}
	return sourceview.Graph{
		"strings": {ImportPath: "strings", Dir: "/goroot/src/strings", GoFiles: []string{"strings.go"}, Standard: true, Deps: []string{"unicode"}},
		"example.com/app/a": {
			ImportPath: "example.com/app/a", Dir: filepath.Join(root, "a"),
			Imports: []string{"strings"}, Deps: []string{"strings", "unicode"},
			GoFiles: []string{"a.go"}, CompiledGoFiles: []string{"a.go"}, EmbedFiles: []string{"data.txt"},
			Module: mainModule(),
		},
		"example.com/app/b": {
			ImportPath: "example.com/app/b", Dir: filepath.Join(root, "b"),
			Imports: []string{"example.com/app/a"}, Deps: []string{"example.com/app/a"},
			GoFiles: []string{"b.go"}, CompiledGoFiles: []string{"b.go"},
			Module: mainModule(),
		},
		"example.com/dep": {
			ImportPath: "example.com/dep", Dir: "/modcache/example.com/dep@v1.0.0", GoFiles: []string{"d.go"},
			Module: &sourceview.ModuleMetadata{Path: "example.com/dep", Version: "v1.0.0", Dir: "/modcache/example.com/dep@v1.0.0"},
		},
	}
}

func graphVariantFor(t *testing.T, root string, manifest *sourceview.Manifest) graphVariant {
	t.Helper()
	graph := graphAt(root)
	input, err := witnessInputForGraph(graph, manifest)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := manifest.GraphWitness(input)
	if err != nil {
		t.Fatal(err)
	}
	rel, err := relativizeGraph(graph, root)
	if err != nil {
		t.Fatal(err)
	}
	return graphVariant{Witness: input, Digest: digest, Graph: rel}
}

func TestGraphStoreRoundTripRebases(t *testing.T) {
	rootA, manifestA := graphFixture(t)
	dir := t.TempDir()
	key, err := graphKey("fp", manifestA, []string{"./..."})
	if err != nil {
		t.Fatal(err)
	}
	variant := graphVariantFor(t, rootA, manifestA)
	if err := storeGraph(dir, key, variant); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(entryPath(filepath.Join(dir, "graph"), key))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), rootA) {
		t.Fatalf("entry contains the absolute module root %s:\n%s", rootA, data)
	}

	rootB, _ := graphFixture(t)
	manifestB := buildGraphManifest(t, rootB)
	keyB, err := graphKey("fp", manifestB, []string{"./..."})
	if err != nil {
		t.Fatal(err)
	}
	if keyB != key {
		t.Fatalf("graph key differs across checkouts: %s vs %s", key, keyB)
	}
	got, outcome := lookupGraph(dir, keyB, manifestB, rootB)
	if !outcome.Hit() || outcome.Reason != graphHit {
		t.Fatalf("lookup = %+v, want hit", outcome)
	}
	want := rebaseGraph(variant.Graph, rootB)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("rebased graph = %+v, want %+v", got, want)
	}
	for path, pkg := range got {
		if pkg.Module != nil && pkg.Module.Main {
			if !strings.HasPrefix(pkg.Dir, rootB) || !strings.HasPrefix(pkg.Module.Dir, rootB) || pkg.Module.GoMod != filepath.Join(rootB, "go.mod") {
				t.Errorf("%s not rebased: Dir %s Module %+v", path, pkg.Dir, pkg.Module)
			}
		}
	}
	if got["strings"].Dir != "/goroot/src/strings" || got["example.com/dep"].Dir != "/modcache/example.com/dep@v1.0.0" {
		t.Errorf("non-main dirs changed: %+v", got)
	}

	// Different key components give different keys.
	for name, k := range map[string]func() (string, error){
		"fingerprint": func() (string, error) { return graphKey("fp2", manifestB, []string{"./..."}) },
		"patterns":    func() (string, error) { return graphKey("fp", manifestB, []string{"./a"}) },
	} {
		if other, err := k(); err != nil || other == key {
			t.Errorf("%s: key %s (err %v) equals base key", name, other, err)
		}
	}
	writeTestFile(t, filepath.Join(rootB, "go.sum"), "x\n")
	if other, err := graphKey("fp", buildGraphManifest(t, rootB), []string{"./..."}); err != nil || other == key {
		t.Errorf("go.sum: key %s (err %v) equals base key", other, err)
	}

	// A changed witnessed directory is a miss with the right reason.
	writeTestFile(t, filepath.Join(rootB, "a", "a.go"), "package a\n\nimport \"fmt\"\n\nvar _ = fmt.Sprint\n")
	_, outcome = lookupGraph(dir, keyB, buildGraphManifest(t, rootB), rootB)
	if outcome.Reason != graphWitnessChanged {
		t.Fatalf("changed lookup = %+v, want witness-changed", outcome)
	}
	_, outcome = lookupGraph(dir, "ffffffff", manifestB, rootB)
	if outcome.Reason != graphEntryMissing {
		t.Fatalf("absent lookup = %+v, want entry-missing", outcome)
	}
}

func TestGraphStoreVariantsMRU(t *testing.T) {
	root, manifest := graphFixture(t)
	dir := t.TempDir()
	key := "abcdef0123"
	base := graphVariantFor(t, root, manifest)
	for i := range 5 {
		v := base
		v.Digest = "digest-" + string(rune('a'+i))
		if err := storeGraph(dir, key, v); err != nil {
			t.Fatal(err)
		}
	}
	// Re-storing an existing digest dedups and moves it to the front.
	again := base
	again.Digest = "digest-c"
	if err := storeGraph(dir, key, again); err != nil {
		t.Fatal(err)
	}
	entry := readGraphEntry(t, dir, key)
	var got []string
	for _, v := range entry.Variants {
		got = append(got, v.Digest)
	}
	if want := []string{"digest-c", "digest-e", "digest-d", "digest-b"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("variants = %v, want %v", got, want)
	}

	// A lookup matches any stored variant, not only the first.
	other := base
	other.Witness = sourceview.GraphWitnessInput{Dirs: []string{"b"}}
	digest, err := manifest.GraphWitness(other.Witness)
	if err != nil {
		t.Fatal(err)
	}
	other.Digest = digest
	for _, v := range []graphVariant{other} {
		if err := storeGraph(dir, key, v); err != nil {
			t.Fatal(err)
		}
	}
	if err := storeGraph(dir, key, again); err != nil {
		t.Fatal(err)
	}
	if _, outcome := lookupGraph(dir, key, manifest, root); !outcome.Hit() {
		t.Fatalf("lookup of non-first variant = %+v, want hit", outcome)
	}
}

func readGraphEntry(t *testing.T, dir, key string) graphEntry {
	t.Helper()
	data, err := os.ReadFile(entryPath(filepath.Join(dir, "graph"), key))
	if err != nil {
		t.Fatal(err)
	}
	var entry graphEntry
	if err := json.Unmarshal(data, &entry); err != nil {
		t.Fatal(err)
	}
	return entry
}

func TestGraphStoreCorruptIsMiss(t *testing.T) {
	root, manifest := graphFixture(t)
	dir := t.TempDir()
	key := "abcdef0123"
	valid, err := json.Marshal(graphEntry{Version: 1, Variants: []graphVariant{graphVariantFor(t, root, manifest)}})
	if err != nil {
		t.Fatal(err)
	}
	traversal := graphVariantFor(t, root, manifest)
	traversal.Graph["example.com/app/a"] = func() sourceview.PackageMetadata {
		pkg := traversal.Graph["example.com/app/a"]
		pkg.Dir = "../../etc"
		return pkg
	}()
	traversalData, err := json.Marshal(graphEntry{Version: 1, Variants: []graphVariant{traversal}})
	if err != nil {
		t.Fatal(err)
	}
	tests := map[string][]byte{
		"truncated":       valid[:len(valid)/2],
		"garbage":         []byte("\x00\xff not json"),
		"version zero":    []byte(`{"version":0}`),
		"version future":  []byte(`{"version":2,"variants":[]}`),
		"no variants":     []byte(`{"version":1,"variants":[]}`),
		"empty file":      {},
		"escaping rel":    traversalData,
		"trailing garbag": append(append([]byte(nil), valid...), []byte("xx")...),
	}
	for name, data := range tests {
		t.Run(name, func(t *testing.T) {
			p := entryPath(filepath.Join(dir, "graph"), key)
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, data, 0o644); err != nil {
				t.Fatal(err)
			}
			graph, outcome := lookupGraph(dir, key, manifest, root)
			if outcome.Reason != graphEntryCorrupt || graph != nil {
				t.Fatalf("lookup = %+v (graph %v), want corrupt", outcome, graph)
			}
		})
	}
	// A corrupt entry is replaced by the next store.
	if err := storeGraph(dir, key, graphVariantFor(t, root, manifest)); err != nil {
		t.Fatal(err)
	}
	if _, outcome := lookupGraph(dir, key, manifest, root); !outcome.Hit() {
		t.Fatalf("lookup after repair = %+v", outcome)
	}
}

func TestWitnessInputForGraph(t *testing.T) {
	root, manifest := graphFixture(t)

	graph := graphAt(root)
	graph["example.com/app/nested"] = sourceview.PackageMetadata{
		ImportPath: "example.com/app/nested", Dir: "/modcache/example.com/app/nested@v1.0.0", GoFiles: []string{"n.go"},
		Module: &sourceview.ModuleMetadata{Path: "example.com/app/nested", Version: "v1.0.0"},
	}
	input, err := witnessInputForGraph(graph, manifest)
	if err != nil {
		t.Fatal(err)
	}
	want := sourceview.GraphWitnessInput{Dirs: []string{"a", "b", "nested"}, EmbedFiles: []string{"a/data.txt"}}
	if !reflect.DeepEqual(input, want) {
		t.Fatalf("input = %+v, want %+v", input, want)
	}

	t.Run("logical root spelled through a symlink", func(t *testing.T) {
		link := filepath.Join(t.TempDir(), "link")
		if err := os.Symlink(root, link); err != nil {
			t.Fatal(err)
		}
		linked := buildGraphManifest(t, link)
		got, err := witnessInputForGraph(graphAt(link), linked)
		if err != nil || !reflect.DeepEqual(got.Dirs, []string{"a", "b"}) {
			t.Fatalf("logical = %+v, %v", got, err)
		}
		got, err = witnessInputForGraph(graphAt(root), linked)
		if err != nil || !reflect.DeepEqual(got.Dirs, []string{"a", "b"}) {
			t.Fatalf("physical = %+v, %v", got, err)
		}
	})

	unavailable := map[string]func(g sourceview.Graph){
		"directory replace": func(g sourceview.Graph) {
			pkg := g["example.com/dep"]
			pkg.Module = &sourceview.ModuleMetadata{Path: "example.com/dep", Version: "v1.0.0", Replace: &sourceview.ModuleMetadata{Path: "../dep", Dir: "/elsewhere/dep"}}
			g["example.com/dep"] = pkg
		},
		"versionless module": func(g sourceview.Graph) {
			pkg := g["example.com/dep"]
			pkg.Module = &sourceview.ModuleMetadata{Path: "example.com/dep", Dir: "/elsewhere/dep"}
			g["example.com/dep"] = pkg
		},
		"main dir outside roots": func(g sourceview.Graph) {
			pkg := g["example.com/app/a"]
			pkg.Dir = "/elsewhere/a"
			g["example.com/app/a"] = pkg
		},
		"main dir empty": func(g sourceview.Graph) {
			pkg := g["example.com/app/a"]
			pkg.Dir = ""
			g["example.com/app/a"] = pkg
		},
	}
	for name, mutate := range unavailable {
		t.Run(name, func(t *testing.T) {
			g := graphAt(root)
			mutate(g)
			if _, err := witnessInputForGraph(g, manifest); !errors.Is(err, sourceview.ErrWitnessUnavailable) {
				t.Fatalf("err = %v, want ErrWitnessUnavailable", err)
			}
		})
	}
	t.Run("version replace is cacheable", func(t *testing.T) {
		g := graphAt(root)
		pkg := g["example.com/dep"]
		pkg.Module = &sourceview.ModuleMetadata{Path: "example.com/dep", Version: "v1.0.0", Replace: &sourceview.ModuleMetadata{Path: "example.com/fork", Version: "v1.1.0"}}
		g["example.com/dep"] = pkg
		if _, err := witnessInputForGraph(g, manifest); err != nil {
			t.Fatal(err)
		}
	})
}

func TestRebaseDropsDepsOnly(t *testing.T) {
	root := "/old/checkout"
	graph := graphAt(root)
	rel, err := relativizeGraph(graph, root)
	if err != nil {
		t.Fatal(err)
	}
	newRoot := "/new/place"
	got := rebaseGraph(rel, newRoot)
	want := graphAt(newRoot)
	for path, pkg := range want {
		pkg.Deps = nil
		want[path] = pkg
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("rebased = %+v\nwant     %+v", got, want)
	}
	// The input graph is untouched.
	if !reflect.DeepEqual(graph, graphAt(root)) {
		t.Fatal("relativizeGraph mutated its input")
	}
	// The stored form carries no Deps and no absolute main-module path.
	data, err := json.Marshal(rel)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), root) {
		t.Fatalf("relative graph contains %s: %s", root, data)
	}
	for path, pkg := range rel {
		if len(pkg.Deps) != 0 {
			t.Errorf("%s keeps Deps", path)
		}
	}

	// Main-module dir outside the module root cannot be stored.
	bad := graphAt(root)
	pkg := bad["example.com/app/a"]
	pkg.Dir = "/elsewhere/a"
	bad["example.com/app/a"] = pkg
	if _, err := relativizeGraph(bad, root); err == nil {
		t.Fatal("relativizeGraph accepted a main-module dir outside the root")
	}
	// Absolute file-list entries cannot be relativized faithfully.
	abs := graphAt(root)
	pkg = abs["example.com/app/a"]
	pkg.CompiledGoFiles = []string{filepath.Join(root, "a", "a.go")}
	abs["example.com/app/a"] = pkg
	if _, err := relativizeGraph(abs, root); err == nil {
		t.Fatal("relativizeGraph accepted an absolute main-module file entry")
	}
}
