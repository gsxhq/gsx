package gen

import (
	"flag"
	"fmt"
	"io"
	"os"
	"runtime/debug"
)

// Option configures Main. It is the option SHAPE for the gen composition root;
// no real options exist in this slice (opts currently do nothing). The
// extension seam — WithFilters / WithClassMerger, which will let a custom gsx
// binary swap the hardcoded codegen std for project-specific filters and a
// class merger — lands in a later slice.
type Option func(*config)

// config holds the resolved options for a Main invocation. It is empty in this
// slice (the stock binary == std codegen); fields arrive with the extension
// seam in a later slice.
type config struct{}

// Main is the gsx process entry point: it builds a config from opts (currently
// a no-op), runs the CLI, and exits with the resulting code. All logic lives in
// the testable run; Main stays tiny so tests never call os.Exit.
func Main(opts ...Option) {
	var cfg config
	for _, opt := range opts {
		opt(&cfg)
	}
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run parses args, dispatches the command, and returns the process exit code
// (0 ok, 1 problems, 2 usage). It writes user-facing output to stdout and
// diagnostics to stderr, and never calls os.Exit so tests can drive it directly.
//
// Global flags may precede the command: -C <dir> (chdir before resolving path
// args), -q (quiet), -v (verbose). The chdir is restored before returning so a
// single process may invoke run repeatedly (e.g. tests) without leaking cwd.
func run(args []string, stdout, stderr io.Writer) int {
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
		return runGenerate(cmdArgs, stdout, stderr, quiet, verbose)
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

// runGenerate runs the generate command over paths (default ["."]) and prints a
// summary. It distinguishes a usage error (a path that does not exist →
// discovery fails with no per-package errors → exit 2) from a codegen error (one
// or more packages failed → exit 1). Success returns 0.
func runGenerate(paths []string, stdout, stderr io.Writer, quiet, verbose bool) int {
	if len(paths) == 0 {
		paths = []string{"."}
	}
	res, err := Generate(paths)

	if len(res.Errs) > 0 {
		// Codegen failures: report each, exit 1.
		for _, e := range res.Errs {
			fmt.Fprintf(stderr, "gsx: %v\n", e)
		}
		return 1
	}
	if err != nil {
		// No per-package errors but Generate failed → discovery/usage error.
		fmt.Fprintf(stderr, "gsx: %v\n", err)
		return 2
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
	version               print the gsx version
	help                  show this help

Global flags:
	-C dir   change to dir before running
	-q       quiet: suppress success output
	-v       verbose: list each written file
`)
}
