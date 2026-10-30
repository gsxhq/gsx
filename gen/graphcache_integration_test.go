package gen

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/gsxhq/gsx/internal/codegen"
	"github.com/gsxhq/gsx/internal/sourceview"
)

// graphDiffModule is the differential fixture: a (Go + GSX) imports b and e,
// e embeds data.txt, b has a constraint-excluded file, and nested/ is an
// in-tree nested module. It depends on nothing outside the standard library.
var graphDiffModule = map[string]string{
	"go.mod":        "module example.com/app\n\ngo 1.26.1\n",
	"a/a.go":        "package a\n\nimport (\n\t_ \"example.com/app/b\"\n\t_ \"example.com/app/e\"\n)\n\nfunc Body() int { return 1 }\n",
	"a/a.gsx":       "package a\n\ncomponent A() { <p>a</p> }\n",
	"b/b.go":        "package b\n\nimport \"strings\"\n\nvar Upper = strings.ToUpper\n",
	"b/c.go":        "package b\n\nimport \"unicode/utf8\"\n\nvar _ = utf8.RuneLen\n",
	"b/tag.go":      "//go:build ignore\n\npackage b\n\nimport \"unicode\"\n\nvar _ = unicode.IsUpper\n",
	"e/e.go":        "package e\n\nimport _ \"embed\"\n\n//go:embed data.txt\nvar Data string\n",
	"e/data.txt":    "data\n",
	"nested/go.mod": "module example.com/app/nested\n\ngo 1.26.1\n",
	"nested/n.go":   "package nested\n",
}

func writeGraphDiffModule(t *testing.T, root string) {
	t.Helper()
	for rel, contents := range graphDiffModule {
		writeTestFile(t, filepath.Join(root, filepath.FromSlash(rel)), contents)
	}
}

// graphPrep snapshots root the way prepareCache does: a fresh manifest, a
// captured Go context, the manifest's GSX package dirs and the cache dir.
func graphPrep(t *testing.T, root string) cachePreparation {
	t.Helper()
	manifest, err := sourceview.Build(sourceview.BuildOptions{ModuleRoot: root, ModulePath: graphTestModule})
	if err != nil {
		t.Fatal(err)
	}
	dirs := make([]string, 0)
	for _, dir := range manifest.PackageDirs() {
		dirs = append(dirs, dir)
	}
	sort.Strings(dirs)
	prep := cachePreparation{root: root, dirs: dirs, goContext: codegen.CaptureGoCommandContext(root), manifest: manifest}
	prep.cacheDir, _ = cacheDir()
	return prep
}

// liveGraph is what go list prints for prep right now.
func liveGraph(prep cachePreparation) (sourceview.Graph, error) {
	patterns, err := graphQueryPatterns(prep.manifest, prep.dirs, nil)
	if err != nil {
		return nil, err
	}
	return listGraph(prep.goContext, prep.manifest, patterns)
}

// graphReadFieldsEqual compares every field gsx reads from a go list graph
// (everything but Deps) and describes the first difference.
func graphReadFieldsEqual(got, want sourceview.Graph) (string, bool) {
	var paths []string
	for importPath := range got {
		paths = append(paths, importPath)
	}
	for importPath := range want {
		if _, ok := got[importPath]; !ok {
			paths = append(paths, importPath)
		}
	}
	sort.Strings(paths)
	for _, importPath := range paths {
		g, gok := got[importPath]
		w, wok := want[importPath]
		if gok != wok {
			return fmt.Sprintf("package %s: present in cached=%v live=%v", importPath, gok, wok), false
		}
		g.Deps, w.Deps = nil, nil
		if !reflect.DeepEqual(normalizePackage(g), normalizePackage(w)) {
			return fmt.Sprintf("package %s:\n cached %+v %+v\n   live %+v %+v", importPath, g, g.Module, w, w.Module), false
		}
	}
	return "", true
}

