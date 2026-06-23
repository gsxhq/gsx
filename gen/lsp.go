package gen

import (
	"fmt"
	"io"
	"path/filepath"

	"github.com/gsxhq/gsx/internal/attrclass"
	"github.com/gsxhq/gsx/internal/codegen"
	"github.com/gsxhq/gsx/internal/diag"
	"github.com/gsxhq/gsx/internal/lsp"
)

// lspAnalyzer is the concrete code analysis behind the language server: it
// resolves the module root for a directory and runs the stock (std-filter)
// codegen pipeline over that one package, returning its diagnostics. It never
// writes .x.go to disk — only PackageResult.Diags is read.
type lspAnalyzer struct{}

func (lspAnalyzer) Diagnose(dir string, override map[string][]byte) ([]diag.Diagnostic, error) {
	root, _, err := moduleRoot(dir)
	if err != nil {
		return nil, err
	}
	out, err := codegen.GeneratePackagesWithFilters(root, []string{dir}, nil, attrclass.Builtin(), nil, nil, override)
	if err != nil {
		return nil, err
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	if pr := out[abs]; pr != nil {
		return pr.Diags, nil
	}
	return nil, nil
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
