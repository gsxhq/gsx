package gen

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type pkgOutput map[string][]byte

type cacheLookupStatus uint8

const (
	cacheLookupHit cacheLookupStatus = iota + 1
	cacheLookupMissing
	cacheLookupCorrupt
	cacheLookupUnreadable
)

// cacheDir returns the cache directory and whether caching is enabled.
func cacheDir() (string, bool) {
	switch v := os.Getenv("GSXCACHE"); {
	case v == "off":
		return "", false
	case v != "":
		return v, true
	}
	base, err := os.UserCacheDir()
	if err != nil {
		return "", false // no usable cache dir → behave as disabled
	}
	return filepath.Join(base, "gsx"), true
}

func entryPath(dir, key string) string {
	shard := key
	if len(key) >= 2 {
		shard = key[:2]
	}
	return filepath.Join(dir, shard, key)
}

func storeGet(dir, key string) (pkgOutput, cacheLookupStatus, error) {
	data, err := os.ReadFile(entryPath(dir, key))
	switch {
	case err == nil:
		out, ok := decodeOutput(data)
		if !ok {
			return nil, cacheLookupCorrupt, nil
		}
		return out, cacheLookupHit, nil
	case os.IsNotExist(err):
		return nil, cacheLookupMissing, nil
	default:
		return nil, cacheLookupUnreadable, err
	}
}

// cachedirTagContent is the standard CACHEDIR.TAG sentinel content.
// See https://bford.info/cachedir/
const cachedirTagContent = "Signature: 8a477f597d28d172789f06886806bc55\n# This file is a cache directory tag created by gsx.\n# For information about cache directory tags see https://bford.info/cachedir/\n"

// writeSentinel idempotently writes the CACHEDIR.TAG file to the cache root dir.
// Best-effort: errors are silently ignored so they never break a cache write.
func writeSentinel(cacheRoot string) {
	tag := filepath.Join(cacheRoot, "CACHEDIR.TAG")
	if _, err := os.Stat(tag); err == nil {
		return // already present
	}
	_ = os.WriteFile(tag, []byte(cachedirTagContent), 0o644)
}

func storePut(dir, key string, out pkgOutput) error {
	p := entryPath(dir, key)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	// Ensure the cache root is tagged as a cache dir (idempotent, best-effort).
	writeSentinel(dir)
	tmp, err := os.CreateTemp(filepath.Dir(p), "tmp-")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(encodeOutput(out)); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), p) // atomic
}

// encodeOutput: count, then for each (sorted by name): len(name) name len(data) data.
func encodeOutput(out pkgOutput) []byte {
	names := make([]string, 0, len(out))
	for n := range out {
		names = append(names, n)
	}
	sort.Strings(names)
	var b []byte
	var tmp [8]byte
	binary.BigEndian.PutUint64(tmp[:], uint64(len(names)))
	b = append(b, tmp[:]...)
	for _, n := range names {
		binary.BigEndian.PutUint64(tmp[:], uint64(len(n)))
		b = append(b, tmp[:]...)
		b = append(b, n...)
		d := out[n]
		binary.BigEndian.PutUint64(tmp[:], uint64(len(d)))
		b = append(b, tmp[:]...)
		b = append(b, d...)
	}
	return b
}

func decodeOutput(b []byte) (pkgOutput, bool) {
	readU64 := func() (uint64, bool) {
		if len(b) < 8 {
			return 0, false
		}
		value := binary.BigEndian.Uint64(b[:8])
		b = b[8:]
		return value, true
	}
	count, ok := readU64()
	if !ok {
		return nil, false
	}
	// Every encoded entry contains at least its name and data lengths. Bound the
	// count before converting it to int or allocating the map.
	if count > uint64(len(b)/16) {
		return nil, false
	}
	out := make(pkgOutput, int(count))
	for range int(count) {
		nameLength, ok := readU64()
		if !ok {
			return nil, false
		}
		if nameLength > uint64(len(b)) {
			return nil, false
		}
		name := string(b[:int(nameLength)])
		b = b[int(nameLength):]
		if name == "" || strings.IndexByte(name, 0) >= 0 || filepath.Base(name) != name || !strings.HasSuffix(name, ".x.go") {
			return nil, false
		}
		if _, duplicate := out[name]; duplicate {
			return nil, false
		}
		dataLength, ok := readU64()
		if !ok {
			return nil, false
		}
		if dataLength > uint64(len(b)) {
			return nil, false
		}
		out[name] = append([]byte(nil), b[:int(dataLength)]...)
		b = b[int(dataLength):]
	}
	if len(b) != 0 {
		return nil, false
	}
	return out, true
}
