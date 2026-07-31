package gen

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/gsxhq/gsx/internal/attrclass"
)

// writeRelocatableModule writes one small gsx module under root. Content is
// byte-identical for every root, so two calls model the same project checked
// out at two paths.
func writeRelocatableModule(t *testing.T, root string) {
	t.Helper()
	repoRoot, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module ex/reloc\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "v"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "v", "v.gsx"), []byte("package v\n\ncomponent A(name string) { <p>{name}</p> }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestCacheSharedAcrossCheckoutPaths pins that the cache key depends on module
// CONTENT, not on where the module happens to be checked out. Two byte-identical
// checkouts at different paths must share cache entries: the first generate
// misses and stores, the second — at a different absolute path — hits.
//
// Regression guard for the GOMOD wart: `go env -json` reports GOMOD as the
// absolute go.mod path, and folding it into the canonical cache environment
// made every checkout location a distinct cache universe. go.mod CONTENT is
// independently hashed as the "main:<module>:go.mod" input (sourceview
// moduleProvenanceInputs), so a real dependency change still misses — proven by
// TestCacheColdWarmEdit and the content-change leg below.
func TestCacheSharedAcrossCheckoutPaths(t *testing.T) {
	t.Setenv("GSXCACHE", t.TempDir())

	first := t.TempDir()
	writeRelocatableModule(t, first)
	_, report, err := generateCachedWithReport([]string{first}, nil, nil, nil, attrclass.Builtin(), true, nil, nil, nil, true, true, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if hits, misses, uncacheable := report.counts(); hits != 0 || misses != 1 || uncacheable != 0 {
		t.Fatalf("first checkout cold: hits=%d misses=%d uncacheable=%d, want 0/1/0", hits, misses, uncacheable)
	}

	second := t.TempDir()
	writeRelocatableModule(t, second)
	_, report, err = generateCachedWithReport([]string{second}, nil, nil, nil, attrclass.Builtin(), true, nil, nil, nil, true, true, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if hits, misses, uncacheable := report.counts(); hits != 1 || misses != 0 || uncacheable != 0 {
		t.Fatalf("identical checkout at a different path: hits=%d misses=%d uncacheable=%d, want 1/0/0 (the cache key must not depend on the checkout path)", hits, misses, uncacheable)
	}

	// Content change at the second path: must miss. This is the direction that
	// keeps the relocation freedom honest — path-independence must never become
	// content-independence.
	if err := os.WriteFile(filepath.Join(second, "v", "v.gsx"), []byte("package v\n\ncomponent A(name string) { <p>Hi {name}</p> }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, report, err = generateCachedWithReport([]string{second}, nil, nil, nil, attrclass.Builtin(), true, nil, nil, nil, true, true, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if hits, misses, uncacheable := report.counts(); hits != 0 || misses != 1 || uncacheable != 0 {
		t.Fatalf("content change: hits=%d misses=%d uncacheable=%d, want 0/1/0", hits, misses, uncacheable)
	}
}