// normalizePackage treats nil and empty lists alike: the stored JSON form
// does not preserve the difference and gsx does not read it.
func normalizePackage(pkg sourceview.PackageMetadata) sourceview.PackageMetadata {
	pkg = copyPackage(pkg)
	for _, list := range []*[]string{&pkg.Imports, &pkg.GoFiles, &pkg.CgoFiles, &pkg.CompiledGoFiles, &pkg.EmbedFiles} {
		if len(*list) == 0 {
			*list = nil
		}
	}
	return pkg
}

// checkCachedAgainstLive is the differential gate: a cached call either
// agrees with a live go list at the same moment, or (when go list fails)
// fails too and is not a hit.
func checkCachedAgainstLive(t *testing.T, label string, prep cachePreparation, graph sourceview.Graph, outcome graphCacheOutcome, err error) {
	t.Helper()
	live, liveErr := liveGraph(prep)
	switch {
	case liveErr != nil && outcome.Hit():
		t.Fatalf("%s: false hit: live go list fails (%v) but the cache returned a graph", label, liveErr)
	case liveErr != nil && err == nil:
		t.Fatalf("%s: live go list fails (%v) but the cached call succeeded with outcome %+v", label, liveErr, outcome)
	case liveErr != nil:
		return
	case err != nil:
		t.Fatalf("%s: cached call failed (%v, outcome %+v) but live go list succeeds", label, err, outcome)
	}
	if diff, ok := graphReadFieldsEqual(graph, live); !ok {
		t.Fatalf("%s: outcome %+v returned a graph that differs from live go list: %s", label, outcome, diff)
	}
}

