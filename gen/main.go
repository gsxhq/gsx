package gen

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime/debug"
	"sort"
	"strings"

	"github.com/gsxhq/gsx/internal/attrclass"
	"github.com/gsxhq/gsx/internal/codegen"
	"github.com/gsxhq/gsx/internal/diag"
	"github.com/gsxhq/gsx/internal/fullmin"
	"github.com/gsxhq/gsx/internal/rawfmt"
)

// Option configures Main. It is the option SHAPE for the gen composition root;
// no real options exist in this slice (opts currently do nothing). The
// extension seam — WithFilters / WithClassMerger, which will let a custom gsx
// binary swap the hardcoded codegen std for project-specific filters and a
// class merger — lands in a later slice.
type Option func(*config)

// config holds the resolved options for a Main invocation.
//
// filterPkgs is the ordered, de-duplicated list of filter-package import paths
// registered via WithFilters; it flows down to codegen with LAST-WINS name
// precedence (overrides go last). When empty, the stock binary == std codegen.
//
// errs collects option-construction problems (e.g. a bad WithFilters marker) so
// the run can fail with a clear message instead of silently dropping the option.
type config struct {
	filterPkgs     []string
	aliases        []codegen.FilterAlias
	cssMin         func(string) (string, error)
	jsMin          func(string) (string, error)
	cssFmt         rawfmt.Formatter
	jsFmt          rawfmt.Formatter
	jsRules        []attrclass.Rule
	urlRules       []attrclass.Rule
	cssRules       []attrclass.Rule
	attrPred       func(name string) (attrclass.Context, bool)
	predLabel      string
	fieldMatcher   codegen.FieldMatcher
	errs           []error
	printWidth     int         // gsx.toml printWidth; 0 means "unset" → 80 at use
	cssMinLevel    MinifyLevel // <style> minification level (zero = MinifyNone)
	jsMinLevel     MinifyLevel // <script> minification level (zero = MinifyNone)
	minifyLevelSet bool        // true once an option (WithMinifyLevel) pinned the levels
}

// effectivePrintWidth returns the configured print width, defaulting to 80 when
// unset (zero or negative).
func (c config) effectivePrintWidth() int {
	if c.printWidth <= 0 {
		return 80
	}
	return c.printWidth
}

// effectiveCSSMin returns the CSS minifier to thread into codegen when the gate
// is on: a custom WithCSSMinifier wins; else the MinifyFull level installs the
// built-in aggressive minifier; else nil (the gate runs the built-in SAFE pass).
// Returning a non-nil func here is what makes full bypass the incremental cache
// (useCache is gated on cssMin == nil).
func (c config) effectiveCSSMin() func(string) (string, error) {
	if c.cssMin != nil {
		return c.cssMin
	}
	if c.cssMinLevel == MinifyFull {
		return fullmin.CSS
	}
	return nil
}

// effectiveJSMin mirrors effectiveCSSMin for <script> JS.
func (c config) effectiveJSMin() func(string) (string, error) {
	if c.jsMin != nil {
		return c.jsMin
	}
	if c.jsMinLevel == MinifyFull {
		return fullmin.JS
	}
	return nil
}

// cssMinifyOn reports whether the <style> minify pass should run: true when a
// minifier is in effect (level == MinifyFull, or a custom WithCSSMinifier is
// installed). Keeps the codegen gate consistent with effectiveCSSMin — a custom
// minifier installed without an explicit level still runs.
func (c config) cssMinifyOn() bool { return c.effectiveCSSMin() != nil }

// jsMinifyOn mirrors cssMinifyOn for <script> JS.
func (c config) jsMinifyOn() bool { return c.effectiveJSMin() != nil }

// classifier builds the resolved Classifier from the accumulated options. A
// config with no attr options yields a built-ins-only Classifier.
func (cfg *config) classifier() *attrclass.Classifier {
	return attrclass.New(attrclass.Rules{
		JS:  cfg.jsRules,
		URL: cfg.urlRules,
		CSS: cfg.cssRules,
	}, cfg.attrPred)
}

// Main is the gsx process entry point: it builds a config from opts (currently
// a no-op), runs the CLI, and exits with the resulting code. All logic lives in
// the testable run; Main stays tiny so tests never call os.Exit.
func Main(opts ...Option) {
	var cfg config
	for _, opt := range opts {
		opt(&cfg)
	}
	os.Exit(runConfig(os.Args[1:], os.Stdout, os.Stderr, cfg))
}

