// Command gsx-typebundle produces the playground type bundle: it loads the gsx
// runtime, the std filter package, and the playground stdlib allowlist, then
// serializes their transitive go/types closure (gcexportdata) to a file that the
// WASM playground embeds. Run from the gsx module root so packages.Load resolves
// the gsx packages. This is the ONLY place packages.Load (and thus `go list`)
// runs for the playground — at build time; the embedded result needs no toolchain.
package main

import (
	"flag"
	"fmt"
	"go/token"
	"go/types"
	"os"

	"golang.org/x/tools/go/packages"

	"github.com/gsxhq/gsx/gen"
	"github.com/gsxhq/gsx/internal/codegen"
	"github.com/gsxhq/gsx/internal/typebundle"
)

func main() {
	out := flag.String("o", "playground.typebundle", "output bundle path")
	flag.Parse()

	imports := []string{"github.com/gsxhq/gsx", codegen.StdImportPath}
	imports = append(imports, gen.DefaultPlaygroundImports...)

	fset := token.NewFileSet()
	cfg := &packages.Config{
		Mode: packages.NeedName | packages.NeedTypes | packages.NeedImports | packages.NeedDeps,
		Fset: fset,
	}
	pkgs, err := packages.Load(cfg, imports...)
	if err != nil {
		fatal("load import set: %v", err)
	}
	closure := map[string]*types.Package{}
	var hadErr bool
	packages.Visit(pkgs, nil, func(p *packages.Package) {
		for _, e := range p.Errors {
			fmt.Fprintf(os.Stderr, "%s: %v\n", p.PkgPath, e)
			hadErr = true
		}
		if p.Types != nil {
			closure[p.PkgPath] = p.Types
		}
	})
	if hadErr {
		fatal("type errors while loading the playground import set")
	}
	list := make([]*types.Package, 0, len(closure))
	for _, p := range closure {
		list = append(list, p)
	}
	data, err := typebundle.Write(fset, list)
	if err != nil {
		fatal("write bundle: %v", err)
	}
	if err := os.WriteFile(*out, data, 0o644); err != nil {
		fatal("write file: %v", err)
	}
	fmt.Fprintf(os.Stderr, "gsx-typebundle: wrote %s (%d packages, %d bytes)\n", *out, len(list), len(data))
}

func fatal(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "gsx-typebundle: "+format+"\n", a...)
	os.Exit(1)
}
