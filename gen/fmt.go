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

	"github.com/gsxhq/gsx/internal/codegen"
	"github.com/gsxhq/gsx/internal/diag"
	"github.com/gsxhq/gsx/internal/gsxfmt"
	"github.com/gsxhq/gsx/internal/pretty"
	"github.com/gsxhq/gsx/internal/rawfmt"
)

// runFmt implements `gsx fmt`: it formats .gsx files to their canonical,
// idempotent form (parse → wsnorm.Normalize → printer.Fprint). It is
// gofmt-faithful in its flag surface and behavior:
//
//	(default)  write each file's formatted source to stdout
//	-w         rewrite each file in place (only when its content changed)
//	-l         list the paths of files whose formatting differs
//	-d         write a unified diff of the changes to stdout
//	-imports   import handling: "goimports" (default) or "gofmt"
//	-no-imports  alias for -imports gofmt
//
// Import handling has two modes, resolved per directory (a CLI flag, when
// given, overrides every directory's gsx.toml):
//
//	goimports (default)  remove unused imports; merge every import declaration
//	                      into one block, dedup identical specs, group the
//	                      standard library separately from everything else,
//	                      and sort within each group
//	gofmt                 format only: imports are never removed, merged,
//	                      deduped, or grouped
//
// Path args are .gsx FILES or DIRECTORIES; directories are walked recursively
// for .gsx files, skipping the same junk dirs as discovery (.git, hidden dirs,
// vendor, node_modules, testdata). No args formats "." recursively.
//
// Exit codes:
//
//	0  success: all files parsed, and for default/-w no errors occurred
//	1  a parse error on any file, OR (with -l or -d) any file differs
//	2  a usage error: an unparseable flag, an invalid -imports value, or
//	   -imports goimports combined with -no-imports
//
// The -l/-d non-zero-on-difference choice is deliberately CI-friendly: it lets
// a build fail when sources are not canonically formatted (like `gofmt -l` used
// as a check), unlike gofmt's own -l which always exits 0. Default and -w exit 0
// on success regardless of how many files changed.
//
// All logic lives here (runFmt returns an int) so tests can drive it without
// os.Exit.
func runFmt(stdout, stderr io.Writer, args []string, cssFmt, jsFmt rawfmt.Formatter, opts codegen.Options, workDir string) int {
	fs := flag.NewFlagSet("gsx fmt", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var (
		write       bool
		list        bool
		diff        bool
		noImports   bool
		importsFlag string
	)
	fs.BoolVar(&write, "w", false, "write result to (source) file instead of stdout")
	fs.BoolVar(&list, "l", false, "list files whose formatting differs")
	fs.BoolVar(&diff, "d", false, "display diffs instead of rewriting files")
	fs.StringVar(&importsFlag, "imports", "", `import handling: "goimports" (default; remove unused + merge/dedup/group) or "gofmt" (format only)`)
	fs.BoolVar(&noImports, "no-imports", false, `alias for -imports gofmt`)
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}

	// CLI mode: -imports wins; -no-imports is its "gofmt" alias. Asking for both
	// goimports and no-imports is contradictory, so it is a usage error rather
	// than a silent precedence rule.
	var cliMode gsxfmt.ImportsMode
	if importsFlag != "" {
		m, err := gsxfmt.ParseImportsMode(importsFlag)
		if err != nil {
			fmt.Fprintf(stderr, "gsx: -imports: %v\n", err)
			return 2
		}
		cliMode = m
	}
	if noImports {
		if cliMode == gsxfmt.ImportsGoimports {
			fmt.Fprintf(stderr, "gsx: -no-imports conflicts with -imports goimports\n")
			return 2
		}
		cliMode = gsxfmt.ImportsGofmt
	}

	// Anchor relative path arguments (and the default ".") at workDir so fmt never
	// consults the process-global cwd.
	paths := absPaths(workDir, fs.Args())
	files, err := gsxFiles(paths)
	if err != nil {
		fmt.Fprintf(stderr, "gsx: %v\n", err)
		return 2
	}

	// Mode is resolved per directory (gsx.toml is discovered by walking up from
	// each file), with the CLI flag — when given — overriding every directory.
	modeByDir := map[string]gsxfmt.ImportsMode{}
	modeFor := func(path string) gsxfmt.ImportsMode {
		dir := filepath.Dir(path)
		m, ok := modeByDir[dir]
		if !ok {
			m = cliMode.Or(importsModeFor(dir))
			modeByDir[dir] = m
		}
		return m
	}

	// Only files whose mode removes unused imports need the (expensive) module
	// analysis; gofmt-mode files are excluded so no codegen.Module is opened for
	// them.
	var removalFiles []string
	for _, p := range files {
		if modeFor(p).RemoveUnused() {
			removalFiles = append(removalFiles, p)
		}
	}
	var unusedByPath map[string][]gsxfmt.ImportRef
	var goDiags map[string][]diag.Diagnostic
	if len(removalFiles) > 0 {
		unusedByPath, goDiags = analyzeUnusedImports(removalFiles, opts)
	}

	exit := 0
	// The analyzer's Go parse errors are reported, but never stop formatting: the
	// invalid Go passes through verbatim and the markup around it still canonicalizes.
	if reportGoDiagnostics(stderr, files, goDiags) {
		exit = 1
	}
	ec := newEditorConfigResolver()
	for _, path := range files {
		orig, err := os.ReadFile(path)
		if err != nil {
			fmt.Fprintf(stderr, "%s: %v\n", path, err)
			exit = 1
			continue
		}
		abs, _ := filepath.Abs(path)
		dir := filepath.Dir(path)
		width, tabWidth := formatSettingsFor(dir, abs, ec)
		mode := modeFor(path)
		formatted, err := gsxfmt.FormatWith(path, orig, gsxfmt.FormatOptions{
			Unused:   unusedByPath[abs], // nil for gofmt-mode files
			Width:    width,
			TabWidth: tabWidth,
			CSSFmt:   cssFmt,
			JSFmt:    jsFmt,
			Reorder:  mode.Reorder(),
		})
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
	abs, err := filepath.Abs(name)
	if err != nil {
		abs = name
	}
	w, _ := formatSettingsFor(".", abs, newEditorConfigResolver())
	return gsxfmt.Format(name, src, w)
}

// formatSettingsFor resolves the print width and tab width for one file.
//
// Precedence, highest first: gsx.toml [formatter] > .editorconfig > built-in.
// (There is no CLI flag or env var for either knob; print_width has never had
// one, and tab_width should not grow one alone.) .editorconfig is a cross-tool
// baseline, so an explicit gsx setting beats it even when the .editorconfig
// sits closer to the file.
//
// dir selects the gsx.toml (discovery is per-directory, memoized on ec —
// see editorConfigResolver's doc comment); path selects the .editorconfig
// section (sections are filename globs). path must be ABSOLUTE — the
// editorconfig library resolves a relative path against the process's
// current working directory during its upward .editorconfig walk, which
// silently yields the wrong section when the caller's cwd differs from
// path's actual directory. Every layer is best-effort: a missing or broken
// config falls through, never fails.
func formatSettingsFor(dir, path string, ec *editorConfigResolver) (width, tabWidth int) {
	es := ec.settingsFor(path)
	cfg, _ := ec.configFor(dir) // not found/unusable → zero config, falls through below
	return resolveFormatSettings(cfg, es)
}

// resolveFormatSettings applies the gsx.toml > .editorconfig > built-in
// precedence to an already-resolved gsx.toml config and .editorconfig
// settings, without caring where either came from. This is the ONE place
// that precedence is encoded: the CLI (formatSettingsFor, above) passes the
// raw file config; the LSP (lspAnalyzer.FormatSettings, gen/lsp.go) passes
// the programmatic-opts-over-file-config merge Analyze already computes.
// Both must resolve identically, or `gsx fmt` and the LSP's format-on-save
// disagree on the same file.
//
// Both cfg and es use zero to mean "unset". A cfg field that is unset MUST
// fall through to es — not clobber it with 0 — because an unset gsx.toml key
// is silence, not an override.
func resolveFormatSettings(cfg config, es editorSettings) (width, tabWidth int) {
	width, tabWidth = es.printWidth, es.tabWidth
	if cfg.printWidth > 0 {
		width = cfg.printWidth
	}
	if cfg.tabWidth > 0 {
		tabWidth = cfg.tabWidth
	}
	if width <= 0 {
		width = 80
	}
	if tabWidth <= 0 {
		tabWidth = pretty.DefaultTabWidth
	}
	return width, tabWidth
}

// importsModeFor returns the effective gsx.toml [formatter] imports mode for dir
// (default goimports), best-effort: discovery/decoding failures fall back to the
// default, exactly like formatSettingsFor.
func importsModeFor(dir string) gsxfmt.ImportsMode {
	path, ok := discoverConfig(dir)
	if !ok {
		return gsxfmt.DefaultImportsMode
	}
	cfg, err := loadConfig(path)
	if err != nil {
		return gsxfmt.DefaultImportsMode
	}
	return cfg.effectiveImportsMode()
}

// analyzeUnusedImports computes, per absolute .gsx path, the imports the file
// declares but does not use — syntactically, via the skeleton (no type-check).
// It opens ONE codegen.Module per module (not per directory) and reuses it across
// that module's directories. Directories not in a module, or that fail to open,
// are skipped (those files are then formatted without import removal). opts
// carries the resolved codegen config so skeletons match what `generate` emits;
// a zero/builtin opts still works (buildSkeleton tolerates unknown filters).
//
// It also returns, per absolute .gsx path, the Go parse diagnostics the skeleton
// surfaced. gsx copies user Go through as an opaque blob, so Go that is invalid
// only in context (an `import` after a declaration, say) is caught nowhere else in
// the fmt path; the skeleton's //line directives have already resolved each
// position back to its .gsx origin. Only genuine parse failures become diagnostics;
// a project that will not load yields none, so it can never make `gsx fmt` fail.
func analyzeUnusedImports(files []string, opts codegen.Options) (map[string][]gsxfmt.ImportRef, map[string][]diag.Diagnostic) {
	out := map[string][]gsxfmt.ImportRef{}
	diags := map[string][]diag.Diagnostic{}
	dirSet := map[string]bool{}
	for _, f := range files {
		dirSet[filepath.Dir(f)] = true
	}
	dirs := make([]string, 0, len(dirSet))
	for d := range dirSet {
		dirs = append(dirs, d)
	}
	groups, _ := groupByModule(dirs)
	for _, g := range groups {
		o := opts
		o.ModuleRoot = g.root
		o.ModulePath = g.modPath
		m, err := codegen.Open(o)
		if err != nil {
			continue // not loadable → syntactic-only fallback (no removal, no diagnostics)
		}
		for _, dir := range g.dirs {
			absDir, err := filepath.Abs(dir)
			if err != nil {
				continue
			}
			byPath, goDiags, err := m.UnusedImports(absDir)
			// Bucket by the .gsx path each diagnostic points at: a package's skeletons
			// can carry diagnostics for any of its files, not only the ones named on
			// the command line. Collected even when the call errored, so a package that
			// is unanalyzable for an unrelated reason still reports the Go it could read.
			for _, d := range goDiags {
				abs, aerr := filepath.Abs(d.Start.Filename)
				if aerr != nil {
					continue
				}
				diags[abs] = append(diags[abs], d)
			}
			if err != nil {
				continue // unanalyzable package → not a user-facing Go diagnostic
			}
			for gsxPath, imps := range byPath {
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
	}
	return out, diags
}

// reportGoDiagnostics prints, to stderr, the analyzer's Go parse diagnostics for
// the files being formatted, and reports whether any was an error.
//
// Unlike gofmt, `gsx fmt` reports and still formats. gofmt refuses to write
// because an unparseable file yields no output at all; gsx produced correct output
// (the invalid Go relays through verbatim), so refusing to write would discard
// work it successfully did — and would disagree with the LSP, which formats the
// same buffer while publishing the same diagnostic on its own channel. What is
// borrowed from gofmt is the part that matters: never silently succeed.
//
// Diagnostics for files outside the format set are dropped: the skeleton is
// per-package, and `gsx fmt a.gsx` must not report on its untouched siblings.
func reportGoDiagnostics(stderr io.Writer, files []string, byPath map[string][]diag.Diagnostic) bool {
	if len(byPath) == 0 {
		return false
	}
	var all []diag.Diagnostic
	for _, path := range files {
		abs, err := filepath.Abs(path)
		if err != nil {
			continue
		}
		all = append(all, byPath[abs]...)
	}
	if len(all) == 0 {
		return false
	}
	sort.SliceStable(all, func(i, j int) bool {
		a, b := all[i].Start, all[j].Start
		if a.Filename != b.Filename {
			return a.Filename < b.Filename
		}
		if a.Line != b.Line {
			return a.Line < b.Line
		}
		return a.Column < b.Column
	})
	if isTTY(stderr) {
		diag.RenderRich(stderr, all, func(name string) ([]byte, bool) {
			b, e := os.ReadFile(name)
			return b, e == nil
		})
	} else {
		diag.RenderCompact(stderr, all)
	}
	for _, d := range all {
		if d.Severity == diag.Error {
			return true
		}
	}
	return false
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
	for i := range len(data) {
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
