package typebundle

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"testing"

	"golang.org/x/tools/go/packages"
)

type mapImporter map[string]*types.Package

func (m mapImporter) Import(path string) (*types.Package, error) {
	if p, ok := m[path]; ok {
		return p, nil
	}
	return nil, fmt.Errorf("not bundled: %q", path)
}

// TestBundleRoundTripNoSubprocess is the core WASM-feasibility proof: bundle a
// fixed import allowlist at "build time" (packages.Load shells out — allowed
// here), then with PATH stripped so ANY exec("go") would fail, reconstruct the
// types and type-check a snippet that uses them. If the consume path shelled
// out, the empty PATH would break it. Passing proves the consume path is
// subprocess-free — exactly what a browser WASM build requires.
func TestBundleRoundTripNoSubprocess(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping packages.Load test in -short mode")
	}

	// --- BUILD PHASE (shell-out allowed) ---
	fset := token.NewFileSet()
	cfg := &packages.Config{
		Mode: packages.NeedName | packages.NeedTypes | packages.NeedImports | packages.NeedDeps,
		Fset: fset,
	}
	loaded, err := packages.Load(cfg, "fmt", "strconv", "strings")
	if err != nil {
		t.Fatalf("packages.Load: %v", err)
	}
	// Collect the transitive closure (every loaded package + dep with type info).
	closure := map[string]*types.Package{}
	packages.Visit(loaded, nil, func(p *packages.Package) {
		if p.Types != nil {
			closure[p.PkgPath] = p.Types
		}
	})
	if len(closure) == 0 {
		t.Fatal("no packages loaded")
	}
	var pkgs []*types.Package
	for _, p := range closure {
		pkgs = append(pkgs, p)
	}
	data, err := Write(fset, pkgs)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	t.Logf("bundle: %d packages, %d bytes", len(pkgs), len(data))

	// --- CONSUME PHASE (prove NO subprocess) ---
	// Strip PATH and disable the packages driver: any attempt to exec `go` now
	// fails. go/types + gcexportdata must carry the whole load.
	t.Setenv("PATH", "")
	t.Setenv("GOPACKAGESDRIVER", "off")

	m, err := Read(data)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	for _, want := range []string{"fmt", "strconv", "strings", "io"} {
		if m[want] == nil {
			t.Fatalf("reconstructed bundle missing %q", want)
		}
	}

	// Type-check a snippet against the reconstructed importer.
	const src = `package p

import (
	"fmt"
	"strconv"
)

func F() string { return fmt.Sprintf("%d", 42) + strconv.Itoa(7) }
`
	cfset := token.NewFileSet()
	f, perr := parser.ParseFile(cfset, "p.go", src, 0)
	if perr != nil {
		t.Fatalf("parse: %v", perr)
	}
	conf := types.Config{Importer: mapImporter(m)}
	info := &types.Info{Defs: map[*ast.Ident]types.Object{}}
	pkg, cerr := conf.Check("p", cfset, []*ast.File{f}, info)
	if cerr != nil {
		t.Fatalf("type-check against reconstructed bundle: %v", cerr)
	}
	// F must resolve to func() string.
	obj := pkg.Scope().Lookup("F")
	if obj == nil {
		t.Fatal("F not found")
	}
	sig, ok := obj.Type().(*types.Signature)
	if !ok || sig.Results().Len() != 1 || sig.Results().At(0).Type().String() != "string" {
		t.Fatalf("F resolved to %s, want func() string", obj.Type())
	}
}
