package gen

import (
	"encoding/binary"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/gsxhq/gsx/internal/diag"
)

type pkgOutput map[string][]byte

// cacheEntry is one directory's cached generation: the .x.go files it wrote
// and the warnings it reported, so a hit reports them again. A directory with
// an error is never stored. Warning filenames are relative to the module root
// (see relWarnings), so an entry stays valid wherever the module is checked
// out.
type cacheEntry struct {
	files    pkgOutput
	warnings []diag.Diagnostic
}

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

func storeGet(dir, key string) (cacheEntry, cacheLookupStatus, error) {
	data, err := os.ReadFile(entryPath(dir, key))
	switch {
	case err == nil:
		entry, ok := decodeEntry(data)
		if !ok {
			return cacheEntry{}, cacheLookupCorrupt, nil
		}
		return entry, cacheLookupHit, nil
	case os.IsNotExist(err):
		return cacheEntry{}, cacheLookupMissing, nil
	default:
		return cacheEntry{}, cacheLookupUnreadable, err
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

func storePut(dir, key string, entry cacheEntry) error {
	return writeAtomic(dir, entryPath(dir, key), encodeEntry(entry))
}

// writeAtomic writes data to p (under the cache root cacheRoot) via a temp
// file in the same directory plus rename, tagging the root as a cache dir.
func writeAtomic(cacheRoot, p string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	// Ensure the cache root is tagged as a cache dir (idempotent, best-effort).
	writeSentinel(cacheRoot)
	tmp, err := os.CreateTemp(filepath.Dir(p), "tmp-")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), p) // atomic
}

// encodeEntry: the files (count, then for each sorted by name: len(name) name
// len(data) data), then len(warnings) warnings, the warnings as a JSON array
// (length 0 when there are none).
func encodeEntry(entry cacheEntry) []byte {
	out := entry.files
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
	var warnings []byte
	if len(entry.warnings) > 0 {
		warnings, _ = json.Marshal(entry.warnings) // plain data: cannot fail
	}
	binary.BigEndian.PutUint64(tmp[:], uint64(len(warnings)))
	b = append(b, tmp[:]...)
	return append(b, warnings...)
}

func decodeEntry(b []byte) (cacheEntry, bool) {
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
		return cacheEntry{}, false
	}
	// Every encoded entry contains at least its name and data lengths. Bound the
	// count before converting it to int or allocating the map.
	if count > uint64(len(b)/16) {
		return cacheEntry{}, false
	}
	out := make(pkgOutput, int(count))
	for range int(count) {
		nameLength, ok := readU64()
		if !ok {
			return cacheEntry{}, false
		}
		if nameLength > uint64(len(b)) {
			return cacheEntry{}, false
		}
		name := string(b[:int(nameLength)])
		b = b[int(nameLength):]
		if name == "" || strings.IndexByte(name, 0) >= 0 || filepath.Base(name) != name || !strings.HasSuffix(name, ".x.go") {
			return cacheEntry{}, false
		}
		if _, duplicate := out[name]; duplicate {
			return cacheEntry{}, false
		}
		dataLength, ok := readU64()
		if !ok {
			return cacheEntry{}, false
		}
		if dataLength > uint64(len(b)) {
			return cacheEntry{}, false
		}
		out[name] = append([]byte(nil), b[:int(dataLength)]...)
		b = b[int(dataLength):]
	}
	warningsLength, ok := readU64()
	if !ok || warningsLength != uint64(len(b)) {
		return cacheEntry{}, false
	}
	var warnings []diag.Diagnostic
	if warningsLength > 0 {
		if json.Unmarshal(b, &warnings) != nil || len(warnings) == 0 {
			return cacheEntry{}, false
		}
		for _, w := range warnings {
			if w.Severity != diag.Warning || !cachedFilename(w.Start.Filename) || !cachedFilename(w.End.Filename) {
				return cacheEntry{}, false
			}
		}
	}
	return cacheEntry{files: out, warnings: warnings}, true
}

// cachedFilename reports whether name is a stored warning filename: empty (a
// positionless diagnostic) or a local slash path below the module root.
func cachedFilename(name string) bool {
	return name == "" || filepath.IsLocal(filepath.FromSlash(name))
}
