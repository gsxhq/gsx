package gen

import (
	"fmt"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"runtime"
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

	const src = `package main

component Greeting(name string) {
	<p>Hello { name |> upper }!</p>
}
`

	// --- CONSUME PHASE (prove NO subprocess) ---
	t.Setenv("PATH", "")
	t.Setenv("GOPACKAGESDRIVER", "off")

	r, err := NewBundledResolver(data, []string{codegen.StdImportPath})
	if err != nil {
		t.Fatalf("NewBundledResolver: %v", err)
	}
	res, err := r.GenerateSource("greeting.gsx", []byte(src))
	if err != nil {
		t.Fatalf("GenerateSource: %v (diags=%v)", err, res.Diags)
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
		Mode: packages.NeedName | packages.NeedTypes | packages.NeedTypesSizes | packages.NeedImports | packages.NeedDeps,
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
	var loadedSizes types.Sizes
	packages.Visit(loaded, nil, func(p *packages.Package) {
		if loadedSizes == nil && p.TypesSizes != nil {
			loadedSizes = p.TypesSizes
		}
	})
	for _, p := range closure {
		pkgs = append(pkgs, p)
	}
	if loadedSizes == nil {
		t.Fatal("packages.Load returned no target type sizes")
	}
	target := typebundle.Target{
		Compiler: runtime.Compiler, GOOS: runtime.GOOS, GOARCH: runtime.GOARCH, CGOEnabled: true,
		ToolchainVersion: "go1.26", LanguageVersion: "go1.26",
		BuildTags: []string{}, ToolTags: []string{"test.tool"}, ReleaseTags: []string{"go1.26"},
	}
	data, err := typebundle.Write(fset, target, pkgs)
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

	// The virtual API's filesystem independence is semantic, not an ENOENT
	// shortcut. Even when its package dir exists and contains active-looking Go
	// and GSX source (including a hostile file at the override's exact path),
	// SourceOnly treats the supplied override as the complete source universe.
	t.Run("ignores host package", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "host.go"), []byte("package hostile\n\nfunc Greeting() {}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "host.gsx"), []byte("package hostile\n\ncomponent Poison( {\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		overridePath := filepath.Join(dir, "greeting.gsx")
		if err := os.WriteFile(overridePath, []byte("package hostile\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		res, err := r.engine.generateSourceOnly(dir, map[string][]byte{overridePath: []byte(src)})
		if err != nil {
			t.Fatalf("source-only generation: %v (diags=%v)", err, res.Diags)
		}
		if len(res.Diags) != 0 {
			t.Fatalf("unexpected diagnostics: %v", res.Diags)
		}
		if len(res.Files) != 1 {
			t.Fatalf("want exactly 1 generated file, got %d", len(res.Files))
		}
		for _, generated := range res.Files {
			if !strings.Contains(string(generated), "Upper(") {
				t.Fatalf("generated output missing bundled filter call:\n%s", generated)
			}
		}
	})

	t.Run("mismatched package clauses are deterministic", func(t *testing.T) {
		files := map[string][]byte{
			"b.gsx": []byte("package beta\n\ncomponent B() { <p/> }\n"),
			"a.gsx": []byte("package alpha\n\ncomponent A() { <p/> }\n"),
		}
		want := fmt.Sprintf(
			"codegen: GSX package %s contains different package clauses: %s declares %q; %s declares %q",
			memDir,
			filepath.Join(memDir, "a.gsx"), "alpha",
			filepath.Join(memDir, "b.gsx"), "beta",
		)
		for i := range 20 {
			_, err := r.GenerateSources(files)
			if err == nil || err.Error() != want {
				t.Fatalf("run %d: error = %v, want %q", i, err, want)
			}
		}
	})

	t.Run("duplicate virtual basenames are rejected deterministically", func(t *testing.T) {
		files := map[string][]byte{
			"b/card.gsx": []byte("package views\n\ncomponent B() { <p/> }\n"),
			"a/card.gsx": []byte("package views\n\ncomponent A() { <p/> }\n"),
		}
		const want = `gen: GenerateSources virtual filenames "a/card.gsx" and "b/card.gsx" both resolve to "card.gsx"`
		for i := range 20 {
			_, err := r.GenerateSources(files)
			if err == nil || err.Error() != want {
				t.Fatalf("run %d: error = %v, want %q", i, err, want)
			}
		}
	})
}
