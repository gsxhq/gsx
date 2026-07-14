package gen

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
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
func runInfo(stdout, stderr io.Writer, dir, configPath string, filterPkgs []string, aliases []codegen.FilterAlias, renderers []codegen.RendererAlias, cls *attrclass.Classifier, fm codegen.FieldMatcher, cmdArgs []string, cssMinLevel, jsMinLevel MinifyLevel, printWidth int) int {
	// Parse the info subcommand's own flags.
	ifs := flag.NewFlagSet("info", flag.ContinueOnError)
	ifs.SetOutput(stderr)
	var asJSON bool
	ifs.BoolVar(&asJSON, "json", false, "emit resolved config as JSON")
	if err := ifs.Parse(cmdArgs); err != nil {
		return 2
	}
	root, modPath, moduleErr := moduleRoot(dir)

	if asJSON {
		if moduleErr != nil {
			fmt.Fprintf(stderr, "gsx: %v\n", moduleErr)
			return 1
		}
		// renderers are not yet part of the JSON manifest schema (manifestSchemaVersion
		// would need a bump) — pass nil here so --json's resolution is unaffected;
		// only the human-readable path below reports them.
		infos, _, err := codegen.ResolveFunctions(codegen.Options{
			ModuleRoot: root,
			ModulePath: modPath,
			FilterPkgs: filterPkgs,
			Aliases:    aliases,
		})
		if err != nil {
			fmt.Fprintf(stderr, "gsx: %v\n", err)
			return 1
		}
		mf := make([]manifestFilter, 0, len(infos))
		for _, fi := range infos {
			mf = append(mf, manifestFilter{Name: fi.Name, Pkg: fi.Pkg, Func: fi.Func})
		}
		data, _ := json.MarshalIndent(buildManifest(modPath, cls, fm != nil, mf, cssMinLevel, jsMinLevel, printWidth), "", "  ")
		fmt.Fprintln(stdout, string(data))
		return 0
	}

	// Print the version banner + config line FIRST, before resolving filters, so
	// `gsx info` still shows WHICH config is in effect even when an alias fails to
	// resolve — the exact debugging scenario info is designated for (spec §6).
	fmt.Fprintf(stdout, "gsx %s\n", bareVersion())

	// The discovered gsx.toml path — the single source of truth for "which config
	// is in effect, from where". Empty means std-only (no config found).
	if configPath != "" {
		fmt.Fprintf(stdout, "config: %s\n", configPath)
	} else {
		fmt.Fprintf(stdout, "config: none\n")
	}
	if moduleErr != nil {
		fmt.Fprintf(stderr, "gsx: %v\n", moduleErr)
		return 1
	}

	// Resolve filters AFTER the config line is printed: on error the config line
	// is already on stdout (the debugging info the user needs), and the resolution
	// error is surfaced to stderr with a nonzero exit. renderers ride the SAME
	// external importer as the filter packages (see ResolveFunctions), so the
	// Renderers section below never triggers a second packages.Load.
	infos, rinfos, err := codegen.ResolveFunctions(codegen.Options{
		ModuleRoot: root,
		ModulePath: modPath,
		FilterPkgs: filterPkgs,
		Aliases:    aliases,
		Renderers:  renderers,
	})
	if err != nil {
		fmt.Fprintf(stderr, "gsx: %v\n", err)
		return 1
	}

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
		kind := "seed-first"
		if fi.Ctx {
			kind = "seed-first +ctx"
		}
		qualified := path.Base(fi.Pkg) + "." + fi.Func
		line := fmt.Sprintf("  %s\t%s\t%s", fi.Name, qualified, kind)
		if len(fi.Shadows) > 0 {
			line += "\t(shadows " + strings.Join(fi.Shadows, ", ") + ")"
		}
		fmt.Fprintln(tw, line)
	}
	tw.Flush()

	// Renderers section: [renderers] is opt-in config, so (like Attribute rules
	// below) it is only printed when at least one is registered — an empty
	// section for the common no-renderers project would just be noise.
	if len(rinfos) > 0 {
		fmt.Fprintf(stdout, "\nRenderers (%d):\n", len(rinfos))
		rtw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
		for _, ri := range rinfos {
			qualified := path.Base(ri.Pkg) + "." + ri.Func
			line := fmt.Sprintf("  %s\t%s", ri.TypeKey, qualified)
			if ri.HasErr {
				line += "\t(R, error)"
			}
			fmt.Fprintln(rtw, line)
		}
		rtw.Flush()
	}

	// Attribute rules section: show user-supplied URL rules and field-matcher status.
	rules := cls.Rules()
	hasRules := len(rules.URL) > 0
	hasFieldMatcher := fm != nil
	if hasRules || hasFieldMatcher {
		fmt.Fprintf(stdout, "\nAttribute rules:\n")
		printRuleSlice(stdout, "URL", rules.URL)
		if hasFieldMatcher {
			fmt.Fprintf(stdout, "  fieldMatcher: custom\n")
		}
	}

	fmt.Fprintf(stdout, "\nminify: css=%s js=%s\n", cssMinLevel, jsMinLevel)
	fmt.Fprintf(stdout, "formatter: print_width=%d\n", printWidth)

	fmt.Fprintf(stdout, "\nEnvironment:\n")
	etw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	for _, o := range envOverrides {
		val := "unset"
		if raw, ok := os.LookupEnv(o.name); ok {
			val = raw + " (active)"
		}
		fmt.Fprintf(etw, "  %s\t%s\t%s\n", o.name, val, o.desc)
	}
	etw.Flush()

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
