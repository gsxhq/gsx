package gen

import (
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"

	"github.com/gsxhq/gsx/internal/codegen"
	"github.com/gsxhq/gsx/internal/typebundle"
)

// TestBundledResolverTransformsNoSubprocess is step 2 of the WASM playground:
// prove the WHOLE gsx transform (type resolution + filter harvest + emit), not
// just go/types, runs from an embedded bundle with no subprocess. The bundle is
// built (packages.Load — allowed) then, with PATH stripped and the packages
// driver disabled, used to generate a .gsx snippet whose pipeline filter (upper)
// lives in the bundled gsx/std. Any hidden shell-out (e.g. byo-props detection)
// would fail under the empty PATH.
func TestBundledResolverTransformsNoSubprocess(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping packages.Load build phase in -short mode")
	}

	// --- BUILD PHASE (shell-out allowed) ---
	data := buildSpikeBundle(t)

	// Snippet on disk (a file read is fine — only subprocesses are forbidden).
	dir := t.TempDir()
	const src = `package main

component Greeting(name string) {
	<p>Hello { name |> upper }!</p>
}
`
	if err := os.WriteFile(filepath.Join(dir, "greeting.gsx"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}

	// --- CONSUME PHASE (prove NO subprocess) ---
	t.Setenv("PATH", "")
	t.Setenv("GOPACKAGESDRIVER", "off")

	r, err := NewBundledResolver(data, []string{codegen.StdImportPath})
	if err != nil {
		t.Fatalf("NewBundledResolver: %v", err)
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

// buildSpikeBundle loads the gsx runtime, std filters, and a few allowlist
// packages and serializes their closure — the BUILD PHASE (packages.Load is
// allowed here; only the consume phase must be subprocess-free).
func buildSpikeBundle(t *testing.T) []byte {
	t.Helper()
	fset := token.NewFileSet()
	cfg := &packages.Config{
		Mode: packages.NeedName | packages.NeedTypes | packages.NeedImports | packages.NeedDeps,
		Fset: fset,
	}
	loaded, err := packages.Load(cfg,
		"github.com/gsxhq/gsx",
		"github.com/gsxhq/gsx/std",
		"fmt", "strings", "context", "io", "strconv",
	)
	if err != nil {
		t.Fatalf("packages.Load: %v", err)
	}
	closure := map[string]*types.Package{}
	packages.Visit(loaded, nil, func(p *packages.Package) {
		if len(p.Errors) == 0 && p.Types != nil {
			closure[p.PkgPath] = p.Types
		}
	})
	pkgs := make([]*types.Package, 0, len(closure))
	for _, p := range closure {
		pkgs = append(pkgs, p)
	}
	data, err := typebundle.Write(fset, pkgs)
	if err != nil {
		t.Fatalf("typebundle.Write: %v", err)
	}
	return data
}

// TestGenerateSourceInMemoryNoFilesystem is step 4: prove the transform runs
// with NO filesystem and NO subprocess — the exact WASM contract. Unlike the
// other spike tests it writes nothing to disk: GenerateSource feeds the snippet
// in memory. With PATH stripped, a passing run means neither a subprocess nor a
// real file was needed.
func TestGenerateSourceInMemoryNoFilesystem(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping packages.Load build phase in -short mode")
	}
	data := buildSpikeBundle(t)

	t.Setenv("PATH", "")
	t.Setenv("GOPACKAGESDRIVER", "off")

	r, err := NewBundledResolver(data, nil)
	if err != nil {
		t.Fatalf("NewBundledResolver: %v", err)
	}
	const src = `package main

component Greeting(name string) {
	<p>Hi { name |> upper }</p>
}
`
	res, err := r.GenerateSource("greeting.gsx", []byte(src))
	if err != nil {
		t.Fatalf("GenerateSource: %v (diags=%v)", err, res.Diags)
	}
	if len(res.Diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", res.Diags)
	}
	if len(res.Files) != 1 {
		t.Fatalf("want exactly 1 generated file, got %d", len(res.Files))
	}
	var out string
	for _, b := range res.Files {
		out = string(b)
	}
	if !strings.Contains(out, "Upper(") {
		t.Fatalf("generated output missing the bundled std.Upper filter call:\n%s", out)
	}
}
