package gen

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gsxhq/gsx/internal/codegen"
	"github.com/gsxhq/gsx/internal/sourceview"
)

func projectionForTest(t *testing.T, root string, graph sourceview.Graph) *sourceview.CacheProjection {
	t.Helper()
	moduleDir, modulePath, err := moduleRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := sourceview.Build(sourceview.BuildOptions{ModuleRoot: moduleDir, ModulePath: modulePath})
	if err != nil {
		t.Fatal(err)
	}
	projection, err := sourceview.NewCacheProjection(manifest, graph)
	if err != nil {
		t.Fatal(err)
	}
	return projection
}

func computeTestKey(t *testing.T, dir, root string, graph sourceview.Graph, config cacheKeyConfig) (string, error) {
	t.Helper()
	return computeKey(dir, projectionForTest(t, root, graph), config)
}

// TestBuildContextKeySensitivity is the core regression guard for Fix 1:
// a different buildCtx string must produce a different cache key, and the same
// buildCtx must produce the same key (stability).
func TestBuildContextKeySensitivity(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	os.WriteFile(filepath.Join(tmp, "go.mod"), []byte("module ex/bctx\n\ngo 1.26\n"), 0o644)
	os.MkdirAll(filepath.Join(tmp, "a"), 0o755)
	os.WriteFile(filepath.Join(tmp, "a", "a.go"), []byte("package a\n"), 0o644)
	os.WriteFile(filepath.Join(tmp, "a", "a.gsx"), []byte("package a\ncomponent View() { <p/> }\n"), 0o644)

	graph, err := loadGraph(tmp)
	if err != nil {
		t.Fatal(err)
	}
	aDir := filepath.Join(tmp, "a")

	bctxDarwin := "go1.26\ndarwin\namd64\n0\n\n"
	bctxLinux := "go1.26\nlinux\namd64\n0\n\n"

	k1a, err := computeTestKey(t, aDir, tmp, graph, cacheKeyConfig{buildContext: bctxDarwin, codegenIdentity: "gen-test"})
	if err != nil {
		t.Fatal(err)
	}
	k1b, err := computeTestKey(t, aDir, tmp, graph, cacheKeyConfig{buildContext: bctxDarwin, codegenIdentity: "gen-test"})
	if err != nil {
		t.Fatal(err)
	}
	if k1a != k1b {
		t.Error("same buildCtx must produce the same key (unstable)")
	}

	k2, err := computeTestKey(t, aDir, tmp, graph, cacheKeyConfig{buildContext: bctxLinux, codegenIdentity: "gen-test"})
	if err != nil {
		t.Fatal(err)
	}
	if k1a == k2 {
		t.Error("different buildCtx (darwin vs linux) must produce different keys")
	}
}

func TestComputeKeyDepClosure(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	os.WriteFile(filepath.Join(tmp, "go.mod"), []byte("module ex/app\n\ngo 1.26\n"), 0o644)
	mk := func(p, content string) {
		os.MkdirAll(filepath.Join(tmp, p), 0o755)
		os.WriteFile(filepath.Join(tmp, p, p+".go"), []byte(content), 0o644)
	}
	mk("a", "package a\n\nfunc A() string { return \"a\" }\n")
	mk("b", "package b\n\nimport \"ex/app/a\"\n\nfunc B() string { return a.A() }\n")
	mk("c", "package c\n\nfunc C() string { return \"c\" }\n")
	os.WriteFile(filepath.Join(tmp, "b", "b.gsx"), []byte("package b\nimport \"ex/app/a\"\ncomponent View() { <p/> }\n"), 0o644)
	graph, err := loadGraph(tmp)
	if err != nil {
		t.Fatal(err)
	}
	bDir := filepath.Join(tmp, "b")
	config := cacheKeyConfig{buildContext: "go1.26\nlinux\namd64\n0\n\n", codegenIdentity: "gen-test"}
	key1, err := computeTestKey(t, bDir, tmp, graph, config)
	if err != nil {
		t.Fatal(err)
	}
	// edit dependency a -> b's key must change
	os.WriteFile(filepath.Join(tmp, "a", "a.go"), []byte("package a\n\nfunc A() string { return \"A2\" }\n"), 0o644)
	key2, _ := computeTestKey(t, bDir, tmp, loadGraphMust(t, tmp), config)
	if key1 == key2 {
		t.Error("editing dependency a must change b's key")
	}
	// edit unrelated c -> b's key must NOT change
	os.WriteFile(filepath.Join(tmp, "c", "c.go"), []byte("package c\n\nfunc C() string { return \"C2\" }\n"), 0o644)
	key3, _ := computeTestKey(t, bDir, tmp, loadGraphMust(t, tmp), config)
	if key3 != key2 {
		t.Error("editing unrelated c must NOT change b's key")
	}
}

