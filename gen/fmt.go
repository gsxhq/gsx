package gen

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/gsxhq/gsx/internal/attrclass"
	"github.com/gsxhq/gsx/internal/codegen"
	"github.com/gsxhq/gsx/internal/gsxfmt"
)

// runFmt implements `gsx fmt`: it formats .gsx files to their canonical,
// idempotent form (parse → wsnorm.Normalize → printer.Fprint). It is
// gofmt-faithful in its flag surface and behavior:
//
//	(default)  write each file's formatted source to stdout
//	-w         rewrite each file in place (only when its content changed)
//	-l         list the paths of files whose formatting differs
//	-d         write a unified diff of the changes to stdout
//
// Path args are .gsx FILES or DIRECTORIES; directories are walked recursively
// for .gsx files, skipping the same junk dirs as discovery (.git, hidden dirs,
// vendor, node_modules, testdata). No args formats "." recursively.
//
// Exit codes:
//
//	0  success: all files parsed, and for default/-w no errors occurred
//	1  a parse error on any file, OR (with -l or -d) any file differs
//
// The -l/-d non-zero-on-difference choice is deliberately CI-friendly: it lets
// a build fail when sources are not canonically formatted (like `gofmt -l` used
// as a check), unlike gofmt's own -l which always exits 0. Default and -w exit 0
// on success regardless of how many files changed.
//
// All logic lives here (runFmt returns an int) so tests can drive it without
// os.Exit.
func runFmt(stdout, stderr io.Writer, args []string) int {
	fs := flag.NewFlagSet("gsx fmt", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var (
		write     bool
		list      bool
		diff      bool
		noImports bool
	)
	fs.BoolVar(&write, "w", false, "write result to (source) file instead of stdout")
	fs.BoolVar(&list, "l", false, "list files whose formatting differs")
	fs.BoolVar(&diff, "d", false, "display diffs instead of rewriting files")
	fs.BoolVar(&noImports, "no-imports", false, "do not remove unused imports (skip module analysis)")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}

	paths := fs.Args()
	files, err := gsxFiles(paths)
	if err != nil {
		fmt.Fprintf(stderr, "gsx: %v\n", err)
		return 2
	}

	var unusedByPath map[string][]gsxfmt.ImportRef
	if !noImports {
		unusedByPath = analyzeUnusedImports(files)
	}

	exit := 0
	widthByDir := map[string]int{}
	for _, path := range files {
		orig, err := os.ReadFile(path)
		if err != nil {
			fmt.Fprintf(stderr, "%s: %v\n", path, err)
			exit = 1
			continue
		}
		abs, _ := filepath.Abs(path)
		dir := filepath.Dir(path)
		width, ok := widthByDir[dir]
		if !ok {
			width = printWidthFor(dir)
			widthByDir[dir] = width
		}
		formatted, err := gsxfmt.FormatRemovingImports(path, orig, unusedByPath[abs], width)
		if err != nil {
			fmt.Fprintf(stderr, "%s: %v\n", path, err)
			exit = 1
			continue
		}
		changed := !bytes.Equal(orig, formatted)
		switch {
		case list:
			if changed {
				fmt.Fprintln(stdout, path)
				exit = 1
			}
		case diff:
			if changed {
				fmt.Fprint(stdout, unifiedDiff(path, orig, formatted))
				exit = 1
			}
		case write:
			if changed {
				mode := os.FileMode(0o644)
				if fi, statErr := os.Stat(path); statErr == nil {
					mode = fi.Mode().Perm()
				}
				if werr := os.WriteFile(path, formatted, mode); werr != nil {
					fmt.Fprintf(stderr, "%s: %v\n", path, werr)
					exit = 1
				}
			}
		default:
			stdout.Write(formatted)
		}
	}
	return exit
}

// Format returns the canonical, idempotent formatting of a single .gsx source
// (the same transformation `gsx fmt` applies): parse → whitespace-normalize →
// print. name is used only in parse-error messages. A non-nil error is a parse
// or print failure. It is the in-process entry point for tooling (editors,
// the playground) that wants to format without the CLI.
func Format(name string, src []byte) ([]byte, error) { return formatGsx(name, src) }

// formatGsx parses src (named for diagnostics), normalizes whitespace, and
// prints the canonical gsx source. The returned bytes are the formatted form; a
// non-nil error is a parse or print failure (the caller treats it as a per-file
// failure and continues with the other files). It delegates to gsxfmt.Format,
// the shared engine the language server's textDocument/formatting also uses.
func formatGsx(name string, src []byte) ([]byte, error) {
	return gsxfmt.Format(name, src, printWidthFor("."))
}

