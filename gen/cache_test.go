package gen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gsxhq/gsx/internal/attrclass"
)

func TestCacheColdWarmEdit(t *testing.T) {
	repoRoot, _ := filepath.Abs("..")
	tmp := t.TempDir()
	os.WriteFile(filepath.Join(tmp, "go.mod"), []byte("module ex/c\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n"), 0o644)
	mkgsx := func(p, body string) {
		os.MkdirAll(filepath.Join(tmp, p), 0o755)
		os.WriteFile(filepath.Join(tmp, p, p+".gsx"), []byte(body), 0o644)
	}
	mkgsx("v", "package v\n\ncomponent A(name string) { <p>{name}</p> }\n")
	mkgsx("w", "package w\n\ncomponent B() { <div>hi</div> }\n")
	t.Setenv("GSXCACHE", t.TempDir())

	// cold: both generate
	res, err := generateCached([]string{tmp}, nil, nil, nil, attrclass.Builtin(), true, nil, nil, true, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Written) != 2 {
		t.Fatalf("cold: want 2 written, got %v", res.Written)
	}

	// warm no-op: nothing regenerated (Written empty — restores are skipped when on-disk matches)
	res, err = generateCached([]string{tmp}, nil, nil, nil, attrclass.Builtin(), true, nil, nil, true, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Written) != 0 {
		t.Fatalf("warm no-op: want 0 written, got %v", res.Written)
	}

	// edit only v -> only v regenerates
	mkgsx("v", "package v\n\ncomponent A(name string) { <p>Hi {name}</p> }\n")
	res, err = generateCached([]string{tmp}, nil, nil, nil, attrclass.Builtin(), true, nil, nil, true, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Written) != 1 || filepath.Base(filepath.Dir(res.Written[0])) != "v" {
		t.Fatalf("edit v: want only v written, got %v", res.Written)
	}
}

// TestNoCacheBypassesCache proves that useCache=false regenerates even when
// the content-hash cache is warm. We delete the on-disk .x.go between runs
// so the hash-gated write fires, giving a concrete Written count to assert on.
func TestNoCacheBypassesCache(t *testing.T) {
	repoRoot, _ := filepath.Abs("..")
	tmp := t.TempDir()
	os.WriteFile(filepath.Join(tmp, "go.mod"), []byte("module ex/nc\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n"), 0o644)
	mkgsx := func(p, body string) {
		os.MkdirAll(filepath.Join(tmp, p), 0o755)
		os.WriteFile(filepath.Join(tmp, p, p+".gsx"), []byte(body), 0o644)
	}
	mkgsx("v", "package v\n\ncomponent A(name string) { <p>{name}</p> }\n")
	t.Setenv("GSXCACHE", t.TempDir())

	// warm the cache
	res, err := generateCached([]string{tmp}, nil, nil, nil, attrclass.Builtin(), true, nil, nil, true, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Written) != 1 {
		t.Fatalf("cold: want 1 written, got %v", res.Written)
	}

	// delete the .x.go so the no-cache path must actually write it again
	xgo := filepath.Join(tmp, "v", "v.x.go")
	if err := os.Remove(xgo); err != nil {
		t.Fatal(err)
	}

	// with --no-cache (useCache=false): regenerates despite warm cache → Written=1
	res, err = generateCached([]string{tmp}, nil, nil, nil, attrclass.Builtin(), false, nil, nil, true, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Written) != 1 {
		t.Fatalf("--no-cache: want 1 written (regenerated from scratch), got %v", res.Written)
	}
	if len(res.Errs) != 0 {
		t.Fatalf("--no-cache: unexpected errors: %v", res.Errs)
	}
}

func TestRestore_AtomicNoTempLeftovers(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	po := pkgOutput{"a.x.go": []byte("package a\n"), "b.x.go": []byte("package a\n")}
	written, upToDate, err := restore(dir, po)
	if err != nil {
		t.Fatal(err)
	}
	if len(written) != 2 || upToDate != 0 {
		t.Fatalf("written=%v upToDate=%d", written, upToDate)
	}
	// Second run: byte-identical, no writes.
	written, upToDate, err = restore(dir, po)
	if err != nil {
		t.Fatal(err)
	}
	if len(written) != 0 || upToDate != 2 {
		t.Fatalf("expected 0 writes / 2 up-to-date, got written=%v upToDate=%d", written, upToDate)
	}
	// No temp files left behind.
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ents {
		if !strings.HasSuffix(e.Name(), ".x.go") {
			t.Errorf("leftover non-output file: %s", e.Name())
		}
	}
	// Output files are world-readable (0644-equivalent), not CreateTemp's 0600.
	fi, err := os.Stat(filepath.Join(dir, "a.x.go"))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm()&0o044 == 0 {
		t.Errorf("output not group/world readable: %v", fi.Mode())
	}
}
