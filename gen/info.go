package gen

import (
	"fmt"
	"io"
	"path"
	"strings"
	"text/tabwriter"

	"github.com/gsxhq/gsx/internal/codegen"
)

// runInfo resolves the configured filter packages and prints the resolved
// pipeline filter table — the table that drives `{ x |> filter }` lowering —
// making last-wins SHADOWING visible. dir anchors the go/packages load against
// the module's go.mod (the dispatcher passes "." so -C is honored). It returns
// the process exit code: 0 on success, 1 if filter resolution fails (the error
// goes to stderr).
func runInfo(stdout, stderr io.Writer, dir string, filterPkgs []string) int {
	infos, err := codegen.ResolveFilters(dir, filterPkgs)
	if err != nil {
		fmt.Fprintf(stderr, "gsx: %v\n", err)
		return 1
	}

	fmt.Fprintf(stdout, "gsx %s\n", version())

	// The configured packages, in last-wins order. An empty list defaults to
	// [std] (ResolveFilters applies the same default), so report that here too.
	pkgs := filterPkgs
	if len(pkgs) == 0 {
		pkgs = []string{stdImportPath}
	}
	fmt.Fprintf(stdout, "\nFilter packages (last-wins):\n")
	for _, p := range pkgs {
		fmt.Fprintf(stdout, "  %s\n", p)
	}

	fmt.Fprintf(stdout, "\nFilters (%d):\n", len(infos))
	tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	for _, fi := range infos {
		kind := "bare"
		if fi.Param {
			kind = "param"
		}
		qualified := path.Base(fi.Pkg) + "." + fi.Func
		line := fmt.Sprintf("  %s\t%s\t%s", fi.Name, qualified, kind)
		if len(fi.Shadows) > 0 {
			line += "\t(shadows " + strings.Join(fi.Shadows, ", ") + ")"
		}
		fmt.Fprintln(tw, line)
	}
	tw.Flush()
	return 0
}

// stdImportPath mirrors codegen's built-in filter package import path so runInfo
// can report the effective default-package list without reaching into codegen's
// unexported constant.
const stdImportPath = "github.com/gsxhq/gsx/std"
