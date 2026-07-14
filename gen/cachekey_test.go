package gen

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/gsxhq/gsx/internal/codegen"
)

// TestBuildContextKeySensitivity is the core regression guard for Fix 1:
// a different buildCtx string must produce a different cache key, and the same
// buildCtx must produce the same key (stability).
func TestBuildContextKeySensitivity(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	os.WriteFile(filepath.Join(tmp, "go.mod"), []byte("module ex/bctx\n\ngo 1.26\n"), 0o644)
	os.MkdirAll(filepath.Join(tmp, "a"), 0o755)
	os.WriteFile(filepath.Join(tmp, "a", "a.go"), []byte("package a\n"), 0o644)

	graph, err := loadGraph(tmp)
	if err != nil {
		t.Fatal(err)
	}
	aDir := filepath.Join(tmp, "a")

	bctxDarwin := "go1.26\ndarwin\namd64\n0\n\n"
	bctxLinux := "go1.26\nlinux\namd64\n0\n\n"

	k1a, err := computeKey(aDir, graph, "ex/bctx", "", "", bctxDarwin, "gen-test", nil, nil, nil, "", false, false, false, nil, tmp)
	if err != nil {
		t.Fatal(err)
	}
	k1b, err := computeKey(aDir, graph, "ex/bctx", "", "", bctxDarwin, "gen-test", nil, nil, nil, "", false, false, false, nil, tmp)
	if err != nil {
		t.Fatal(err)
	}
	if k1a != k1b {
		t.Error("same buildCtx must produce the same key (unstable)")
	}

	k2, err := computeKey(aDir, graph, "ex/bctx", "", "", bctxLinux, "gen-test", nil, nil, nil, "", false, false, false, nil, tmp)
	if err != nil {
		t.Fatal(err)
	}
	if k1a == k2 {
		t.Error("different buildCtx (darwin vs linux) must produce different keys")
	}
}