func TestGraphCacheDifferential(t *testing.T) {
	t.Setenv("GSXCACHE", t.TempDir())
	write := func(rel, contents string) func(*testing.T, string) {
		return func(t *testing.T, root string) {
			writeTestFile(t, filepath.Join(root, filepath.FromSlash(rel)), contents)
		}
	}
	remove := func(rel string) func(*testing.T, string) {
		return func(t *testing.T, root string) {
			if err := os.Remove(filepath.Join(root, filepath.FromSlash(rel))); err != nil {
				t.Fatal(err)
			}
		}
	}
	steps := func(edits ...func(*testing.T, string)) func(*testing.T, string) {
		return func(t *testing.T, root string) {
			for _, edit := range edits {
				edit(t, root)
			}
		}
	}
	rows := []struct {
		name string
		// setup, when set, runs before the base call.
		setup func(*testing.T, string)
		edit  func(*testing.T, string)
		// wantHit: both calls after the edit must hit.
		wantHit bool
		// wantReason, when set, is the second call's outcome; wantDetail, when
		// set, is a substring of its detail.
		wantReason graphCacheReason
		wantDetail string
	}{
		{name: "body edit", edit: write("a/a.go", "package a\n\nimport (\n\t_ \"example.com/app/b\"\n\t_ \"example.com/app/e\"\n)\n\nfunc Body() int { return 2 }\n"), wantHit: true},
		{name: "embed content edit", edit: write("e/data.txt", "other data\n"), wantHit: true},
		{name: "import added", edit: write("b/b.go", "package b\n\nimport (\n\t\"strings\"\n\t\"unicode\"\n)\n\nvar Upper = strings.ToUpper\nvar _ = unicode.IsUpper\n")},
		{name: "import removed", edit: write("a/a.go", "package a\n\nimport _ \"example.com/app/b\"\n\nfunc Body() int { return 1 }\n")},
		{name: "import reordered", edit: write("a/a.go", "package a\n\nimport (\n\t_ \"example.com/app/e\"\n\t_ \"example.com/app/b\"\n)\n\nfunc Body() int { return 1 }\n")},
		{name: "build tag changed", edit: write("b/tag.go", "//go:build !ignore\n\npackage b\n\nimport \"unicode\"\n\nvar _ = unicode.IsUpper\n")},
		{name: "file added", edit: write("b/extra.go", "package b\n\nimport \"strconv\"\n\nvar _ = strconv.Itoa\n")},
		{name: "file deleted", edit: remove("b/c.go")},
		{name: "excluded file deleted", edit: remove("b/tag.go")},
		{name: "file renamed", edit: steps(remove("b/c.go"), write("b/d.go", graphDiffModule["b/c.go"]))},
		{name: "GOOS file added darwin", edit: write("b/x_darwin.go", "package b\n\nimport \"os\"\n\nvar _ = os.Getpid\n")},
		{name: "GOOS file added linux", edit: write("b/x_linux.go", "package b\n\nimport \"os\"\n\nvar _ = os.Getpid\n")},
		{name: "package clause changed", edit: write("b/c.go", "package bee\n\nimport \"unicode/utf8\"\n\nvar _ = utf8.RuneLen\n")},
		{name: "nested go.mod added", edit: write("a/go.mod", "module example.com/app/a\n\ngo 1.26.1\n")},
		{name: "nested go.mod added under dependency", edit: write("b/go.mod", "module example.com/app/b\n\ngo 1.26.1\n")},
		{
			name: "go.mod requires nested module through replace",
			edit: steps(
				write("go.mod", "module example.com/app\n\ngo 1.26.1\n\nrequire example.com/app/nested v0.0.0\n\nreplace example.com/app/nested => ./nested\n"),
				write("a/a.go", "package a\n\nimport (\n\t_ \"example.com/app/b\"\n\t_ \"example.com/app/e\"\n\t_ \"example.com/app/nested\"\n)\n\nfunc Body() int { return 1 }\n"),
			),
			wantReason: graphUncacheable,
		},
		{name: "go.mod go version edited", edit: write("go.mod", "module example.com/app\n\ngo 1.27\n")},
		{
			// go list reads the alternate module file, which neither the key
			// nor the witness covers.
			name: "GOFLAGS -modfile edited",
			setup: func(t *testing.T, root string) {
				writeTestFile(t, filepath.Join(root, "alt.mod"), graphDiffModule["go.mod"])
				// The physical spelling keeps Module.GoMod under GOMOD's
				// directory, so only the -modfile refusal stops the store.
				physical, err := filepath.EvalSymlinks(root)
				if err != nil {
					t.Fatal(err)
				}
				t.Setenv("GOFLAGS", "-modfile="+filepath.Join(physical, "alt.mod"))
			},
			edit:       write("alt.mod", "module example.com/app\n\ngo 1.27\n"),
			wantReason: graphUncacheable,
			wantDetail: "-modfile",
		},
		{name: "go.sum added", edit: write("go.sum", "example.com/other v1.0.0/go.mod h1:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=\n")},
		{name: "new gsx package", edit: write("w/w.gsx", "package w\n\nimport _ \"example.com/app/b\"\n\ncomponent W() { <p>w</p> }\n")},
		{name: "gsx imports changed", edit: write("a/a.gsx", "package a\n\nimport _ \"example.com/app/nested\"\n\ncomponent A() { <p>a</p> }\n")},
		{name: "unparseable header", edit: write("b/b.go", "package b\n\nimport (\n")},
		{name: "c file added", edit: write("b/x.c", "int x;\n")},
		{name: "embed target deleted", edit: remove("e/data.txt")},
		{name: "embed pattern edited", edit: steps(write("e/extra.txt", "extra\n"), write("e/e.go", "package e\n\nimport _ \"embed\"\n\n//go:embed *.txt\nvar Data string\n"))},
		{name: "embed file added under pattern", edit: steps(
			write("e/e.go", "package e\n\nimport \"embed\"\n\n//go:embed *.txt\nvar Data embed.FS\n"),
			write("e/more.txt", "more\n"),
		)},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			root := t.TempDir()
			writeGraphDiffModule(t, root)
			if row.setup != nil {
				row.setup(t, root)
			}

			prep := graphPrep(t, root)
			graph, outcome, err := loadGraphCached(prep, nil)
			checkCachedAgainstLive(t, "base", prep, graph, outcome, err)

			row.edit(t, root)
			for call := 1; call <= 2; call++ {
				prep := graphPrep(t, root)
				graph, outcome, err := loadGraphCached(prep, nil)
				label := fmt.Sprintf("call %d after edit", call)
				checkCachedAgainstLive(t, label, prep, graph, outcome, err)
				if row.wantHit && !outcome.Hit() {
					t.Fatalf("%s: outcome %+v, want a hit", label, outcome)
				}
				if call == 2 && row.wantReason != "" && (outcome.Reason != row.wantReason || !strings.Contains(outcome.Detail, row.wantDetail)) {
					t.Fatalf("%s: outcome %+v, want %s (%s)", label, outcome, row.wantReason, row.wantDetail)
				}
			}
		})
	}
}

