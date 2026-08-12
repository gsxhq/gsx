package gen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestWatchSession_BatchRefreshFailureIsolatesToBrokenDir pins the
// partial-failure contract of the batched refresh in regenDirs: when one dir's
// refresh fails (here its paired .x.go path is occupied by a directory, which
// the manifest deliberately fails closed on), every healthy sibling in the
// same module must still refresh, regenerate, and write — only the culprit dir
// carries the error. The all-or-nothing batch alone would fan the one error
// out to the whole module and leave healthy dirs' outputs stale on disk.
func TestWatchSession_BatchRefreshFailureIsolatesToBrokenDir(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	write := func(p, s string) {
		full := filepath.Join(root, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module example.com/m\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+gsxModuleDir(t)+"\n")
	write("a/a.gsx", "package a\ncomponent A() { <p>v1</p> }\n")
	write("b/b.gsx", "package b\ncomponent B() { <p>v1</p> }\n")

	aDir := filepath.Join(root, "a")
	bDir := filepath.Join(root, "b")
	s, startup, err := startWatchSessionForTest(watchConfig{paths: []string{root}})
	if err != nil {
		t.Fatalf("startWatchSessionForTest: %v", err)
	}
	for _, r := range startup {
		if !r.OK {
			t.Fatalf("startup regen not OK for %s: err=%v diags=%v", r.Dir, r.Err, r.Diags)
		}
	}

	// Edit a's source, then break b: its paired output path becomes a
	// directory, which pairedOutputPresent fails closed on during refresh.
	write("a/a.gsx", "package a\ncomponent A() { <p>v2</p> }\n")
	if err := os.Remove(filepath.Join(bDir, "b.x.go")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(bDir, "b.x.go"), 0o755); err != nil {
		t.Fatal(err)
	}

	results := s.regenDirs([]string{aDir, bDir})
	if len(results) != 2 {
		t.Fatalf("regenDirs returned %d results, want 2", len(results))
	}
	aRes, bRes := results[0], results[1]
	if !aRes.OK || aRes.Err != nil {
		t.Fatalf("healthy dir a must regenerate despite b's broken refresh; got OK=%v err=%v", aRes.OK, aRes.Err)
	}
	if bRes.OK || bRes.Err == nil {
		t.Fatalf("broken dir b must carry the refresh error; got OK=%v err=%v", bRes.OK, bRes.Err)
	}
	if !strings.Contains(bRes.Err.Error(), "b.x.go") {
		t.Fatalf("b's error must blame b's own paired output, got: %v", bRes.Err)
	}
	generated, err := os.ReadFile(filepath.Join(aDir, "a.x.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(generated), "v2") {
		t.Fatalf("a.x.go on disk is stale — the edit did not regenerate:\n%s", generated)
	}
}