func TestDirSourceHashStableAndSensitive(t *testing.T) {
	t.Parallel()
	d := t.TempDir()
	os.WriteFile(filepath.Join(d, "a.gsx"), []byte("package v\ncomponent A(){<p>x</p>}\n"), 0o644)
	h1, err := dirSourceHash(d)
	if err != nil {
		t.Fatal(err)
	}
	h2, err := dirSourceHash(d)
	if err != nil {
		t.Fatal(err)
	}
	if h1 != h2 {
		t.Fatal("hash not stable for identical inputs")
	}
	// generated .x.go must NOT affect the hash
	os.WriteFile(filepath.Join(d, "a.x.go"), []byte("package v // generated\n"), 0o644)
	h3, err := dirSourceHash(d)
	if err != nil {
		t.Fatal(err)
	}
	if h3 != h1 {
		t.Errorf(".x.go must be excluded from source hash")
	}
	// editing source MUST change the hash
	os.WriteFile(filepath.Join(d, "a.gsx"), []byte("package v\ncomponent A(){<p>y</p>}\n"), 0o644)
	h4, err := dirSourceHash(d)
	if err != nil {
		t.Fatal(err)
	}
	if h4 == h1 {
		t.Errorf("source edit must change the hash")
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
	graph, err := loadGraph(tmp)
	if err != nil {
		t.Fatal(err)
	}
	bDir := filepath.Join(tmp, "b")
	key1, err := computeKey(bDir, graph, "ex/app", "", "", "go1.26\nlinux\namd64\n0\n\n", "gen-test", nil, nil, nil, "", false, false, false, nil, tmp)
	if err != nil {
		t.Fatal(err)
	}
	// edit dependency a -> b's key must change
	os.WriteFile(filepath.Join(tmp, "a", "a.go"), []byte("package a\n\nfunc A() string { return \"A2\" }\n"), 0o644)
	key2, _ := computeKey(bDir, loadGraphMust(t, tmp), "ex/app", "", "", "go1.26\nlinux\namd64\n0\n\n", "gen-test", nil, nil, nil, "", false, false, false, nil, tmp)
	if key1 == key2 {
		t.Error("editing dependency a must change b's key")
	}
	// edit unrelated c -> b's key must NOT change
	os.WriteFile(filepath.Join(tmp, "c", "c.go"), []byte("package c\n\nfunc C() string { return \"C2\" }\n"), 0o644)
	key3, _ := computeKey(bDir, loadGraphMust(t, tmp), "ex/app", "", "", "go1.26\nlinux\namd64\n0\n\n", "gen-test", nil, nil, nil, "", false, false, false, nil, tmp)
	if key3 != key2 {
		t.Error("editing unrelated c must NOT change b's key")
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
	graph, err := loadGraph(tmp)
	if err != nil {
		return "", err
	}
	aDir := filepath.Join(tmp, "a")
	return computeKey(aDir, graph, "ex/cmtest", "", "", "go1.26\nlinux\namd64\n0\n\n", "gen-test", nil, nil, nil, "", false, false, false, classMerger, tmp)
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
	a := base(&codegen.ClassMergerRef{PkgPath: "x/twcfg", FuncName: "Merge"})
	b := base(&codegen.ClassMergerRef{PkgPath: "x/twcfg", FuncName: "Other"})
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
	graph, err := loadGraph(tmp)
	if err != nil {
		t.Fatal(err)
	}
	aDir := filepath.Join(tmp, "a")
	k, err := computeKey(aDir, graph, "ex/rndtest", "", "", "go1.26\nlinux\namd64\n0\n\n", "gen-test", nil, nil, renderers, "", false, false, false, nil, tmp)
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
	rA := codegen.RendererAlias{TypeKey: "example.com/p.A", PkgPath: "example.com/f", FuncName: "RenderA"}
	rB := codegen.RendererAlias{TypeKey: "example.com/p.B", PkgPath: "example.com/f", FuncName: "RenderB"}
	rBOther := codegen.RendererAlias{TypeKey: "example.com/p.B", PkgPath: "example.com/f", FuncName: "RenderBOther"}

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
			got, err := computeKey(viewsDir, graph, "ex/rnddeps", "", "", "bctx", "gen-test", nil, nil, renderers, "", false, false, false, nil, root)
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
		// This physical directory is deliberately named after the external import
		// path. Path identity must not mistake it for module-owned source: its real
		// module import path is ex/rnddeps/external.example/renderers.
		write("external.example/renderers/renderers.gsx", "package renderers\n\nfunc RenderA(v string) string { return v }\n")

		viewsDir := filepath.Join(root, "views")
		graph := loadGraphMust(t, root)
		renderers := []codegen.RendererAlias{{
			TypeKey:  "example.com/p.A",
			PkgPath:  "external.example/renderers",
			FuncName: "RenderA",
		}}
		key := func() string {
			t.Helper()
			got, err := computeKey(viewsDir, graph, "ex/rnddeps", "", "", "bctx", "gen-test", nil, nil, renderers, "", false, false, false, nil, root)
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
		write("renderers/renderers.gsx", "package renderers\n\nfunc RenderA(v string) string { return v }\n")

		viewsDir := filepath.Join(root, "views")
		graph := loadGraphMust(t, root)
		renderers := []codegen.RendererAlias{
			{TypeKey: "example.com/p.A", PkgPath: "ex/rnddeps/renderers", FuncName: "RenderA"},
			{TypeKey: "example.com/p.A", PkgPath: "external.example/renderers", FuncName: "RenderA"},
		}
		key := func() string {
			t.Helper()
			got, err := computeKey(viewsDir, graph, "ex/rnddeps", "", "", "bctx", "gen-test", nil, nil, renderers, "", false, false, false, nil, root)
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

	t.Run("traversal path adds no sibling hash", func(t *testing.T) {
		root := t.TempDir()
		write := func(base, rel, src string) {
			t.Helper()
			file := filepath.Join(base, filepath.FromSlash(rel))
			if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(file, []byte(src), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		write(root, "go.mod", "module ex/rnddeps\n\ngo 1.26.1\n")
		write(root, "views/views.go", "package views\n")
		sibling := filepath.Join(filepath.Dir(root), "sibling")
		write(sibling, "renderers.gsx", "package sibling\n\nfunc RenderA(v string) string { return v }\n")

		viewsDir := filepath.Join(root, "views")
		graph := loadGraphMust(t, root)
		renderers := []codegen.RendererAlias{{
			TypeKey:  "example.com/p.A",
			PkgPath:  "ex/rnddeps/../sibling",
			FuncName: "RenderA",
		}}
		key := func() string {
			t.Helper()
			got, err := computeKey(viewsDir, graph, "ex/rnddeps", "", "", "bctx", "gen-test", nil, nil, renderers, "", false, false, false, nil, root)
			if err != nil {
				t.Fatal(err)
			}
			return got
		}
		before := key()
		write(sibling, "renderers.gsx", "package sibling\n\nfunc RenderA(v string) string { return \"changed: \" + v }\n")
		after := key()
		if before != after {
			t.Fatal("editing an existing sibling named through renderer import-path traversal must not change the cache key")
		}
	})

	t.Run("symlink escape adds no outside hash", func(t *testing.T) {
		root := t.TempDir()
		write := func(base, rel, src string) {
			t.Helper()
			file := filepath.Join(base, filepath.FromSlash(rel))
			if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(file, []byte(src), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		write(root, "go.mod", "module ex/rnddeps\n\ngo 1.26.1\n")
		write(root, "views/views.go", "package views\n")
		outside := filepath.Join(filepath.Dir(root), "outside-renderers")
		write(outside, "renderers.gsx", "package renderers\n\nfunc RenderA(v string) string { return v }\n")
		if err := os.Symlink(outside, filepath.Join(root, "linked-renderers")); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}

		viewsDir := filepath.Join(root, "views")
		graph := loadGraphMust(t, root)
		renderers := []codegen.RendererAlias{{
			TypeKey:  "example.com/p.A",
			PkgPath:  "ex/rnddeps/linked-renderers",
			FuncName: "RenderA",
		}}
		key := func() string {
			t.Helper()
			got, err := computeKey(viewsDir, graph, "ex/rnddeps", "", "", "bctx", "gen-test", nil, nil, renderers, "", false, false, false, nil, root)
			if err != nil {
				t.Fatal(err)
			}
			return got
		}
		before := key()
		write(outside, "renderers.gsx", "package renderers\n\nfunc RenderA(v string) string { return \"changed: \" + v }\n")
		after := key()
		if before != after {
			t.Fatal("editing source outside the module through a renderer directory symlink must not change the cache key")
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
			got, err := computeKey(viewsDir, graph, "ex/rnddeps", "", "", "bctx", "gen-test", nil, nil, renderers, "", false, false, false, nil, root)
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

	t.Run("invalid import grammar adds no local hash", func(t *testing.T) {
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
		invalid := []string{"bad name", "trailing.", "CON", "bad~1"}
		renderers := make([]codegen.RendererAlias, 0, len(invalid))
		for i, rel := range invalid {
			write(rel+"/renderers.gsx", "package renderers\n\nfunc RenderA(v string) string { return v }\n")
			renderers = append(renderers, codegen.RendererAlias{
				TypeKey:  fmt.Sprintf("example.com/p.T%d", i),
				PkgPath:  "ex/rnddeps/" + rel,
				FuncName: "RenderA",
			})
		}

		viewsDir := filepath.Join(root, "views")
		graph := loadGraphMust(t, root)
		key := func() string {
			t.Helper()
			got, err := computeKey(viewsDir, graph, "ex/rnddeps", "", "", "bctx", "gen-test", nil, nil, renderers, "", false, false, false, nil, root)
			if err != nil {
				t.Fatal(err)
			}
			return got
		}
		before := key()
		for _, rel := range invalid {
			write(rel+"/renderers.gsx", "package renderers\n\nfunc RenderA(v string) string { return \"changed: \" + v }\n")
		}
		after := key()
		if before != after {
			t.Fatal("editing existing directories named by invalid renderer import paths must not change the cache key")
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
		write("a..b/renderers.gsx", "package renderers\n\nfunc RenderA(v string) string { return v }\n")

		viewsDir := filepath.Join(root, "views")
		graph := loadGraphMust(t, root)
		renderers := []codegen.RendererAlias{{TypeKey: "example.com/p.A", PkgPath: "ex/rnddeps/a..b", FuncName: "RenderA"}}
		key := func() string {
			t.Helper()
			got, err := computeKey(viewsDir, graph, "ex/rnddeps", "", "", "bctx", "gen-test", nil, nil, renderers, "", false, false, false, nil, root)
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

// TestComputeKeyFingerprintSensitivity asserts that a different clsFingerprint
// produces a different cache key (so changing attr rules invalidates the cache),
// and that the same fingerprint produces the same key (stability).
func TestComputeKeyFingerprintSensitivity(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	os.WriteFile(filepath.Join(tmp, "go.mod"), []byte("module ex/fptest\n\ngo 1.26\n"), 0o644)
	os.MkdirAll(filepath.Join(tmp, "a"), 0o755)
	os.WriteFile(filepath.Join(tmp, "a", "a.go"), []byte("package a\n"), 0o644)

	graph, err := loadGraph(tmp)
	if err != nil {
		t.Fatal(err)
	}
	aDir := filepath.Join(tmp, "a")
	bctx := "go1.26\nlinux\namd64\n0\n\n"

	fp1 := "fingerprint-aaa"
	fp2 := "fingerprint-bbb"

	k1a, err := computeKey(aDir, graph, "ex/fptest", "", "", bctx, "gen-test", nil, nil, nil, fp1, false, false, false, nil, tmp)
	if err != nil {
		t.Fatal(err)
	}
	k1b, err := computeKey(aDir, graph, "ex/fptest", "", "", bctx, "gen-test", nil, nil, nil, fp1, false, false, false, nil, tmp)
	if err != nil {
		t.Fatal(err)
	}
	if k1a != k1b {
		t.Error("same fingerprint must produce the same key (unstable)")
	}

	k2, err := computeKey(aDir, graph, "ex/fptest", "", "", bctx, "gen-test", nil, nil, nil, fp2, false, false, false, nil, tmp)
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
	mk("pages/home.gsx", "package pages\n\nimport \"example.com/app/ui\"\n\ncomponent Home() {\n\t<ui.Card/>\n}\n")

	graph, err := loadGraph(root)
	if err != nil {
		t.Fatal(err)
	}
	pagesDir := filepath.Join(root, "pages")
	key := func() string {
		k, err := computeKey(pagesDir, graph, "example.com/app", "gm", "gs", "bctx", "cid", nil, nil, nil, "cls", false, false, false, nil, root)
		if err != nil {
			t.Fatal(err)
		}
		return k
	}
	k1 := key()
	// Direct .gsx-only dep edit changes the key.
	mk("ui/card.gsx", "package ui\n\nimport \"example.com/app/icons\"\n\ncomponent Card(variant string) {\n\t<icons.Dot/>\n}\n")
	k2 := key()
	if k1 == k2 {
		t.Fatal("editing ui (direct .gsx-only dep) did not change pages' cache key")
	}
	// Transitive .gsx-only dep edit changes the key.
	mk("icons/icon.gsx", "package icons\n\ncomponent Dot() {\n\t<b/>\n}\n")
	k3 := key()
	if k2 == k3 {
		t.Fatal("editing icons (transitive .gsx-only dep) did not change pages' cache key")
	}
}
