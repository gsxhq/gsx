package gen

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/gsxhq/gsx/internal/wsfix"
)

// runFix implements `gsx fix <fixer> [flags] [path ...]`. The one fixer,
// whitespace, applies the punctuation-line-break and redundant-space-literal
// fixes (internal/wsfix) in place: run it before `gsx fmt` when upgrading from
// the pre-v0.5 line-break rule, since formatting turns those line breaks into
// same-line spaces that no longer look unintended.
//
//	-l  list files that would change; write nothing
//	-d  print a unified diff; write nothing
//
// Exit codes: 0 success (and, with -l or -d, nothing to fix); 1 with -l or -d
// when a file would change; 2 a usage error or a read/parse/write failure.
func runFix(args []string, stdout, stderr io.Writer, workDir string) int {
	usage := func() {
		fmt.Fprint(stderr, `usage: gsx fix whitespace [-l] [-d] [path ...]

Fixers:
  whitespace  join line breaks that render a space before closing or after
              opening punctuation, and drop { " " } beside a line break that
              already renders one. Run before gsx fmt when upgrading.

Flags:
  -l  list files that would change; write nothing
  -d  print diffs; write nothing
`)
	}
	if len(args) == 0 || args[0] == "-h" || args[0] == "-help" || args[0] == "--help" {
		usage()
		if len(args) == 0 {
			return 2
		}
		return 0
	}
	if args[0] != "whitespace" {
		fmt.Fprintf(stderr, "gsx fix: unknown fixer %q\n", args[0])
		usage()
		return 2
	}
	fs := flag.NewFlagSet("gsx fix whitespace", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = usage
	list := fs.Bool("l", false, "list files that would change")
	diff := fs.Bool("d", false, "print diffs")
	if err := fs.Parse(args[1:]); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	files, err := gsxFiles(absPaths(workDir, fs.Args()))
	if err != nil {
		fmt.Fprintf(stderr, "gsx: %v\n", err)
		return 2
	}
	failed, changed := false, false
	for _, path := range files {
		src, err := os.ReadFile(path)
		if err != nil {
			fmt.Fprintf(stderr, "gsx: %v\n", err)
			failed = true
			continue
		}
		out, n, err := wsfix.Fix(path, src)
		if err != nil {
			fmt.Fprintf(stderr, "gsx: %v\n", err)
			failed = true
			continue
		}
		if bytes.Equal(out, src) {
			continue
		}
		changed = true
		switch {
		case *list || *diff:
			if *list {
				fmt.Fprintln(stdout, path)
			}
			if *diff {
				fmt.Fprint(stdout, unifiedDiff(path, src, out))
			}
		default:
			info, err := os.Stat(path)
			if err == nil {
				err = os.WriteFile(path, out, info.Mode().Perm())
			}
			if err != nil {
				fmt.Fprintf(stderr, "gsx: %v\n", err)
				failed = true
				continue
			}
			fmt.Fprintf(stderr, "gsx: fixed %d whitespace finding(s) in %s\n", n, path)
		}
	}
	switch {
	case failed:
		return 2
	case changed && (*list || *diff):
		return 1
	}
	return 0
}