// run parses args, dispatches the command, and returns the process exit code
// (0 ok, 1 problems, 2 usage). It writes user-facing output to stdout and
// diagnostics to stderr, and never calls os.Exit so tests can drive it directly.
//
// Global flags may precede the command: -C <dir> (chdir before resolving path
// args), -q (quiet), -v (verbose). The chdir is restored before returning so a
// single process may invoke run repeatedly (e.g. tests) without leaking cwd.
func run(args []string, stdout, stderr io.Writer) int {
	return runConfig(args, stdout, stderr, config{})
}

// runConfig is run with an explicit config: it carries the resolved options
// (filterPkgs, option-construction errs) from Main down to the generate path.
// run delegates to it with a zero config (stock std codegen) so existing
// callers and tests can drive the CLI without building options.
func runConfig(args []string, stdout, stderr io.Writer, cfg config) int {
	// Surface any option-construction errors (e.g. a bad WithFilters marker)
	// before doing any work, so a misconfigured custom binary fails clearly.
	if len(cfg.errs) > 0 {
		for _, e := range cfg.errs {
			fmt.Fprintf(stderr, "gsx: %v\n", e)
		}
		return 2
	}
	fs := flag.NewFlagSet("gsx", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { printUsage(stderr) }
	var (
		chdir   string
		quiet   bool
		verbose bool
	)
	fs.StringVar(&chdir, "C", "", "change to `dir` before running")
	fs.BoolVar(&quiet, "q", false, "quiet: suppress success output")
	fs.BoolVar(&verbose, "v", false, "verbose: list each written file")

	if err := fs.Parse(args); err != nil {
		// flag already printed the error and (for -h) the usage to stderr.
		if err == flag.ErrHelp {
			printUsage(stdout)
			return 0
		}
		return 2
	}

	rest := fs.Args()
	if len(rest) == 0 {
		printUsage(stdout)
		return 0
	}

	if chdir != "" {
		orig, err := os.Getwd()
		if err != nil {
			fmt.Fprintf(stderr, "gsx: %v\n", err)
			return 2
		}
		if err := os.Chdir(chdir); err != nil {
			fmt.Fprintf(stderr, "gsx: -C %s: %v\n", chdir, err)
			return 2
		}
		defer os.Chdir(orig)
	}

	cmd, cmdArgs := rest[0], rest[1:]
	switch cmd {
	case "generate":
		// Only generate and info consume gsx.toml; config-agnostic commands
		// (version/help/clean/fmt/lsp) must not load it, so a malformed config
		// can't break them. resolveConfig discovers+loads+merges (opts on top).
		merged, _, err := resolveConfig(cfg)
		if err != nil {
			fmt.Fprintf(stderr, "gsx: %v\n", err)
			return 2
		}
		effCSS, effJS := merged.effectiveCSSMin(), merged.effectiveJSMin()
		return runGenerate(cmdArgs, stdout, stderr, quiet, verbose, false, merged.filterPkgs, merged.aliases, merged.classifier(), merged.fieldMatcher, effCSS, effJS, merged.cssMinifyOn(), merged.jsMinifyOn())
	case "clean":
		return runClean(cmdArgs, stdout, stderr)
	case "info":
		// Resolve against cwd: -C (handled above) has already chdir'd, so "."
		// anchors the go/packages load at the user's chosen directory.
		merged, configPath, err := resolveConfig(cfg)
		if err != nil {
			fmt.Fprintf(stderr, "gsx: %v\n", err)
			return 2
		}
		return runInfo(stdout, stderr, ".", configPath, merged.filterPkgs, merged.aliases, merged.classifier(), merged.predLabel, merged.fieldMatcher, cmdArgs, merged.cssMinLevel, merged.jsMinLevel)
	case "fmt":
		// fmt respects gsx.toml printWidth per-dir (via printWidthFor inside
		// runFmt) and tolerates a malformed config. The CSS/JS formatter
		// overrides are programmatic options (no gsx.toml entry), so they come
		// from cfg directly — not resolveConfig (which would hard-fail on a bad
		// config).
		return runFmt(stdout, stderr, cmdArgs, cfg.cssFmt, cfg.jsFmt)
	case "init":
		return runInit(cmdArgs, os.Stdin, stdout, stderr)
	case "lsp":
		// lsp resolves gsx.toml per-file (best-effort) and merges these compiled-in
		// opts under it; cfg.errs are already surfaced at the top of runConfig.
		return runLSP(os.Stdin, stdout, stderr, cfg, cmdArgs)
	case "version":
		fmt.Fprintln(stdout, version())
		return 0
	case "help":
		printUsage(stdout)
		return 0
	default:
		fmt.Fprintf(stderr, "gsx: unknown command %q\nRun 'gsx help' for usage.\n", cmd)
		return 2
	}
}

// resolveConfig discovers a gsx.toml from the (post-chdir) working dir, loads it
// as the BASE config, and merges the programmatic optCfg ON TOP so opts win under
// the existing last-wins resolution. It returns the merged config and the
// discovered path ("" when none found, which info reports as "config: none").
//
// It is called ONLY by the commands that consume config (generate, info) — not
// by version/help/clean/fmt/lsp — so a malformed/ typo'd gsx.toml cannot break a
// config-agnostic command. A LOAD error (malformed TOML / unknown key / bad
// alias) is returned naming the path so the caller can fail clearly (exit 2).
//
// With no gsx.toml this is a no-op returning optCfg unchanged and an empty path,
// preserving byte-identical stock behavior. The resolved values populate the
// SAME config fields opts already do, so the cache key folds a config change
// automatically (no separate config hash). Option-construction errs on optCfg are
// already surfaced at the top of runConfig before dispatch, so they need no
// recheck here.
func resolveConfig(optCfg config) (merged config, configPath string, err error) {
	// Layer 1: file defaults (base is the zero config when no gsx.toml exists).
	var base config
	path, ok := discoverConfig(".")
	if ok {
		base, err = loadConfig(path)
		if err != nil {
			return config{}, "", err
		}
		configPath = path
	}
	// Layer 3: env overrides the file. Applied to base BEFORE the merge so the
	// final precedence is option > env > config (mergeConfig lets opts win).
	base, err = applyEnvOverrides(base)
	if err != nil {
		return config{}, "", err
	}
	// Layer 2: programmatic options win (existing last-wins merge).
	return mergeConfig(base, optCfg), configPath, nil
}

// runClean implements the `clean` command. Currently the only flag is --cache,
// which removes the gsx cache directory (resolved via cacheDir). When the cache
// is disabled no-ops are printed and the function returns 0. Without any flags
// the usage for `clean` is printed and the function returns 0.
func runClean(args []string, stdout, stderr io.Writer) int {
	cfs := flag.NewFlagSet("clean", flag.ContinueOnError)
	cfs.SetOutput(stderr)
	var cache bool
	cfs.BoolVar(&cache, "cache", false, "remove the gsx cache directory")
	if err := cfs.Parse(args); err != nil {
		return 2
	}
	if !cache {
		fmt.Fprint(stdout, "gsx clean: no flags given.\n\nUsage:\n\tgsx clean --cache   remove the gsx cache directory\n")
		return 0
	}
	dir, enabled := cacheDir()
	if !enabled {
		fmt.Fprintln(stdout, "gsx clean: cache is disabled (GSXCACHE=off or no usable cache dir); nothing to remove")
		return 0
	}
	// If the directory doesn't exist there's nothing to remove — success.
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		fmt.Fprintf(stdout, "gsx clean: cache dir does not exist: %s\n", dir)
		return 0
	}
	// Require the CACHEDIR.TAG sentinel to prevent accidentally nuking a
	// non-cache dir (e.g. GSXCACHE=$HOME would otherwise delete $HOME).
	tag := filepath.Join(dir, "CACHEDIR.TAG")
	if _, err := os.Stat(tag); os.IsNotExist(err) {
		fmt.Fprintf(stderr, "gsx: refusing to remove %q: not a gsx cache dir (no CACHEDIR.TAG)\n", dir)
		return 1
	}
	if err := os.RemoveAll(dir); err != nil {
		fmt.Fprintf(stderr, "gsx: clean --cache: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "removed gsx cache: %s\n", dir)
	return 0
}

// runGenerate runs the generate command over paths (default ["."]) and prints a
// summary. It distinguishes a usage error (a path that does not exist →
// discovery fails → exit 2) from a codegen error (one or more error-severity
// diagnostics → exit 1). Success returns 0.
// noCache bypasses the content-hash cache and forces a full regeneration.
func runGenerate(args []string, stdout, stderr io.Writer, quiet, verbose, noCache bool, filterPkgs []string, aliases []codegen.FilterAlias, cls *attrclass.Classifier, fm codegen.FieldMatcher, cssMin, jsMin func(string) (string, error), cssMinify, jsMinify bool) int {
	gfs := flag.NewFlagSet("generate", flag.ContinueOnError)
	gfs.SetOutput(stderr)
	var nocacheFlag bool
	var jsonFlag bool
	var watchFlag bool
	var formatFlag string
	// -q/-v are output flags that belong to generate (cf. `go build -v`), so they
	// work in any position after the command. Their defaults are the global
	// values, which OR-combines a global `gsx -v generate` with a per-command
	// `gsx generate -v` — either turns the behaviour on.
	quietFlag, verboseFlag := quiet, verbose
	gfs.BoolVar(&nocacheFlag, "no-cache", noCache, "bypass the content-hash cache; regenerate all")
	gfs.BoolVar(&jsonFlag, "json", false, "emit diagnostics as a JSON array to stdout")
	gfs.BoolVar(&watchFlag, "watch", false, "watch sources and regenerate on change (long-lived)")
	gfs.StringVar(&formatFlag, "format", "", "output format for --watch: \"ndjson\" for machine consumption")
	gfs.BoolVar(&quietFlag, "q", quiet, "quiet: suppress success output")
	gfs.BoolVar(&verboseFlag, "v", verbose, "verbose: list each written file")
	// Partition args into flag tokens (starting with "-") and positional paths
	// so that flags work in any position relative to path arguments.
	// Note: only boolean flags are supported here; -flag value pairs are not.
	var flagArgs, paths []string
	for _, a := range args {
		if strings.HasPrefix(a, "-") {
			flagArgs = append(flagArgs, a)
		} else {
			paths = append(paths, a)
		}
	}
	if err := gfs.Parse(flagArgs); err != nil {
		return 2
	}
	if len(paths) == 0 {
		paths = []string{"."}
	}
	if watchFlag {
		return runWatch(watchConfig{
			paths: paths, format: formatFlag,
			stdout: stdout, stderr: stderr, quiet: quiet, verbose: verbose,
			filterPkgs: filterPkgs, aliases: aliases, cls: cls,
			fm: fm, cssMin: cssMin, jsMin: jsMin, cssMinify: cssMinify, jsMinify: jsMinify,
		})
	}
	// Bypass the cache when --no-cache is set OR when a custom minifier is
	// configured: funcs are not hashable, so the cache cannot key on cssMin/jsMin.
	// Bypass the cache when a custom field matcher is installed: funcs are not
	// hashable, so the cache cannot key on fm. Mirror the minifier bypass pattern.
	useCache := !nocacheFlag && cssMin == nil && jsMin == nil && fm == nil
	res, err := generateCached(paths, filterPkgs, aliases, cls, fm, useCache, cssMin, jsMin, cssMinify, jsMinify)

	// Operational errors (I/O, module-graph failures): these are not diagnostics.
	// Print each with the gsx: prefix and return early.
	if len(res.Errs) > 0 {
		for _, e := range res.Errs {
			fmt.Fprintf(stderr, "gsx: %v\n", e)
		}
		return 1
	}
	// If generate failed with no operational errors and no error diagnostics,
	// it must be a discovery/usage error (e.g. path does not exist).
	if err != nil && !anyErrorDiag(res.Diags) {
		fmt.Fprintf(stderr, "gsx: %v\n", err)
		return 2
	}

	// Sort the merged diagnostics deterministically: filename→line→column.
	sort.SliceStable(res.Diags, func(i, j int) bool {
		a, b := res.Diags[i].Start, res.Diags[j].Start
		if a.Filename != b.Filename {
			return a.Filename < b.Filename
		}
		if a.Line != b.Line {
			return a.Line < b.Line
		}
		return a.Column < b.Column
	})

	// Render diagnostics.
	if jsonFlag {
		// JSON mode: write to stdout, nothing to stderr.
		_ = diag.RenderJSON(stdout, res.Diags)
	} else if isTTY(stderr) {
		// Rich mode: rustc-style with source snippet + caret.
		src := func(name string) ([]byte, bool) {
			b, e := os.ReadFile(name)
			return b, e == nil
		}
		diag.RenderRich(stderr, res.Diags, src)
	} else {
		// Compact mode: one line per diagnostic.
		diag.RenderCompact(stderr, res.Diags)
	}

	// Exit 1 if any error-severity diagnostic.
	if anyErrorDiag(res.Diags) {
		return 1
	}

	if quietFlag {
		return 0
	}
	if verboseFlag {
		for _, w := range res.Written {
			fmt.Fprintln(stdout, w)
		}
	}
	// Always report what happened — including a no-op run where everything was
	// already current — so a bare or -v run is never silently empty.
	n, u := len(res.Written), res.UpToDate
	switch {
	case n > 0 && u > 0:
		fmt.Fprintf(stdout, "gsx: wrote %d file(s), %d up to date\n", n, u)
	case n > 0:
		fmt.Fprintf(stdout, "gsx: wrote %d file(s)\n", n)
	case u > 0:
		fmt.Fprintf(stdout, "gsx: %d file(s) already up to date\n", u)
	}
	return 0
}

// isTTY reports whether w is a character device (i.e., a terminal). Uses only
// stdlib: checks os.ModeCharDevice on the file's mode bits.
func isTTY(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

// bareVersion reports just the gsx module version (e.g. "v1.2.3"), or "(devel)"
// when none is embedded. It is the one-line form embedded by `info`; the
// `version` command uses the richer banner from version().
func bareVersion() string {
	if info, ok := debug.ReadBuildInfo(); ok {
		if v := info.Main.Version; v != "" {
			return v
		}
	}
	return "(devel)"
}

// version reports the gsx version banner for the `version` command. It reads the
// build info's main-module version, or "(devel)" when none is embedded (e.g.
// `go run` or a local build), and enriches it with the VCS revision/time/dirty
// state and the Go toolchain version when available — so a dev build still
// reports a useful commit for bug reports. The banner already begins with
// "gsx "; callers embedding a one-line version should use bareVersion instead.
func version() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "gsx (devel)"
	}
	return formatBuildVersion(info)
}

// formatBuildVersion renders the multi-line version banner from build info. It
// is split out from version() so it can be tested deterministically with a
// synthetic *debug.BuildInfo rather than the ambient build environment.
func formatBuildVersion(info *debug.BuildInfo) string {
	ver := info.Main.Version
	if ver == "" {
		ver = "(devel)"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "gsx %s", ver)

	// Extract VCS settings (present when built with buildvcs from a repo).
	var rev, vcsTime string
	var dirty bool
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
		case "vcs.time":
			vcsTime = s.Value
		case "vcs.modified":
			dirty = s.Value == "true"
		}
	}
	if rev != "" {
		short := rev
		if len(short) > 12 {
			short = short[:12]
		}
		fmt.Fprintf(&b, "\n  commit: %s", short)
		var paren []string
		if vcsTime != "" {
			paren = append(paren, vcsTime)
		}
		if dirty {
			paren = append(paren, "dirty")
		}
		if len(paren) > 0 {
			fmt.Fprintf(&b, " (%s)", strings.Join(paren, ", "))
		}
	}
	if info.GoVersion != "" {
		fmt.Fprintf(&b, "\n  go:     %s", info.GoVersion)
	}
	return b.String()
}

// printUsage writes the top-level usage text listing the available commands.
func printUsage(w io.Writer) {
	fmt.Fprint(w, `gsx — JSX-like HTML templating for Go.

Usage:
	gsx [global flags] <command> [arguments]

Commands:
	generate [paths...]   generate .x.go from .gsx files (default: .)
	fmt [paths...]        format .gsx files (canonical, idempotent)
	init [dir]            scaffold a gsx + Vite starter app (--template, --module)
	clean --cache         remove the gsx cache directory
	info                  list the resolved pipeline filters
	lsp                   run the language server over stdio (JSON-RPC)
	version               print the gsx version
	help                  show this help

Global flags (must precede the command):
	-C dir   change to dir before running

generate flags (accepted in any position):
	-q       quiet: suppress success output
	-v       verbose: list each written file
	--json   emit diagnostics as a JSON array to stdout
	--no-cache   bypass the content-hash cache; regenerate all
`)
}
