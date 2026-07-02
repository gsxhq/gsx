package gen

import (
	"testing"
)

// keyWith computes a cache key for a tiny synthetic graph differing only in the
// minify booleans; everything else is held constant.
func keyWith(t *testing.T, cssMinify, jsMinify bool) string {
	t.Helper()
	dir := t.TempDir()
	graph := map[string]pkgInfo{}
	k, err := computeKey(dir, graph, "example.com/x", "gomod", "gosum", "bctx", "codegenid", nil, nil, "clsfp", false, cssMinify, jsMinify, nil, dir)
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
