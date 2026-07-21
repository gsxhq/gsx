package gen

import (
	"path/filepath"
	"slices"
	"testing"
)

func TestModuleSymbolDirsAddsOnlyExactModuleOwnedGSXOverrides(t *testing.T) {
	root := filepath.Join(t.TempDir(), "module")
	diskDir := filepath.Join(root, "disk")
	firstDir := filepath.Join(root, "first")
	secondDir := filepath.Join(root, "second")
	outside := filepath.Join(filepath.Dir(root), "module-other", "outside.gsx")

	got := moduleSymbolDirs(root, []string{diskDir, firstDir}, map[string][]byte{
		filepath.Join(firstDir, "page.gsx"):     []byte("dedupes disk directory"),
		filepath.Join(secondDir, "view.gsx"):    []byte("open-only directory"),
		filepath.Join(secondDir, "not-gsx.go"):  []byte("wrong extension"),
		filepath.Join(secondDir, "poison.x.go"): []byte("generated extension"),
		outside:                                 []byte("outside lexical module boundary"),
		filepath.Join(root, "..", "escape.gsx"): []byte("cleaned outside boundary"),
	})
	want := []string{diskDir, firstDir, secondDir}
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("moduleSymbolDirs = %v, want exact sorted module-owned directories %v", got, want)
	}
}
