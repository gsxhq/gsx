package codegen

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestSourceOnlyNeverReadsHostSource(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "component.gsx")
	src := []byte("package virtual\n")
	if err := os.WriteFile(path, src, 0o644); err != nil {
		t.Fatal(err)
	}

	m, err := Open(Options{
		ModuleRoot: dir,
		ModulePath: "example.com/virtual",
		SourceOnly: true,
		Bundle:     testBundle(targetTestImporter(), funcTables{}),
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, found := m.source(path); found {
		t.Fatal("source-only module observed host source before an override was installed")
	}

	m.SetOverride(path, src)
	if got := m.dirtyDirs(); len(got) != 1 || got[0] != dir {
		t.Fatalf("dirty dirs = %v, want [%s]; identical host bytes must be irrelevant", got, dir)
	}
	got, found := m.source(path)
	if !found || !bytes.Equal(got, src) {
		t.Fatalf("source after override = (%q, %v), want (%q, true)", got, found, src)
	}
}
