package gen

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStoreGet(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	out := pkgOutput{"a.x.go": []byte("package a\n"), "b.x.go": []byte("package b\n")}
	if err := storePut(dir, "deadbeefkey", out); err != nil {
		t.Fatal(err)
	}
	got, status, err := storeGet(dir, "deadbeefkey")
	if err != nil || status != cacheLookupHit {
		t.Fatalf("hit lookup = (%v, %v, %v), want output, hit, nil", got, status, err)
	}
	if string(got["a.x.go"]) != "package a\n" || string(got["b.x.go"]) != "package b\n" {
		t.Errorf("round-trip mismatch: %v", got)
	}

	if got, status, err := storeGet(dir, "missingkey"); got != nil || status != cacheLookupMissing || err != nil {
		t.Errorf("missing lookup = (%v, %v, %v), want nil, missing, nil", got, status, err)
	}

	corruptKey := "corruptkey"
	if err := os.MkdirAll(filepath.Dir(entryPath(dir, corruptKey)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(entryPath(dir, corruptKey), encodeOutput(out)[:7], 0o644); err != nil {
		t.Fatal(err)
	}
	if got, status, err := storeGet(dir, corruptKey); got != nil || status != cacheLookupCorrupt || err != nil {
		t.Errorf("corrupt lookup = (%v, %v, %v), want nil, corrupt, nil", got, status, err)
	}

	unreadableKey := "unreadablekey"
	if err := os.MkdirAll(entryPath(dir, unreadableKey), 0o755); err != nil {
		t.Fatal(err)
	}
	if got, status, err := storeGet(dir, unreadableKey); got != nil || status != cacheLookupUnreadable || err == nil {
		t.Errorf("unreadable lookup = (%v, %v, %v), want nil, unreadable, error", got, status, err)
	}
}

func TestCacheDirOff(t *testing.T) {
	t.Setenv("GSXCACHE", "off")
	if _, enabled := cacheDir(); enabled {
		t.Error("GSXCACHE=off must disable the cache")
	}
	t.Setenv("GSXCACHE", t.TempDir())
	if d, enabled := cacheDir(); !enabled || d == "" {
		t.Error("GSXCACHE=<dir> must enable + point at dir")
	}
}
