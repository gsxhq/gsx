package gen

import (
	"os"
	"path/filepath"
	"testing"
)

// keyWith computes a cache key for a tiny synthetic graph differing only in the
// minify booleans; everything else is held constant.
func keyWith(t *testing.T, cssMinify, jsMinify bool) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/x\n\ngo 1.26.1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "view")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "view.go"), []byte("package view\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	graph := loadGraphMust(t, root)
	k, err := computeTestKey(t, dir, root, graph, cacheKeyConfig{
		buildContext:          "bctx",
		codegenIdentity:       "codegenid",
		classifierFingerprint: "clsfp",
		cssMinify:             cssMinify,
		jsMinify:              jsMinify,
	})
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func TestComputeKey_MinifyChangesKey(t *testing.T) {
	t.Parallel()
	on := keyWith(t, true, true)
	offCSS := keyWith(t, false, true)
	offJS := keyWith(t, true, false)
	if on == offCSS {
		t.Fatal("css minify change must change the cache key")
	}
	if on == offJS {
		t.Fatal("js minify change must change the cache key")
	}
	// Stable: same inputs → same key.
	if on != keyWith(t, true, true) {
		t.Fatal("same inputs must yield the same key")
	}
}
