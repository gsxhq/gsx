package gen

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gsxhq/gsx/internal/codegen"
)

// TestCodegenIdentityKeySensitivity is the regression guard for cache
// auto-invalidation: a different codegen identity (i.e. a different gsx binary,
// even an uncommitted dev rebuild that changes emit output) MUST produce a
// different cache key, and the same identity MUST produce the same key. This is
// what prevents the "5aed1ba changed emit but version wasn't bumped → cache
// served stale output" class of bug without relying on a manual constant bump.
func TestCodegenIdentityKeySensitivity(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	os.WriteFile(filepath.Join(tmp, "go.mod"), []byte("module ex/cid\n\ngo 1.26\n"), 0o644)
	os.MkdirAll(filepath.Join(tmp, "a"), 0o755)
	os.WriteFile(filepath.Join(tmp, "a", "a.go"), []byte("package a\n"), 0o644)
	graph, err := loadGraph(tmp)
	if err != nil {
		t.Fatal(err)
	}
	aDir := filepath.Join(tmp, "a")
	bctx := "go1.26\ndarwin\namd64\n0\n\n"

	k1, err := computeKey(aDir, graph, "ex/cid", "", "", bctx, "gen-AAA", nil, nil, nil, "", false, false, false, nil, tmp)
	if err != nil {
		t.Fatal(err)
	}
	k1b, err := computeKey(aDir, graph, "ex/cid", "", "", bctx, "gen-AAA", nil, nil, nil, "", false, false, false, nil, tmp)
	if err != nil {
		t.Fatal(err)
	}
	if k1 != k1b {
		t.Error("same codegen identity must produce the same key (unstable)")
	}
	k2, err := computeKey(aDir, graph, "ex/cid", "", "", bctx, "gen-BBB", nil, nil, nil, "", false, false, false, nil, tmp)
	if err != nil {
		t.Fatal(err)
	}
	if k1 == k2 {
		t.Error("different codegen identity (binary change) must produce a different key")
	}
}

// TestSelfHashStableNonEmpty proves the running gsx binary is content-hashed
// (so a rebuilt binary auto-invalidates the cache) and that the hash is stable
// within a process and matches a manual hash of the executable.
func TestSelfHashStableNonEmpty(t *testing.T) {
	t.Parallel()
	h1 := selfHash()
	if h1 == "" {
		t.Fatal("selfHash empty — gsx binary not hashed into the cache key")
	}
	if h1 != selfHash() {
		t.Fatal("selfHash not stable within a process")
	}
	exe, err := os.Executable()
	if err != nil {
		t.Skip("os.Executable unavailable")
	}
	data, err := os.ReadFile(exe)
	if err != nil {
		t.Skip("cannot read executable")
	}
	sum := sha256.Sum256(data)
	if want := hex.EncodeToString(sum[:]); h1 != want {
		t.Fatalf("selfHash=%s want %s", h1, want)
	}
}

// TestGenerateReportsUpToDate proves a no-op regenerate (output already current
// on disk) is reported as up-to-date rather than vanishing into silence: the
// second Generate writes nothing but counts the unchanged output in UpToDate.
func TestGenerateReportsUpToDate(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping module-resolution test in -short mode")
	}
	mod := newModule(t, "gsxuptodate")
	pkgDir := filepath.Join(mod, "views")
	writeFile(t, pkgDir, "hi.gsx", hiComponent)

	r1, err := Generate([]string{pkgDir})
	if err != nil {
		t.Fatalf("first Generate: %v", err)
	}
	if len(r1.Written) != 1 {
		t.Fatalf("first run should write 1, got %v", r1.Written)
	}

	r2, err := Generate([]string{pkgDir})
	if err != nil {
		t.Fatalf("second Generate: %v", err)
	}
	if len(r2.Written) != 0 {
		t.Fatalf("second run should write 0, got %v", r2.Written)
	}
	if r2.UpToDate != 1 {
		t.Fatalf("second run UpToDate=%d, want 1", r2.UpToDate)
	}
}

// TestRunGenerateUpToDateMessage proves the CLI prints feedback when a run
// writes nothing because everything is current — so -v / a bare run is never
// silently empty.
func TestRunGenerateUpToDateMessage(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping module-resolution test in -short mode")
	}
	mod := newModule(t, "gsxuptodatemsg")
	pkgDir := filepath.Join(mod, "views")
	writeFile(t, pkgDir, "hi.gsx", hiComponent)

	runCapture(t, []string{"generate", pkgDir}) // first run writes
	code, out, errb := runCapture(t, []string{"generate", pkgDir})
	if code != 0 {
		t.Fatalf("exit %d stderr=%q", code, errb)
	}
	if !strings.Contains(out, "up to date") {
		t.Fatalf("expected an 'up to date' message on a no-op run, got %q", out)
	}
}

// TestCodegenIdentityComposition proves the identity folds in BOTH the manual
// codegen version (a coarse, explicit invalidation lever) and the binary hash
// (automatic invalidation on any codegen change).
func TestCodegenIdentityComposition(t *testing.T) {
	t.Parallel()
	id := codegenIdentity()
	if !strings.Contains(id, codegen.Version()) {
		t.Fatalf("codegenIdentity %q must include codegen.Version() %q", id, codegen.Version())
	}
	if h := selfHash(); h != "" && !strings.Contains(id, h) {
		t.Fatalf("codegenIdentity %q must include selfHash %q", id, h)
	}
}