func TestGraphCacheSharedAcrossCheckouts(t *testing.T) {
	t.Setenv("GSXCACHE", t.TempDir())
	parent := t.TempDir()
	first := filepath.Join(parent, "first")
	second := filepath.Join(parent, "second")
	link := filepath.Join(parent, "link")
	writeGraphDiffModule(t, first)
	writeGraphDiffModule(t, second)
	if err := os.Symlink(first, link); err != nil {
		t.Fatal(err)
	}

	prep := graphPrep(t, first)
	graph, outcome, err := loadGraphCached(prep, nil)
	checkCachedAgainstLive(t, "first", prep, graph, outcome, err)
	if outcome.Reason != graphEntryMissing {
		t.Fatalf("first checkout outcome %+v, want %s", outcome, graphEntryMissing)
	}
	for _, root := range []string{second, link} {
		prep := graphPrep(t, root)
		graph, outcome, err := loadGraphCached(prep, nil)
		checkCachedAgainstLive(t, root, prep, graph, outcome, err)
		if !outcome.Hit() {
			t.Fatalf("%s: outcome %+v, want a hit from the first checkout's entry", root, outcome)
		}
		goMod, err := prep.goContext.GoMod()
		if err != nil {
			t.Fatal(err)
		}
		a := graph[graphTestModule+"/a"]
		if want := filepath.Join(filepath.Dir(goMod), "a"); a.Dir != want {
			t.Fatalf("%s: a.Dir = %s, want %s", root, a.Dir, want)
		}
	}
}

func TestGraphCacheHitReadsNothing(t *testing.T) {
	t.Setenv("GSXCACHE", t.TempDir())
	root := t.TempDir()
	writeGraphDiffModule(t, root)
	prep := graphPrep(t, root)
	if _, outcome, err := loadGraphCached(prep, nil); err != nil || outcome.Reason != graphEntryMissing {
		t.Fatalf("cold call: outcome %+v, err %v", outcome, err)
	}

	prep = graphPrep(t, root)
	body := filepath.Join(root, "a", "a.go")
	if err := os.Chmod(body, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(body, 0o644) })
	if _, outcome, err := loadGraphCached(prep, nil); err != nil || !outcome.Hit() {
		t.Fatalf("warm call with an unreadable body file: outcome %+v, err %v, want a hit that reads no source", outcome, err)
	}
}

func TestGraphCacheStoreSkippedWhenFilesChange(t *testing.T) {
	importUnicode := "package b\n\nimport (\n\t\"strings\"\n\t\"unicode\"\n)\n\nvar Upper = strings.ToUpper\nvar _ = unicode.IsUpper\n"
	write := func(rel, contents string) func(*testing.T, string) {
		return func(t *testing.T, root string) {
			writeTestFile(t, filepath.Join(root, filepath.FromSlash(rel)), contents)
		}
	}
	remove := func(rel string) func(*testing.T, string) {
		return func(t *testing.T, root string) {
			if err := os.Remove(filepath.Join(root, filepath.FromSlash(rel))); err != nil {
				t.Fatal(err)
			}
		}
	}
	rows := []struct {
		name string
		// before and after run around go list; restore, when set, runs before
		// the second call to return the tree to the snapshot's state.
		before, after, restore func(*testing.T, string)
	}{
		{name: "header edited", after: write("b/b.go", importUnicode), restore: write("b/b.go", graphDiffModule["b/b.go"])},
		// Content re-reads see the original bytes again; only stat identity
		// tells that go list read the edit.
		{name: "header edited and reverted", before: write("b/b.go", importUnicode), after: write("b/b.go", graphDiffModule["b/b.go"])},
		{name: "go.mod edited and reverted", before: write("go.mod", "module example.com/app\n\ngo 1.27\n"), after: write("go.mod", graphDiffModule["go.mod"])},
		{name: "file added and removed", before: write("b/extra.go", "package b\n\nimport \"strconv\"\n\nvar _ = strconv.Itoa\n"), after: remove("b/extra.go")},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			t.Setenv("GSXCACHE", t.TempDir())
			root := t.TempDir()
			writeGraphDiffModule(t, root)
			hook := func(edit func(*testing.T, string)) func() {
				if edit == nil {
					return nil
				}
				return func() { edit(t, root) }
			}
			beforeListGraph, afterListGraph = hook(row.before), hook(row.after)
			t.Cleanup(func() { beforeListGraph, afterListGraph = nil, nil })

			prep := graphPrep(t, root)
			if _, outcome, err := loadGraphCached(prep, nil); err != nil || outcome.Reason != graphStoreSkipped {
				t.Fatalf("files changed during go list: outcome %+v, err %v, want %s", outcome, err, graphStoreSkipped)
			}
			beforeListGraph, afterListGraph = nil, nil
			if row.restore != nil {
				row.restore(t, root)
			}

			// The skipped graph must not have been stored under the
			// snapshot's witness.
			prep = graphPrep(t, root)
			graph, outcome, err := loadGraphCached(prep, nil)
			checkCachedAgainstLive(t, "next run", prep, graph, outcome, err)
			if outcome.Reason != graphEntryMissing {
				t.Fatalf("next run: outcome %+v, want %s", outcome, graphEntryMissing)
			}
		})
	}
}