// printWidthFor returns the effective gsx.toml printWidth for dir (default 80),
// best-effort: discovery/decoding failures fall back to 80.
func printWidthFor(dir string) int {
	path, ok := discoverConfig(dir)
	if !ok {
		return 80
	}
	cfg, err := loadConfig(path)
	if err != nil {
		return 80
	}
	return cfg.effectivePrintWidth()
}

// analyzeUnusedImports best-effort computes, per absolute .gsx path, the imports
// the file declares but does not use, by analyzing each containing directory's
// package. Directories not in a module, or that fail to load, are skipped — the
// caller then formats those files syntactically (no removal). Keys are absolute.
func analyzeUnusedImports(files []string) map[string][]gsxfmt.ImportRef {
	out := map[string][]gsxfmt.ImportRef{}
	dirs := map[string]bool{}
	for _, f := range files {
		dirs[filepath.Dir(f)] = true
	}
	for dir := range dirs {
		root, _, err := moduleRoot(dir)
		if err != nil {
			continue // not in a module → syntactic fallback
		}
		absDir, err := filepath.Abs(dir)
		if err != nil {
			continue
		}
		res, err := codegen.GeneratePackagesWithFilters(root, []string{absDir}, nil, nil, attrclass.Builtin(), nil, nil, nil, nil)
		if err != nil {
			continue
		}
		pr := res[absDir]
		if pr == nil {
			continue
		}
		for gsxPath, imps := range pr.UnusedImports {
			absPath, err := filepath.Abs(gsxPath)
			if err != nil {
				continue
			}
			refs := make([]gsxfmt.ImportRef, len(imps))
			for i, u := range imps {
				refs[i] = gsxfmt.ImportRef{Name: u.Name, Path: u.Path}
			}
			out[absPath] = refs
		}
	}
	return out
}

// gsxFiles resolves the path args to a sorted, de-duplicated list of .gsx files
// to format. Each arg is a .gsx file (taken as-is) or a directory (walked
// recursively, skipping junk dirs). No args defaults to walking ".". A
// nonexistent path is an error.
func gsxFiles(paths []string) ([]string, error) {
	if len(paths) == 0 {
		paths = []string{"."}
	}
	set := map[string]bool{}
	for _, p := range paths {
		info, err := os.Stat(p)
		if err != nil {
			return nil, err
		}
		if !info.IsDir() {
			if strings.HasSuffix(p, ".gsx") {
				set[p] = true
			}
			continue
		}
		walkErr := filepath.WalkDir(p, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				// Never skip the root the caller explicitly named; only its subdirs.
				if path != p && shouldSkipDir(d.Name()) {
					return filepath.SkipDir
				}
				return nil
			}
			if strings.HasSuffix(d.Name(), ".gsx") {
				set[path] = true
			}
			return nil
		})
		if walkErr != nil {
			return nil, walkErr
		}
	}
	out := make([]string, 0, len(set))
	for f := range set {
		out = append(out, f)
	}
	sort.Strings(out)
	return out, nil
}

// unifiedDiff returns a minimal unified diff between the original and formatted
// bytes of path, with the conventional `--- path.orig` / `+++ path` headers and
// a single hunk covering the whole file. It is a line-level diff sufficient to
// see what `gsx fmt -w` would change; it is not a minimal-edit diff.
func unifiedDiff(path string, a, b []byte) string {
	aLines := splitLinesKeepEnds(a)
	bLines := splitLinesKeepEnds(b)
	var sb strings.Builder
	fmt.Fprintf(&sb, "--- %s.orig\n", path)
	fmt.Fprintf(&sb, "+++ %s\n", path)
	fmt.Fprintf(&sb, "@@ -1,%d +1,%d @@\n", len(aLines), len(bLines))
	for _, ln := range aLines {
		writeDiffLine(&sb, '-', ln)
	}
	for _, ln := range bLines {
		writeDiffLine(&sb, '+', ln)
	}
	return sb.String()
}

// splitLinesKeepEnds splits data into lines, each retaining its trailing
// newline (if any). A trailing newline does not produce a final empty line.
func splitLinesKeepEnds(data []byte) []string {
	if len(data) == 0 {
		return nil
	}
	var lines []string
	start := 0
	for i := 0; i < len(data); i++ {
		if data[i] == '\n' {
			lines = append(lines, string(data[start:i+1]))
			start = i + 1
		}
	}
	if start < len(data) {
		lines = append(lines, string(data[start:]))
	}
	return lines
}

// writeDiffLine writes one diff line prefixed by sign, ensuring the rendered
// line is newline-terminated even when the source line had no trailing newline.
func writeDiffLine(sb *strings.Builder, sign byte, line string) {
	sb.WriteByte(sign)
	if strings.HasSuffix(line, "\n") {
		sb.WriteString(line)
	} else {
		sb.WriteString(line)
		sb.WriteByte('\n')
	}
}
