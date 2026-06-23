package gen

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime/debug"
	"sort"

	"github.com/gsxhq/gsx/internal/attrclass"
	"github.com/gsxhq/gsx/internal/diag"
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
	filterPkgs []string
	cssMin     func(string) (string, error)
	jsMin      func(string) (string, error)
	jsRules    []attrclass.Rule
	urlRules   []attrclass.Rule
	cssRules   []attrclass.Rule
	attrPred   func(name string) (attrclass.Context, bool)
	predLabel  string
	errs       []error
}

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
		return runGenerate(cmdArgs, stdout, stderr, quiet, verbose, false, cfg.filterPkgs, cfg.classifier(), cfg.predLabel, cfg.cssMin, cfg.jsMin)
	case "clean":
		return runClean(cmdArgs, stdout, stderr)
	case "info":
		// Resolve against cwd: -C (handled above) has already chdir'd, so "."
		// anchors the go/packages load at the user's chosen directory.
		return runInfo(stdout, stderr, ".", cfg.filterPkgs, cfg.classifier(), cfg.predLabel, cmdArgs)
	case "fmt":
		return runFmt(stdout, stderr, cmdArgs)
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
func runGenerate(args []string, stdout, stderr io.Writer, quiet, verbose, noCache bool, filterPkgs []string, cls *attrclass.Classifier, predLabel string, cssMin, jsMin func(string) (string, error)) int {
	gfs := flag.NewFlagSet("generate", flag.ContinueOnError)
	gfs.SetOutput(stderr)
	var nocacheFlag bool
	var jsonFlag bool
	gfs.BoolVar(&nocacheFlag, "no-cache", noCache, "bypass the content-hash cache; regenerate all")
	gfs.BoolVar(&jsonFlag, "json", false, "emit diagnostics as a JSON array to stdout")
	if err := gfs.Parse(args); err != nil {
		return 2
	}
	paths := gfs.Args()
	if len(paths) == 0 {
		paths = []string{"."}
	}
	// Bypass the cache when --no-cache is set OR when a custom minifier is
	// configured: funcs are not hashable, so the cache cannot key on cssMin/jsMin.
	useCache := !nocacheFlag && cssMin == nil && jsMin == nil
	res, err := generateCached(paths, filterPkgs, cls, predLabel, useCache, cssMin, jsMin)

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

	if quiet {
		return 0
	}
	if verbose {
		for _, w := range res.Written {
			fmt.Fprintln(stdout, w)
		}
	}
	if n := len(res.Written); n > 0 {
		fmt.Fprintf(stdout, "gsx: wrote %d file(s)\n", n)
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

// version reports the gsx version from the build info's main module, or
// "(devel)" when no version is embedded (e.g. `go run` or a local build).
func version() string {
	if info, ok := debug.ReadBuildInfo(); ok {
		if v := info.Main.Version; v != "" {
			return v
		}
	}
	return "(devel)"
}

// printUsage writes the top-level usage text listing the available commands.
func printUsage(w io.Writer) {
	fmt.Fprint(w, `gsx — JSX-like HTML templating for Go.

Usage:
	gsx [global flags] <command> [arguments]

Commands:
	generate [paths...]   generate .x.go from .gsx files (default: .)
	fmt [paths...]        format .gsx files (canonical, idempotent)
	clean --cache         remove the gsx cache directory
	info                  list the resolved pipeline filters
	version               print the gsx version
	help                  show this help

Global flags:
	-C dir   change to dir before running
	-q       quiet: suppress success output
	-v       verbose: list each written file
`)
}