func TestGraphCacheUnwritableDir(t *testing.T) {
	cache := t.TempDir()
	if err := os.Chmod(cache, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(cache, 0o755) })
	t.Setenv("GSXCACHE", cache)
	root := t.TempDir()
	writeGraphDiffModule(t, root)

	prep := graphPrep(t, root)
	graph, outcome, err := loadGraphCached(prep, nil)
	checkCachedAgainstLive(t, "read-only cache", prep, graph, outcome, err)
	if outcome.Reason != graphStoreFailed || outcome.Detail == "" {
		t.Fatalf("read-only cache: outcome %+v, want %s with the write error", outcome, graphStoreFailed)
	}
}

func TestGraphCacheDisabledRunsGoList(t *testing.T) {
	t.Setenv("GSXCACHE", "off")
	root := t.TempDir()
	writeGraphDiffModule(t, root)
	prep := graphPrep(t, root)
	graph, outcome, err := loadGraphCached(prep, nil)
	checkCachedAgainstLive(t, "cache off", prep, graph, outcome, err)
	if outcome.Reason != graphUncacheable || !strings.Contains(outcome.Detail, "cache disabled") {
		t.Fatalf("cache off: outcome %+v, want %s (cache disabled)", outcome, graphUncacheable)
	}
}

func TestGraphCacheStoreSkippedWhenKeyInputsChange(t *testing.T) {
	t.Setenv("GSXCACHE", t.TempDir())
	root := t.TempDir()
	writeGraphDiffModule(t, root)
	goMod := filepath.Join(root, "go.mod")
	beforeListGraph = func() {
		writeTestFile(t, goMod, "module example.com/app\n\ngo 1.27\n")
	}
	t.Cleanup(func() { beforeListGraph = nil })

	prep := graphPrep(t, root)
	_, outcome, err := loadGraphCached(prep, nil)
	if err != nil || outcome.Reason != graphStoreSkipped || !strings.Contains(outcome.Detail, "key inputs changed") {
		t.Fatalf("go.mod edited before go list: outcome %+v, err %v, want %s (key inputs changed)", outcome, err, graphStoreSkipped)
	}
	beforeListGraph = nil

	// Back to the go.mod the key was computed from: the graph listed under
	// the edited go.mod must not have been stored under that key.
	writeTestFile(t, goMod, graphDiffModule["go.mod"])
	prep = graphPrep(t, root)
	graph, outcome, err := loadGraphCached(prep, nil)
	checkCachedAgainstLive(t, "after restoring go.mod", prep, graph, outcome, err)
	if outcome.Reason != graphEntryMissing {
		t.Fatalf("after restoring go.mod: outcome %+v, want %s", outcome, graphEntryMissing)
	}
}
