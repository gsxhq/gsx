package codegen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSrcOverrideReplacesDiskContent: a .gsx on disk is clean, but an override
// buffer introduces a type error; the override must drive the diagnostics.
func TestSrcOverrideReplacesDiskContent(t *testing.T) {
	repoRoot, _ := filepath.Abs("../..")
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "go.mod"),
		"module example.com/x\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n",
	)
	gsxPath := filepath.Join(dir, "page.gsx")
	// On-disk version is valid.
	mustWrite(t, gsxPath, "package x\n\ncomponent Page() {\n\t<div>hi</div>\n}\n")

	// Override introduces a reference to an undefined identifier.
	override := map[string][]byte{
		gsxPath: []byte("package x\n\ncomponent Page() {\n\t<div>{ nope }</div>\n}\n"),
	}
	out, err := GeneratePackagesWithFilters(dir, []string{dir}, nil, nil, nil, nil, nil, override)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	pr := out[dir]
	if pr == nil {
		t.Fatalf("no result for %s (keys: %v)", dir, prKeysOf(out))
	}
	if len(pr.Diags) == 0 {
		t.Fatalf("expected a diagnostic from the override, got none")
	}
	found := false
	for _, d := range pr.Diags {
		if strings.Contains(d.Message, "nope") {
			found = true
		}
	}
	if !found {
		t.Fatalf("diags did not mention undefined 'nope': %+v", pr.Diags)
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func prKeysOf(m map[string]*PackageResult) []string {
	var k []string
	for s := range m {
		k = append(k, s)
	}
	return k
}
