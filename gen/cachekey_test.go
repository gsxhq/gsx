package gen

import (
	"os"
	"path/filepath"
	"testing"
)

// TestBuildContextKeySensitivity is the core regression guard for Fix 1:
// a different buildCtx string must produce a different cache key, and the same
// buildCtx must produce the same key (stability).
func TestBuildContextKeySensitivity(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	os.WriteFile(filepath.Join(tmp, "go.mod"), []byte("module ex/bctx\n\ngo 1.26\n"), 0o644)
	os.MkdirAll(filepath.Join(tmp, "a"), 0o755)
	os.WriteFile(filepath.Join(tmp, "a", "a.go"), []byte("package a\n"), 0o644)

	graph, err := loadGraph(tmp)
	if err != nil {
		t.Fatal(err)
	}
	aDir := filepath.Join(tmp, "a")

	bctxDarwin := "go1.26\ndarwin\namd64\n0\n\n"
	bctxLinux := "go1.26\nlinux\namd64\n0\n\n"

	k1a, err := computeKey(aDir, graph, "ex/bctx", "", "", bctxDarwin, "gen-test", nil, nil, "", false, false, false)
	if err != nil {
		t.Fatal(err)
	}
	k1b, err := computeKey(aDir, graph, "ex/bctx", "", "", bctxDarwin, "gen-test", nil, nil, "", false, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if k1a != k1b {
		t.Error("same buildCtx must produce the same key (unstable)")
	}

	k2, err := computeKey(aDir, graph, "ex/bctx", "", "", bctxLinux, "gen-test", nil, nil, "", false, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if k1a == k2 {
		t.Error("different buildCtx (darwin vs linux) must produce different keys")
	}
}

func TestDirSourceHashStableAndSensitive(t *testing.T) {
	t.Parallel()
	d := t.TempDir()
	os.WriteFile(filepath.Join(d, "a.gsx"), []byte("package v\ncomponent A(){<p>x</p>}\n"), 0o644)
	h1, err := dirSourceHash(d)
	if err != nil {
		t.Fatal(err)
	}
	h2, err := dirSourceHash(d)
	if err != nil {
		t.Fatal(err)
	}
	if h1 != h2 {
		t.Fatal("hash not stable for identical inputs")
	}
	// generated .x.go must NOT affect the hash
	os.WriteFile(filepath.Join(d, "a.x.go"), []byte("package v // generated\n"), 0o644)
	h3, err := dirSourceHash(d)
	if err != nil {
		t.Fatal(err)
	}
	if h3 != h1 {
		t.Errorf(".x.go must be excluded from source hash")
	}
	// editing source MUST change the hash
	os.WriteFile(filepath.Join(d, "a.gsx"), []byte("package v\ncomponent A(){<p>y</p>}\n"), 0o644)
	h4, err := dirSourceHash(d)
	if err != nil {
		t.Fatal(err)
	}
	if h4 == h1 {
		t.Errorf("source edit must change the hash")
	}
}

func TestComputeKeyDepClosure(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	os.WriteFile(filepath.Join(tmp, "go.mod"), []byte("module ex/app\n\ngo 1.26\n"), 0o644)
	mk := func(p, content string) {
		os.MkdirAll(filepath.Join(tmp, p), 0o755)
		os.WriteFile(filepath.Join(tmp, p, p+".go"), []byte(content), 0o644)
	}
	mk("a", "package a\n\nfunc A() string { return \"a\" }\n")
	mk("b", "package b\n\nimport \"ex/app/a\"\n\nfunc B() string { return a.A() }\n")
	mk("c", "package c\n\nfunc C() string { return \"c\" }\n")
	graph, err := loadGraph(tmp)
	if err != nil {
		t.Fatal(err)
	}
	bDir := filepath.Join(tmp, "b")
	key1, err := computeKey(bDir, graph, "ex/app", "", "", "go1.26\nlinux\namd64\n0\n\n", "gen-test", nil, nil, "", false, false, false)
	if err != nil {
		t.Fatal(err)
	}
	// edit dependency a -> b's key must change
	os.WriteFile(filepath.Join(tmp, "a", "a.go"), []byte("package a\n\nfunc A() string { return \"A2\" }\n"), 0o644)
	key2, _ := computeKey(bDir, loadGraphMust(t, tmp), "ex/app", "", "", "go1.26\nlinux\namd64\n0\n\n", "gen-test", nil, nil, "", false, false, false)
	if key1 == key2 {
		t.Error("editing dependency a must change b's key")
	}
	// edit unrelated c -> b's key must NOT change
	os.WriteFile(filepath.Join(tmp, "c", "c.go"), []byte("package c\n\nfunc C() string { return \"C2\" }\n"), 0o644)
	key3, _ := computeKey(bDir, loadGraphMust(t, tmp), "ex/app", "", "", "go1.26\nlinux\namd64\n0\n\n", "gen-test", nil, nil, "", false, false, false)
	if key3 != key2 {
		t.Error("editing unrelated c must NOT change b's key")
	}
}

func loadGraphMust(t *testing.T, root string) map[string]pkgInfo {
	t.Helper()
	g, err := loadGraph(root)
	if err != nil {
		t.Fatal(err)
	}
	return g
}

// TestComputeKeyFingerprintSensitivity asserts that a different clsFingerprint
// produces a different cache key (so changing attr rules invalidates the cache),
// and that the same fingerprint produces the same key (stability).
func TestComputeKeyFingerprintSensitivity(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	os.WriteFile(filepath.Join(tmp, "go.mod"), []byte("module ex/fptest\n\ngo 1.26\n"), 0o644)
	os.MkdirAll(filepath.Join(tmp, "a"), 0o755)
	os.WriteFile(filepath.Join(tmp, "a", "a.go"), []byte("package a\n"), 0o644)

	graph, err := loadGraph(tmp)
	if err != nil {
		t.Fatal(err)
	}
	aDir := filepath.Join(tmp, "a")
	bctx := "go1.26\nlinux\namd64\n0\n\n"

	fp1 := "fingerprint-aaa"
	fp2 := "fingerprint-bbb"

	k1a, err := computeKey(aDir, graph, "ex/fptest", "", "", bctx, "gen-test", nil, nil, fp1, false, false, false)
	if err != nil {
		t.Fatal(err)
	}
	k1b, err := computeKey(aDir, graph, "ex/fptest", "", "", bctx, "gen-test", nil, nil, fp1, false, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if k1a != k1b {
		t.Error("same fingerprint must produce the same key (unstable)")
	}

	k2, err := computeKey(aDir, graph, "ex/fptest", "", "", bctx, "gen-test", nil, nil, fp2, false, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if k1a == k2 {
		t.Error("different clsFingerprint must produce different cache keys")
	}
}
