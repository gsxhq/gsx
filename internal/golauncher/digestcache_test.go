package golauncher

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// touch rewrites path with content, forcing a distinct modification timestamp so
// the test does not depend on the host clock's resolution between two writes.
func touch(t *testing.T, path, content string, age time.Duration) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
	stamp := time.Now().Add(-age)
	if err := os.Chtimes(path, stamp, stamp); err != nil {
		t.Fatal(err)
	}
}

func TestInspectReusesDigestForUnchangedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "go")
	touch(t, path, "launcher bytes", time.Minute)

	before := digestReads.Load()
	first, firstDigest, err := inspect(path)
	if err != nil {
		t.Fatal(err)
	}
	second, secondDigest, err := inspect(path)
	if err != nil {
		t.Fatal(err)
	}
	reads := digestReads.Load() - before

	if firstDigest != secondDigest {
		t.Fatalf("digest changed for an unchanged file: %x then %x", firstDigest, secondDigest)
	}
	if !os.SameFile(first, second) {
		t.Fatal("inspect reported different files for an unchanged path")
	}
	if reads != 1 {
		t.Fatalf("inspect hashed the file %d times, want 1 (second call must reuse the cached digest)", reads)
	}
}

func TestInspectRehashesAfterSameSizeInPlaceRewrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "compile")
	touch(t, path, "compiler version one", time.Minute)
	_, first, err := inspect(path)
	if err != nil {
		t.Fatal(err)
	}

	// Same byte length, so only the timestamps distinguish the two versions.
	touch(t, path, "compiler version two", 0)
	_, second, err := inspect(path)
	if err != nil {
		t.Fatal(err)
	}

	if first == second {
		t.Fatal("inspect returned a stale digest after a same-size in-place rewrite")
	}
}

func TestInspectRehashesAfterReplacementReusingTimestamps(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "go")
	touch(t, path, "launcher one", time.Minute)
	original, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	_, first, err := inspect(path)
	if err != nil {
		t.Fatal(err)
	}

	// Replace the file with a different inode carrying identical size and
	// modification time, so only the file identity distinguishes them.
	replacement := filepath.Join(dir, "replacement")
	if err := os.WriteFile(replacement, []byte("launcher two"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(replacement, original.ModTime(), original.ModTime()); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacement, path); err != nil {
		t.Fatal(err)
	}

	_, second, err := inspect(path)
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("inspect returned a stale digest after the file was replaced")
	}
}

func TestInspectKeepsDistinctPathsApart(t *testing.T) {
	dir := t.TempDir()
	first := filepath.Join(dir, "go")
	second := filepath.Join(dir, "compile")
	touch(t, first, "launcher bytes", time.Minute)
	touch(t, second, "compiler bytes", time.Minute)

	_, firstDigest, err := inspect(first)
	if err != nil {
		t.Fatal(err)
	}
	_, secondDigest, err := inspect(second)
	if err != nil {
		t.Fatal(err)
	}
	if firstDigest == secondDigest {
		t.Fatal("inspect returned the same digest for two different files")
	}
}
