package gen

import (
	"testing"
)

func TestStoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	out := pkgOutput{"a.x.go": []byte("package a\n"), "b.x.go": []byte("package b\n")}
	if err := storePut(dir, "deadbeefkey", out); err != nil {
		t.Fatal(err)
	}
	got, ok := storeGet(dir, "deadbeefkey")
	if !ok {
		t.Fatal("expected hit")
	}
	if string(got["a.x.go"]) != "package a\n" || string(got["b.x.go"]) != "package b\n" {
		t.Errorf("round-trip mismatch: %v", got)
	}
	if _, ok := storeGet(dir, "missingkey"); ok {
		t.Error("expected miss for unknown key")
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
