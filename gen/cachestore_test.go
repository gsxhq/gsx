package gen

import (
	"encoding/binary"
	"fmt"
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

type cacheSerializedEntry struct {
	name string
	data []byte
}

func encodeCacheEntries(entries ...cacheSerializedEntry) []byte {
	var encoded []byte
	appendU64 := func(value uint64) {
		var b [8]byte
		binary.BigEndian.PutUint64(b[:], value)
		encoded = append(encoded, b[:]...)
	}
	appendU64(uint64(len(entries)))
	for _, entry := range entries {
		appendU64(uint64(len(entry.name)))
		encoded = append(encoded, entry.name...)
		appendU64(uint64(len(entry.data)))
		encoded = append(encoded, entry.data...)
	}
	return encoded
}

func TestDecodeOutputRejectsMalformedSerializedBytes(t *testing.T) {
	maxU64 := ^uint64(0)
	var maxU64Bytes [8]byte
	binary.BigEndian.PutUint64(maxU64Bytes[:], maxU64)
	var oneEntry [8]byte
	binary.BigEndian.PutUint64(oneEntry[:], 1)
	entryPrefix := func(name string) []byte {
		var nameLength [8]byte
		binary.BigEndian.PutUint64(nameLength[:], uint64(len(name)))
		return append(append(append([]byte{}, oneEntry[:]...), nameLength[:]...), name...)
	}
	valid := encodeCacheEntries(cacheSerializedEntry{name: "view.x.go", data: []byte("package view\n")})
	duplicate := encodeCacheEntries(
		cacheSerializedEntry{name: "view.x.go", data: []byte("first")},
		cacheSerializedEntry{name: "view.x.go", data: []byte("second")},
	)

	tests := []struct {
		name string
		data []byte
	}{
		{name: "max count", data: maxU64Bytes[:]},
		{name: "max name length", data: append(append(append([]byte{}, oneEntry[:]...), maxU64Bytes[:]...), make([]byte, 8)...)},
		{name: "max data length", data: append(entryPrefix("view.x.go"), maxU64Bytes[:]...)},
		{name: "truncated count", data: []byte{0}},
		{name: "truncated name length", data: append(oneEntry[:], 0)},
		{name: "truncated name", data: append(append(encodeCacheEntries(cacheSerializedEntry{name: "view.x.go"})[:16], []byte("view")...), make([]byte, 4)...)},
		{name: "truncated data length", data: append(encodeCacheEntries(cacheSerializedEntry{name: "view.x.go"})[:25], 0)},
		{name: "truncated data", data: encodeCacheEntries(cacheSerializedEntry{name: "view.x.go", data: []byte("body")})[:35]},
		{name: "trailing garbage", data: append(valid, 0)},
		{name: "duplicate names", data: duplicate},
		{name: "empty name", data: encodeCacheEntries(cacheSerializedEntry{name: "", data: []byte("body")})},
		{name: "null byte in name", data: encodeCacheEntries(cacheSerializedEntry{name: "view\x00.x.go", data: []byte("body")})},
		{name: "missing generated suffix", data: encodeCacheEntries(cacheSerializedEntry{name: "view.go", data: []byte("body")})},
		{name: "nested name", data: encodeCacheEntries(cacheSerializedEntry{name: "nested/view.x.go", data: []byte("body")})},
		{name: "path traversal", data: encodeCacheEntries(cacheSerializedEntry{name: "../view.x.go", data: []byte("body")})},
	}

	dir := t.TempDir()
	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if out, ok := decodeOutput(test.data); ok || out != nil {
				t.Fatalf("decodeOutput(%q) = (%v, %v), want nil, false", test.name, out, ok)
			}

			key := fmt.Sprintf("malformed-%d", index)
			path := entryPath(dir, key)
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, test.data, 0o644); err != nil {
				t.Fatal(err)
			}
			if out, status, err := storeGet(dir, key); out != nil || status != cacheLookupCorrupt || err != nil {
				t.Fatalf("storeGet(%q) = (%v, %v, %v), want nil, corrupt, nil", test.name, out, status, err)
			}
		})
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
