package playbundle

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestEmbeddedBundleTransformsOffline is step 3 of the WASM playground: prove the
// transform runs from the EMBEDDED bundle alone, with no packages.Load anywhere
// (unlike step 2, which built the bundle in-test). With PATH stripped and the
// packages driver disabled, the embedded blob → resolver → transform path must
// generate a snippet whose `name |> upper` resolves to the bundled std.Upper.
// This is the closest native analog to the browser WASM build.
func TestEmbeddedBundleTransformsOffline(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping transform test in -short mode")
	}
	if len(Bundle()) == 0 {
		t.Fatal("embedded bundle is empty")
	}

	t.Setenv("PATH", "")
	t.Setenv("GOPACKAGESDRIVER", "off")

	dir := t.TempDir()
	const src = `package main

component Greeting(name string) {
	<p>Hello { name |> upper }!</p>
}
`
	if err := os.WriteFile(filepath.Join(dir, "g.gsx"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}

	r, err := NewResolver()
	if err != nil {
		t.Fatalf("NewResolver from embedded bundle: %v", err)
	}
	res, err := r.Generate(dir, nil)
	if err != nil {
		t.Fatalf("Generate: %v (diags=%v)", err, res.Diags)
	}
	if len(res.Diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", res.Diags)
	}
	var out string
	for _, b := range res.Files {
		out += string(b)
	}
	if !strings.Contains(out, "Upper(") {
		t.Fatalf("generated output missing the bundled std.Upper filter call:\n%s", out)
	}
}
