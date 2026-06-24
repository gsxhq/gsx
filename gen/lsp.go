package gen

import (
	"fmt"
	"io"
	"path/filepath"

	"github.com/gsxhq/gsx/internal/attrclass"
	"github.com/gsxhq/gsx/internal/codegen"
	"github.com/gsxhq/gsx/internal/lsp"
)

// lspAnalyzer is the concrete code analysis behind the language server: it
// resolves the module root for a directory and runs the stock (std-filter)
// codegen pipeline over that one package, returning the retained Package — the
// diagnostics plus the read-only type info (Fset, TypesInfo, expr-map, gsx AST)
// the read-intelligence features need. It never writes .x.go to disk.
//
// Slice-1 limitation: codegen discovers .gsx files by on-disk glob, so a buffer
// opened in the editor but never saved to disk is not analyzed (its override is
// never consulted). Editing existing .gsx files works; unsaved-new files are a
// slice-2 follow-up.
type lspAnalyzer struct{}

func (lspAnalyzer) Analyze(dir string, override map[string][]byte) (*lsp.Package, error) {
	root, _, err := moduleRoot(dir)
	if err != nil {
		return nil, err
	}
	out, err := codegen.GeneratePackagesWithFilters(root, []string{dir}, nil, attrclass.Builtin(), nil, nil, nil, override)
	if err != nil {
		return nil, err
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	pr := out[abs]
	if pr == nil {
		return &lsp.Package{}, nil
	}
	cross := make(map[string]lsp.CrossRef, len(pr.CrossIndex))
	for k, v := range pr.CrossIndex {
		cross[k] = lsp.CrossRef{Name: v.Name, Decl: v.Decl, Refs: v.Refs}
	}
	nav := make([]lsp.NavRef, len(pr.NavIndex))
	for i, nr := range pr.NavIndex {
		nav[i] = lsp.NavRef{From: nr.From, Name: nr.Name, To: nr.To}
	}
	return &lsp.Package{
		Diags:      pr.Diags,
		GSXFset:    pr.GSXFset,
		Fset:       pr.Fset,
		Info:       pr.Info,
		ExprMap:    pr.ExprMap,
		Files:      pr.GSXFiles,
		CrossIndex: cross,
		NavIndex:   nav,
	}, nil
}

// runLSP runs the gsx language server over stdin/stdout (JSON-RPC), logging
// operational failures to stderr. It returns a process exit code.
func runLSP(stdin io.Reader, stdout, stderr io.Writer, _ []string) int {
	srv := lsp.NewServer(stdin, stdout, lspAnalyzer{})
	if err := srv.Run(); err != nil {
		fmt.Fprintf(stderr, "gsx: lsp: %v\n", err)
		return 1
	}
	return 0
}