func TestComputeKeyConfiguredSourceClosure(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	write := func(rel, source string) {
		t.Helper()
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(source), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module ex/configured\n\ngo 1.26.1\n")
	write("views/views.go", "package views\n")
	write("views/views.gsx", "package views\ncomponent View() { <p/> }\n")
	write("model/model.go", "package model\n\ntype Value string\n")
	write("filters/filters.go", "package filters\n\nimport \"ex/configured/model\"\n\nfunc Format(value model.Value) string { return string(value) }\n")
	write("merger/merger.go", "package merger\n\nfunc Merge(values []string) string { return \"\" }\n")

	viewsDir := filepath.Join(root, "views")
	key := func(filters []string, aliases []codegen.FilterAlias, merger *codegen.ClassMergerRef) string {
		t.Helper()
		graph := loadGraphWithRootsMust(t, root, configuredPackagePaths(filters, aliases, nil, merger))
		value, err := computeTestKey(t, viewsDir, root, graph, cacheKeyConfig{
			buildContext:          "build",
			codegenIdentity:       "generator",
			filterPackages:        filters,
			aliases:               aliases,
			classifierFingerprint: "classifier",
			classMerger:           merger,
		})
		if err != nil {
			t.Fatal(err)
		}
		return value
	}

	filterPath := "ex/configured/filters"
	filterBefore := key([]string{filterPath}, nil, nil)
	aliasBefore := key(nil, []codegen.FilterAlias{{Name: "format", PkgPath: filterPath, FuncName: "Format"}}, nil)
	write("model/model.go", "package model\n\ntype Value int\n")
	if after := key([]string{filterPath}, nil, nil); after == filterBefore {
		t.Fatal("transitive source edit behind a module-local filter did not change the cache key")
	}
	if after := key(nil, []codegen.FilterAlias{{Name: "format", PkgPath: filterPath, FuncName: "Format"}}, nil); after == aliasBefore {
		t.Fatal("transitive source edit behind a module-local filter alias did not change the cache key")
	}

	merger := &codegen.ClassMergerRef{PkgPath: "ex/configured/merger", FuncName: "Merge"}
	mergerBefore := key(nil, nil, merger)
	write("merger/merger.go", "package merger\n\nfunc Merge(values []string) string { return \"changed\" }\n")
	if after := key(nil, nil, merger); after == mergerBefore {
		t.Fatal("module-local class merger source edit did not change the cache key")
	}
}

func TestComputeKeyFilterAliasIdentity(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	write := func(rel, source string) {
		t.Helper()
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(source), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module ex/alias-identity\n\ngo 1.26.1\n")
	write("views/views.gsx", "package views\n\ncomponent View() { <p/> }\n")
	write("filters/filters.go", "package filters\n\nfunc One(value string) string { return value }\nfunc Two(value string) string { return value }\n")

	viewsDir := filepath.Join(root, "views")
	filterPath := "ex/alias-identity/filters"
	graph := loadGraphWithRootsMust(t, root, []string{filterPath})
	projection := projectionForTest(t, root, graph)
	key := func(aliases []codegen.FilterAlias) string {
		t.Helper()
		if got := fmt.Sprint(configuredPackagePaths(nil, aliases, nil, nil)); got != fmt.Sprint([]string{filterPath}) {
			t.Fatalf("configured source roots = %s, want only %s", got, filterPath)
		}
		value, err := computeKey(viewsDir, projection, cacheKeyConfig{
			buildContext:          "build",
			codegenIdentity:       "generator",
			aliases:               aliases,
			classifierFingerprint: "classifier",
		})
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	alias := func(name, function string) codegen.FilterAlias {
		return codegen.FilterAlias{Name: name, PkgPath: filterPath, FuncName: function}
	}

	base := key([]codegen.FilterAlias{alias("format", "One")})
	if same := key([]codegen.FilterAlias{alias("format", "One")}); same != base {
		t.Fatal("identical filter alias identity produced an unstable cache key")
	}
	if changedName := key([]codegen.FilterAlias{alias("render", "One")}); changedName == base {
		t.Fatal("changing a filter alias name did not change the cache key")
	}
	if changedFunction := key([]codegen.FilterAlias{alias("format", "Two")}); changedFunction == base {
		t.Fatal("changing a filter alias function did not change the cache key")
	}
	oneThenTwo := key([]codegen.FilterAlias{alias("format", "One"), alias("format", "Two")})
	twoThenOne := key([]codegen.FilterAlias{alias("format", "Two"), alias("format", "One")})
	if oneThenTwo == twoThenOne {
		t.Fatal("reversing last-wins filter alias registrations did not change the cache key")
	}
}

func TestComputeKeyHashesReachableLocalReplacement(t *testing.T) {
	t.Parallel()
	parent := t.TempDir()
	root := filepath.Join(parent, "app")
	replacement := filepath.Join(parent, "replacement")
	for _, dir := range []string{root, replacement} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(replacement, "go.mod"), []byte("module example.com/replacement\n\ngo 1.26.1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	replacementSource := filepath.Join(replacement, "value.go")
	if err := os.WriteFile(replacementSource, []byte("package replacement\n\ntype Value string\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	inactiveSource := filepath.Join(replacement, "inactive.go")
	if err := os.WriteFile(inactiveSource, []byte("//go:build inactive\n\npackage replacement\n\ntype Inactive string\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/app\n\ngo 1.26.1\n\nrequire example.com/replacement v0.0.0\nreplace example.com/replacement => ../replacement\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	viewsDir := filepath.Join(root, "views")
	if err := os.MkdirAll(viewsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(viewsDir, "views.go"), []byte("package views\n\nimport \"example.com/replacement\"\n\ntype Value = replacement.Value\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(viewsDir, "views.gsx"), []byte("package views\nimport \"example.com/replacement\"\ncomponent View() { <p/> }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	key := func() string {
		t.Helper()
		value, err := computeTestKey(t, viewsDir, root, loadGraphMust(t, root), cacheKeyConfig{
			buildContext:          "build",
			codegenIdentity:       "generator",
			classifierFingerprint: "classifier",
		})
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	before := key()
	if err := os.WriteFile(inactiveSource, []byte("//go:build inactive\n\npackage replacement\n\ntype Inactive int\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if after := key(); after != before {
		t.Fatal("build-excluded replacement source changed the cache key")
	}
	if err := os.WriteFile(filepath.Join(replacement, "go.mod"), []byte("module example.com/replacement\n\ngo 1.25\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	afterMod := key()
	if afterMod == before {
		t.Fatal("reachable replacement go.mod edit did not change the cache key")
	}
	if err := os.WriteFile(filepath.Join(replacement, "go.sum"), []byte("example.com/unused v1.0.0 h1:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	afterSum := key()
	if afterSum == afterMod {
		t.Fatal("reachable replacement go.sum edit did not change the cache key")
	}
	if err := os.WriteFile(replacementSource, []byte("package replacement\n\ntype Value int\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if after := key(); after == afterSum {
		t.Fatal("reachable source edit behind a local replace did not change the cache key")
	}
}

func TestComputeKeyRejectsMainModuleCgo(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/app\n\ngo 1.26.1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "views")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "views.gsx"), []byte("package views\n\ncomponent View() { <p/> }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	graph := map[string]pkgInfo{
		"example.com/app/views": {
			ImportPath: "example.com/app/views",
			Dir:        dir,
			CgoFiles:   []string{"bridge.go"},
			Module:     &pkgModule{Path: "example.com/app", Dir: root, Main: true},
		},
	}
	_, err := computeTestKey(t, dir, root, graph, cacheKeyConfig{
		buildContext:          "build",
		codegenIdentity:       "generator",
		classifierFingerprint: "classifier",
	})
	if !errors.Is(err, sourceview.ErrUncacheableCgo) {
		t.Fatalf("computeKey error = %v, want ErrUncacheableCgo", err)
	}
	if !strings.Contains(err.Error(), `"example.com/app/views"`) {
		t.Fatalf("computeKey error = %v, want package detail", err)
	}
}

func TestComputeKeyCachesImmutableCgoDependency(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/app\n\ngo 1.26.1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "views")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "views.gsx"), []byte("package views\n\ncomponent View() { <p/> }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	graph := map[string]pkgInfo{
		"example.com/app/views": {
			ImportPath: "example.com/app/views",
			Dir:        dir,
			Imports:    []string{"example.com/immutable/cgo"},
			Module:     &pkgModule{Path: "example.com/app", Dir: root, Main: true},
		},
		"example.com/immutable/cgo": {
			ImportPath: "example.com/immutable/cgo",
			CgoFiles:   []string{"bridge.go"},
			Module:     &pkgModule{Path: "example.com/immutable", Version: "v1.2.3"},
		},
	}
	config := cacheKeyConfig{
		buildContext:          "build",
		codegenIdentity:       "generator",
		classifierFingerprint: "classifier",
	}
	first, err := computeTestKey(t, dir, root, graph, config)
	if err != nil {
		t.Fatalf("first computeKey: %v", err)
	}
	second, err := computeTestKey(t, dir, root, graph, config)
	if err != nil {
		t.Fatalf("second computeKey: %v", err)
	}
	if first != second {
		t.Fatalf("immutable cgo key changed: first %s, second %s", first, second)
	}
}

// computeKeyForTest invokes computeKey with a minimal fixed graph/module setup,
// varying only the classMerger parameter. The fixed content ensures the key is
// stable across calls (same own hash, no in-module deps), so only classMerger
// can cause the key to differ.
func computeKeyForTest(t *testing.T, classMerger *codegen.ClassMergerRef) (string, error) {
	t.Helper()
	tmp := t.TempDir()
	os.WriteFile(filepath.Join(tmp, "go.mod"), []byte("module ex/cmtest\n\ngo 1.26\n"), 0o644)
	os.MkdirAll(filepath.Join(tmp, "a"), 0o755)
	os.WriteFile(filepath.Join(tmp, "a", "a.go"), []byte("package a\n"), 0o644)
	os.WriteFile(filepath.Join(tmp, "a", "a.gsx"), []byte("package a\ncomponent View() { <p/> }\n"), 0o644)
	os.MkdirAll(filepath.Join(tmp, "twcfg"), 0o755)
	os.WriteFile(filepath.Join(tmp, "twcfg", "merge.go"), []byte("package twcfg\nfunc Merge(values []string) string { return \"\" }\nfunc Other(values []string) string { return \"\" }\n"), 0o644)
	var roots []string
	if classMerger != nil {
		roots = append(roots, classMerger.PkgPath)
	}
	graph := loadGraphWithRootsMust(t, tmp, roots)
	aDir := filepath.Join(tmp, "a")
	return computeTestKey(t, aDir, tmp, graph, cacheKeyConfig{
		buildContext:    "go1.26\nlinux\namd64\n0\n\n",
		codegenIdentity: "gen-test",
		classMerger:     classMerger,
	})
}

// TestComputeKeyVariesByClassMerger is the regression guard for Task 5:
// changing class_merger must bust the incremental cache.
func TestComputeKeyVariesByClassMerger(t *testing.T) {
	t.Parallel()
	base := func(ref *codegen.ClassMergerRef) string {
		k, err := computeKeyForTest(t, ref)
		if err != nil {
			t.Fatal(err)
		}
		return k
	}
	none := base(nil)
	a := base(&codegen.ClassMergerRef{PkgPath: "ex/cmtest/twcfg", FuncName: "Merge"})
	b := base(&codegen.ClassMergerRef{PkgPath: "ex/cmtest/twcfg", FuncName: "Other"})
	if none == a || a == b {
		t.Fatalf("cache key must vary by merger: none=%s a=%s b=%s", none, a, b)
	}
}

// computeKeyForRenderersTest invokes computeKey with a minimal fixed
// graph/module setup, varying only the renderers parameter. The fixed content
// ensures the key is stable across calls (same own hash, no in-module deps),
// so only renderers can cause the key to differ.
func computeKeyForRenderersTest(t *testing.T, renderers []codegen.RendererAlias) string {
	t.Helper()
	tmp := t.TempDir()
	os.WriteFile(filepath.Join(tmp, "go.mod"), []byte("module ex/rndtest\n\ngo 1.26\n"), 0o644)
	os.MkdirAll(filepath.Join(tmp, "a"), 0o755)
	os.WriteFile(filepath.Join(tmp, "a", "a.go"), []byte("package a\n"), 0o644)
	os.WriteFile(filepath.Join(tmp, "a", "a.gsx"), []byte("package a\ncomponent View() { <p/> }\n"), 0o644)
	os.MkdirAll(filepath.Join(tmp, "f"), 0o755)
	os.WriteFile(filepath.Join(tmp, "f", "render.go"), []byte("package f\nfunc RenderA(value string) string { return value }\nfunc RenderB(value string) string { return value }\nfunc RenderBOther(value string) string { return value }\n"), 0o644)
	graph := loadGraphWithRootsMust(t, tmp, configuredPackagePaths(nil, nil, renderers, nil))
	aDir := filepath.Join(tmp, "a")
	k, err := computeTestKey(t, aDir, tmp, graph, cacheKeyConfig{
		buildContext:    "go1.26\nlinux\namd64\n0\n\n",
		codegenIdentity: "gen-test",
		renderers:       renderers,
	})
	if err != nil {
		t.Fatal(err)
	}
	return k
}

// TestComputeKeyRenderers is the regression guard for the renderers= pin: a
// changed registration must bust the cache, but the pin is resolved
// last-wins-per-TypeKey and then sorted by TypeKey, so swapping the
// REGISTRATION ORDER of two distinct TypeKeys must NOT change the key (the
// renderer table is a per-key map — unlike aliases=, order there is not
// itself meaning).
func TestComputeKeyRenderers(t *testing.T) {
	t.Parallel()
	rA := codegen.RendererAlias{TypeKey: "example.com/p.A", PkgPath: "ex/rndtest/f", FuncName: "RenderA"}
	rB := codegen.RendererAlias{TypeKey: "example.com/p.B", PkgPath: "ex/rndtest/f", FuncName: "RenderB"}
	rBOther := codegen.RendererAlias{TypeKey: "example.com/p.B", PkgPath: "ex/rndtest/f", FuncName: "RenderBOther"}

	none := computeKeyForRenderersTest(t, nil)
	withA := computeKeyForRenderersTest(t, []codegen.RendererAlias{rA})
	if none == withA {
		t.Fatal("registering a renderer must change the cache key")
	}

	withAThenOther := computeKeyForRenderersTest(t, []codegen.RendererAlias{rA, rBOther})
	withAThenB := computeKeyForRenderersTest(t, []codegen.RendererAlias{rA, rB})
	if withAThenOther == withAThenB {
		t.Fatal("changing a renderer registration (same TypeKey, different func) must change the cache key")
	}

	orderAB := computeKeyForRenderersTest(t, []codegen.RendererAlias{rA, rB})
	orderBA := computeKeyForRenderersTest(t, []codegen.RendererAlias{rB, rA})
	if orderAB != orderBA {
		t.Fatal("swapping registration order of two distinct TypeKeys must NOT change the cache key (order-independent pin)")
	}

	t.Run("module-local source", func(t *testing.T) {
		root := t.TempDir()
		write := func(rel, src string) {
			t.Helper()
			path := filepath.Join(root, filepath.FromSlash(rel))
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		write("go.mod", "module ex/rnddeps\n\ngo 1.26.1\n")
		write("views/views.go", "package views\n")
		write("views/views.gsx", "package views\ncomponent View() { <p/> }\n")
		write("renderers/renderers.gsx", "package renderers\n\nfunc RenderA(v string) string { return v }\n")

		viewsDir := filepath.Join(root, "views")
		graph := loadGraphMust(t, root)
		renderers := []codegen.RendererAlias{{
			TypeKey:  "example.com/p.A",
			PkgPath:  "ex/rnddeps/renderers",
			FuncName: "RenderA",
		}}
		key := func() string {
			t.Helper()
			got, err := computeTestKey(t, viewsDir, root, graph, cacheKeyConfig{buildContext: "bctx", codegenIdentity: "gen-test", renderers: renderers})
			if err != nil {
				t.Fatal(err)
			}
			return got
		}
		before := key()
		write("renderers/renderers.gsx", "package renderers\n\nfunc RenderA(v string) string { return \"changed: \" + v }\n")
		after := key()
		if before == after {
			t.Fatal("editing an unimported module-local renderer package must change the consumer cache key")
		}
	})

	t.Run("external path adds no local hash", func(t *testing.T) {
		root := t.TempDir()
		external := t.TempDir()
		write := func(rel, src string) {
			t.Helper()
			path := filepath.Join(root, filepath.FromSlash(rel))
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.WriteFile(filepath.Join(external, "go.mod"), []byte("module external.example/renderers\n\ngo 1.26.1\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(external, "render.go"), []byte("package renderers\nfunc RenderA(value string) string { return value }\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		write("go.mod", "module ex/rnddeps\n\ngo 1.26.1\n\nrequire external.example/renderers v0.0.0\nreplace external.example/renderers => "+filepath.ToSlash(external)+"\n")
		write("views/views.go", "package views\n")
		write("views/views.gsx", "package views\ncomponent View() { <p/> }\n")
		// This physical directory is deliberately named after the external import
		// path. Path identity must not mistake it for module-owned source: its real
		// module import path is ex/rnddeps/external.example/renderers.
		write("external.example/renderers/renderers.gsx", "package renderers\n\nfunc RenderA(v string) string { return v }\n")

		viewsDir := filepath.Join(root, "views")
		renderers := []codegen.RendererAlias{{
			TypeKey:  "example.com/p.A",
			PkgPath:  "external.example/renderers",
			FuncName: "RenderA",
		}}
		graph := loadGraphWithRootsMust(t, root, []string{"external.example/renderers"})
		key := func() string {
			t.Helper()
			got, err := computeTestKey(t, viewsDir, root, graph, cacheKeyConfig{buildContext: "bctx", codegenIdentity: "gen-test", renderers: renderers})
			if err != nil {
				t.Fatal(err)
			}
			return got
		}
		before := key()
		write("external.example/renderers/renderers.gsx", "package renderers\n\nfunc RenderA(v string) string { return \"changed: \" + v }\n")
		after := key()
		if before != after {
			t.Fatal("editing a coincidental local directory for an external renderer path must not change the cache key")
		}
	})

	t.Run("shadowed module-local source", func(t *testing.T) {
		root := t.TempDir()
		external := t.TempDir()
		write := func(rel, src string) {
			t.Helper()
			path := filepath.Join(root, filepath.FromSlash(rel))
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.WriteFile(filepath.Join(external, "go.mod"), []byte("module external.example/renderers\n\ngo 1.26.1\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(external, "render.go"), []byte("package renderers\nfunc RenderA(value string) string { return value }\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		write("go.mod", "module ex/rnddeps\n\ngo 1.26.1\n\nrequire external.example/renderers v0.0.0\nreplace external.example/renderers => "+filepath.ToSlash(external)+"\n")
		write("views/views.go", "package views\n")
		write("views/views.gsx", "package views\ncomponent View() { <p/> }\n")
		write("renderers/renderers.gsx", "package renderers\n\nfunc RenderA(v string) string { return v }\n")

		viewsDir := filepath.Join(root, "views")
		renderers := []codegen.RendererAlias{
			{TypeKey: "example.com/p.A", PkgPath: "ex/rnddeps/renderers", FuncName: "RenderA"},
			{TypeKey: "example.com/p.A", PkgPath: "external.example/renderers", FuncName: "RenderA"},
		}
		graph := loadGraphWithRootsMust(t, root, []string{"external.example/renderers"})
		key := func() string {
			t.Helper()
			got, err := computeTestKey(t, viewsDir, root, graph, cacheKeyConfig{buildContext: "bctx", codegenIdentity: "gen-test", renderers: renderers})
			if err != nil {
				t.Fatal(err)
			}
			return got
		}
		before := key()
		write("renderers/renderers.gsx", "package renderers\n\nfunc RenderA(v string) string { return \"changed: \" + v }\n")
		after := key()
		if before != after {
			t.Fatal("editing a module-local renderer shadowed by the final external registration must not change the cache key")
		}
	})

	t.Run("valid nested module-local source", func(t *testing.T) {
		root := t.TempDir()
		write := func(rel, src string) {
			t.Helper()
			file := filepath.Join(root, filepath.FromSlash(rel))
			if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(file, []byte(src), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		write("go.mod", "module ex/rnddeps\n\ngo 1.26.1\n")
		write("views/views.go", "package views\n")
		write("views/views.gsx", "package views\ncomponent View() { <p/> }\n")
		write("ui/renderers/renderers.gsx", "package renderers\n\nfunc RenderA(v string) string { return v }\n")

		viewsDir := filepath.Join(root, "views")
		graph := loadGraphMust(t, root)
		renderers := []codegen.RendererAlias{{
			TypeKey:  "example.com/p.A",
			PkgPath:  "ex/rnddeps/ui/renderers",
			FuncName: "RenderA",
		}}
		key := func() string {
			t.Helper()
			got, err := computeTestKey(t, viewsDir, root, graph, cacheKeyConfig{buildContext: "bctx", codegenIdentity: "gen-test", renderers: renderers})
			if err != nil {
				t.Fatal(err)
			}
			return got
		}
		before := key()
		write("ui/renderers/renderers.gsx", "package renderers\n\nfunc RenderA(v string) string { return \"changed: \" + v }\n")
		after := key()
		if before == after {
			t.Fatal("editing a valid nested module-local renderer package must change the cache key")
		}
	})

	t.Run("xmod-valid consecutive dots remain local", func(t *testing.T) {
		root := t.TempDir()
		write := func(rel, src string) {
			t.Helper()
			file := filepath.Join(root, filepath.FromSlash(rel))
			if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(file, []byte(src), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		write("go.mod", "module ex/rnddeps\n\ngo 1.26.1\n")
		write("views/views.go", "package views\n")
		write("views/views.gsx", "package views\ncomponent View() { <p/> }\n")
		write("a..b/renderers.gsx", "package renderers\n\nfunc RenderA(v string) string { return v }\n")

		viewsDir := filepath.Join(root, "views")
		graph := loadGraphMust(t, root)
		renderers := []codegen.RendererAlias{{TypeKey: "example.com/p.A", PkgPath: "ex/rnddeps/a..b", FuncName: "RenderA"}}
		key := func() string {
			t.Helper()
			got, err := computeTestKey(t, viewsDir, root, graph, cacheKeyConfig{buildContext: "bctx", codegenIdentity: "gen-test", renderers: renderers})
			if err != nil {
				t.Fatal(err)
			}
			return got
		}
		before := key()
		write("a..b/renderers.gsx", "package renderers\n\nfunc RenderA(v string) string { return \"changed: \" + v }\n")
		after := key()
		if before == after {
			t.Fatal("editing a renderer package accepted by module.CheckImportPath must change the cache key")
		}
	})
}

func loadGraphMust(t *testing.T, root string) map[string]pkgInfo {
	t.Helper()
	g, err := loadGraph(root)
	if err != nil {
		t.Fatal(err)
	}
	return g
}

func loadGraphWithRootsMust(t *testing.T, root string, roots []string) sourceview.Graph {
	t.Helper()
	moduleDir, modulePath, err := moduleRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := sourceview.Build(sourceview.BuildOptions{ModuleRoot: moduleDir, ModulePath: modulePath})
	if err != nil {
		t.Fatal(err)
	}
	packageDirs := manifest.PackageDirs()
	dirs := make([]string, 0, len(packageDirs))
	for _, dir := range packageDirs {
		dirs = append(dirs, dir)
	}
	graph, err := loadGraphWithContext(codegen.CaptureGoCommandContext(moduleDir), manifest, dirs, roots)
	if err != nil {
		t.Fatal(err)
	}
	return graph
}

// TestComputeKeyFingerprintSensitivity asserts that a different clsFingerprint
// produces a different cache key (so changing attr rules invalidates the cache),
// and that the same fingerprint produces the same key (stability).
func TestComputeKeyFingerprintSensitivity(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	os.WriteFile(filepath.Join(tmp, "go.mod"), []byte("module ex/fptest\n\ngo 1.26\n"), 0o644)
	os.MkdirAll(filepath.Join(tmp, "a"), 0o755)
	os.WriteFile(filepath.Join(tmp, "a", "a.go"), []byte("package a\n"), 0o644)
	os.WriteFile(filepath.Join(tmp, "a", "a.gsx"), []byte("package a\ncomponent View() { <p/> }\n"), 0o644)

	graph, err := loadGraph(tmp)
	if err != nil {
		t.Fatal(err)
	}
	aDir := filepath.Join(tmp, "a")
	bctx := "go1.26\nlinux\namd64\n0\n\n"

	fp1 := "fingerprint-aaa"
	fp2 := "fingerprint-bbb"

	k1a, err := computeTestKey(t, aDir, tmp, graph, cacheKeyConfig{buildContext: bctx, codegenIdentity: "gen-test", classifierFingerprint: fp1})
	if err != nil {
		t.Fatal(err)
	}
	k1b, err := computeTestKey(t, aDir, tmp, graph, cacheKeyConfig{buildContext: bctx, codegenIdentity: "gen-test", classifierFingerprint: fp1})
	if err != nil {
		t.Fatal(err)
	}
	if k1a != k1b {
		t.Error("same fingerprint must produce the same key (unstable)")
	}

	k2, err := computeTestKey(t, aDir, tmp, graph, cacheKeyConfig{buildContext: bctx, codegenIdentity: "gen-test", classifierFingerprint: fp2})
	if err != nil {
		t.Fatal(err)
	}
	if k1a == k2 {
		t.Error("different clsFingerprint must produce different cache keys")
	}
}

// TestComputeKeyGsxOnlyDeps is the regression guard for the stale-cache bug: a
// dep reachable ONLY through a .gsx-hoisted import (no .x.go on disk, so go
// list has no edge) must still be folded into the importer's cache key.
// Editing the dep changes the key. Covers the transitive chain
// pages -> ui -> icons as well as the direct edge.
func TestComputeKeyGsxOnlyDeps(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	mk := func(rel, src string) {
		t.Helper()
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mk("go.mod", "module example.com/app\n\ngo 1.26.1\n")
	mk("icons/icon.gsx", "package icons\n\ncomponent Dot() {\n\t<i/>\n}\n")
	mk("ui/card.gsx", "package ui\n\nimport \"example.com/app/icons\"\n\ncomponent Card() {\n\t<icons.Dot/>\n}\n")
	mk("ui/card.x.go", "package poison\nfunc (\n")
	mk("pages/home.gsx", "package pages\n\nimport \"example.com/app/ui\"\n\ncomponent Home() {\n\t<ui.Card/>\n}\n")

	pagesDir := filepath.Join(root, "pages")
	snapshot := func() (*sourceview.Manifest, sourceview.Graph, *sourceview.CacheProjection) {
		t.Helper()
		manifest, err := sourceview.Build(sourceview.BuildOptions{ModuleRoot: root, ModulePath: "example.com/app"})
		if err != nil {
			t.Fatal(err)
		}
		graph, err := loadGraphWithContext(codegen.CaptureGoCommandContext(root), manifest, []string{pagesDir}, nil)
		if err != nil {
			t.Fatal(err)
		}
		projection, err := sourceview.NewCacheProjection(manifest, graph)
		if err != nil {
			t.Fatal(err)
		}
		return manifest, graph, projection
	}
	key := func(projection *sourceview.CacheProjection) string {
		t.Helper()
		k, err := computeKey(pagesDir, projection, cacheKeyConfig{
			buildContext:          "bctx",
			codegenIdentity:       "cid",
			classifierFingerprint: "cls",
		})
		if err != nil {
			t.Fatal(err)
		}
		return k
	}

	manifest, graph, projection := snapshot()
	for importPath, dir := range manifest.PackageDirs() {
		metadata, ok := graph[importPath]
		if !ok {
			t.Fatalf("shared manifest package %q missing from selected cache graph", importPath)
		}
		wantDir, err := filepath.EvalSymlinks(dir)
		if err != nil {
			t.Fatal(err)
		}
		gotDir, err := filepath.EvalSymlinks(metadata.Dir)
		if err != nil {
			t.Fatal(err)
		}
		if gotDir != wantDir {
			t.Fatalf("selected cache dir for %q = %s, want manifest dir %s", importPath, metadata.Dir, dir)
		}
		for _, selected := range metadata.CompiledGoFiles {
			if strings.Contains(selected, "gsx-sourceview-overlay-") {
				t.Fatalf("temporary overlay backing path entered selected graph for %q: %s", importPath, selected)
			}
		}
	}
	logicalFiles, err := projection.LogicalFiles(pagesDir, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range logicalFiles {
		if strings.Contains(path, "gsx-sourceview-overlay-") || strings.HasSuffix(path, ".x.go") || strings.Contains(filepath.Base(path), "zz_gsx_source_inventory_") {
			t.Fatalf("transport/generated source entered logical cache projection: %v", logicalFiles)
		}
	}

	k1 := key(projection)
	mk("ui/card.x.go", "package different_poison\nfunc (\n")
	_, _, poisonProjection := snapshot()
	if got := key(poisonProjection); got != k1 {
		t.Fatal("changing paired poison output changed the cache key")
	}
	if err := os.Remove(filepath.Join(root, "ui", "card.x.go")); err != nil {
		t.Fatal(err)
	}
	_, _, absentProjection := snapshot()
	if got := key(absentProjection); got != k1 {
		t.Fatal("removing paired generated output changed the cache key")
	}

	mk("ui/inactive.go", "//go:build helpervariant\n\npackage ui\nfunc _gsxrenderCard() {}\n")
	_, _, inactiveProjection := snapshot()
	helperKey := key(inactiveProjection)
	if helperKey == k1 {
		t.Fatal("adding an inactive helper-name input did not change the cache key")
	}
	mk("ui/helper_test.go", "package ui\nfunc _gsxrenderCard1() {}\n")
	_, _, testProjection := snapshot()
	testKey := key(testProjection)
	if testKey == helperKey {
		t.Fatal("adding a same-package test helper-name input did not change the cache key")
	}

	// Direct .gsx-only dep edit changes the key.
	mk("ui/card.gsx", "package ui\n\nimport \"example.com/app/icons\"\n\ncomponent Card(variant string) {\n\t<icons.Dot/>\n}\n")
	_, _, directProjection := snapshot()
	k2 := key(directProjection)
	if testKey == k2 {
		t.Fatal("editing ui (direct .gsx-only dep) did not change pages' cache key")
	}
	// Transitive .gsx-only dep edit changes the key.
	mk("icons/icon.gsx", "package icons\n\ncomponent Dot() {\n\t<b/>\n}\n")
	_, _, transitiveProjection := snapshot()
	k3 := key(transitiveProjection)
	if k2 == k3 {
		t.Fatal("editing icons (transitive .gsx-only dep) did not change pages' cache key")
	}
}

func TestGraphQueryPatternsUseRelativePathsOnlyForManifestPackages(t *testing.T) {
	root := t.TempDir()
	write := func(rel, source string) {
		t.Helper()
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(source), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module example.com/app\n\ngo 1.26.1\n")
	write("ui/card.gsx", "package ui\ncomponent Card() { <p/> }\n")
	write("pages/home.gsx", "package pages\nimport (\n \"example.com/app/ui\"\n \"example.com/external\"\n)\ncomponent Home() { <ui.Card/> }\n")
	write("admin/admin.gsx", "package admin\ncomponent Admin() { <p/> }\n")
	manifest, err := sourceview.Build(sourceview.BuildOptions{ModuleRoot: root, ModulePath: "example.com/app"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := graphQueryPatterns(
		manifest,
		[]string{filepath.Join(root, "pages")},
		[]string{"github.com/gsxhq/gsx", "example.com/configured"},
	)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"./pages", "./ui", "example.com/configured", "example.com/external", "github.com/gsxhq/gsx"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("graph query patterns = %v, want %v", got, want)
	}
}
