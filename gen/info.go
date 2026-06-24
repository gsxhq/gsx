package gen

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"path"
	"strings"
	"text/tabwriter"

	"github.com/gsxhq/gsx/internal/attrclass"
	"github.com/gsxhq/gsx/internal/codegen"
)

// runInfo resolves the configured filter packages and prints the resolved
// pipeline filter table — the table that drives `{ x |> filter }` lowering —
// making last-wins SHADOWING visible. dir anchors the go/packages load against
// the module's go.mod (the dispatcher passes "." so -C is honored). It returns
// the process exit code: 0 on success, 1 if filter resolution fails (the error
// goes to stderr).
//
// When asJSON is true it emits the manifest JSON form instead of the human table.
// cmdArgs are the subcommand arguments (used to parse --json).
func runInfo(stdout, stderr io.Writer, dir string, filterPkgs []string, cls *attrclass.Classifier, predLabel string, cmdArgs []string) int {
	// Parse the info subcommand's own flags.
	ifs := flag.NewFlagSet("info", flag.ContinueOnError)
	ifs.SetOutput(stderr)
	var asJSON bool
	ifs.BoolVar(&asJSON, "json", false, "emit resolved config as JSON")
	if err := ifs.Parse(cmdArgs); err != nil {
		return 2
	}

	infos, err := codegen.ResolveFilters(dir, filterPkgs)
	if err != nil {
		fmt.Fprintf(stderr, "gsx: %v\n", err)
		return 1
	}

	if asJSON {
		// Resolve module path for the manifest Module field; "" if unknown.
		var modPath string
		if _, mp, err := moduleRoot(dir); err == nil {
			modPath = mp
		}
		mf := make([]manifestFilter, 0, len(infos))
		for _, fi := range infos {
			mf = append(mf, manifestFilter{Name: fi.Name, Pkg: fi.Pkg, Func: fi.Func})
		}
		data, _ := json.MarshalIndent(buildManifest(modPath, cls, predLabel, mf), "", "  ")
		fmt.Fprintln(stdout, string(data))
		return 0
	}

	fmt.Fprintf(stdout, "gsx %s\n", bareVersion())

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

	// Attribute rules section: show user-supplied rules and predicate status.
	rules := cls.Rules()
	hasRules := len(rules.JS) > 0 || len(rules.URL) > 0 || len(rules.CSS) > 0
	hasPred := cls.HasPredicate()
	if hasRules || hasPred {
		fmt.Fprintf(stdout, "\nAttribute rules:\n")
		printRuleSlice(stdout, "JS", rules.JS)
		printRuleSlice(stdout, "URL", rules.URL)
		printRuleSlice(stdout, "CSS", rules.CSS)
		if hasPred {
			label := predLabel
			if label == "" {
				label = "(unnamed)"
			}
			fmt.Fprintf(stdout, "  predicate: %s\n", label)
		}
	}

	return 0
}

// printRuleSlice prints a labelled list of rules when non-empty.
func printRuleSlice(w io.Writer, label string, rules []attrclass.Rule) {
	if len(rules) == 0 {
		return
	}
	for _, r := range rules {
		if r.Name != "" {
			fmt.Fprintf(w, "  %s  name=%s\n", label, r.Name)
		} else {
			fmt.Fprintf(w, "  %s  prefix=%s\n", label, r.Prefix)
		}
	}
}

// stdImportPath mirrors codegen's built-in filter package import path so runInfo
// can report the effective default-package list without reaching into codegen's
// unexported constant.
const stdImportPath = "github.com/gsxhq/gsx/std"
