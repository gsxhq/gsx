// Package gen is the gsx generation engine: it discovers .gsx files under a set
// of paths, runs the codegen for each Go package directory, and writes the
// resulting .x.go files to disk next to their .gsx sources.
package gen

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/gsxhq/gsx/internal/attrclass"
	"github.com/gsxhq/gsx/internal/diag"
)

// skipDirs are directory names never descended into during discovery, in
// addition to any directory whose name begins with a dot.
var skipDirs = map[string]bool{
	".git":         true,
	"vendor":       true,
	"node_modules": true,
	"testdata":     true,
}

// shouldSkipDir reports whether a directory with the given base name must be
// skipped during the walk: hidden dirs (name starts with ".") or any name in
// skipDirs. The current directory marker "." is never skipped.
func shouldSkipDir(name string) bool {
	if name == "." || name == "" {
		return false
	}
	if strings.HasPrefix(name, ".") {
		return true
	}
	return skipDirs[name]
}

// discoverDirs walks each given path recursively and returns the unique, sorted
// set of directories that DIRECTLY contain at least one *.gsx file. Empty paths
// default to ["."]. A path that is a single .gsx file contributes its containing
// directory. Directories named .git, vendor, node_modules, testdata, or any
// hidden (dot-prefixed) directory are skipped and not descended into. An error
// is returned if any given path does not exist.
// absAgainst resolves p against base when p is relative, returning a cleaned
// absolute path; an already-absolute p is returned cleaned, unchanged. base must
// itself be absolute. This is the reentrant replacement for filepath.Abs at the
// CLI path-resolution boundary: resolution depends on an explicit working
// directory (-C, else the startup cwd) instead of the process-global cwd, so the
// command never has to os.Chdir and can run concurrently (the gen test suite
// drives run/runConfig in-process and now runs in parallel).
func absAgainst(base, p string) string {
	if filepath.IsAbs(p) {
		return filepath.Clean(p)
	}
	return filepath.Join(base, p)
}

// absPaths resolves each CLI path argument against base (see absAgainst). An
// empty list defaults to base itself — the old "." default, now anchored at the
// chosen working directory rather than the process cwd.
func absPaths(base string, paths []string) []string {
	if len(paths) == 0 {
		return []string{base}
	}
	out := make([]string, len(paths))
	for i, p := range paths {
		out[i] = absAgainst(base, p)
	}
	return out
}

func discoverDirs(paths []string) ([]string, error) {
	if len(paths) == 0 {
		paths = []string{"."}
	}
	found := map[string]bool{}
	for _, p := range paths {
		info, err := os.Stat(p)
		if err != nil {
			return nil, err
		}
		if !info.IsDir() {
			if strings.HasSuffix(p, ".gsx") {
				found[filepath.Dir(p)] = true
			}
			continue
		}
		if err := walkForGsx(p, found); err != nil {
			return nil, err
		}
	}
	dirs := make([]string, 0, len(found))
	for d := range found {
		// Resolve to an absolute path: the codegen type resolver loads each
		// package via go/packages with an overlay keyed by absolute filenames,
		// so a relative dir (e.g. under a -C chdir) would fail to match the
		// overlay and report "no Go files". Absolute dirs also make Written
		// paths unambiguous for the CLI summary.
		abs, err := filepath.Abs(d)
		if err != nil {
			return nil, err
		}
		dirs = append(dirs, abs)
	}
	sort.Strings(dirs)
	return dirs, nil
}

// walkForGsx walks root, recording into found any directory that directly
// contains a .gsx file, while skipping junk directories.
func walkForGsx(root string, found map[string]bool) error {
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			// Never skip the root itself even if its name is junk-like (the
			// caller explicitly asked for it); only skip discovered subdirs.
			if path != root && shouldSkipDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(d.Name(), ".gsx") {
			found[filepath.Dir(path)] = true
		}
		return nil
	})
}

// Result reports the outcome of a Generate run. Written holds the paths of the
// .x.go files written to disk; Errs holds genuine operational errors (I/O,
// module-graph failures, write failures) — NOT codegen/type diagnostics. Diags
// holds all structured diagnostics (errors, warnings) collected from codegen.
//
// Files is populated by CachedResolver.Generate and by the BundledResolver
// GenerateSource(s) methods. It maps each .gsx path (relative, e.g.
// "views/comp.x.go") to its generated .x.go bytes. The top-level Generate
// function writes outputs and leaves Files nil.
type Result struct {
	Written []string
	Errs    []error
	Diags   []diag.Diagnostic
	Files   map[string][]byte
	// UpToDate counts output files that were already current on disk (byte-
	// identical, so no write happened). Lets the CLI report "N up to date"
	// instead of falling silent when a run produces no writes.
	UpToDate int
	// Removed holds the paths of gsx-owned orphan .x.go files deleted this run
	// — a .x.go whose corresponding .gsx no longer exists (see
	// removeOrphanXgo/sweepOrphanDirs in gen/orphan.go). Sorted.
	Removed []string
}

// Generate discovers .gsx files under the given paths (default ["."]), runs
// codegen per Go package directory, and writes each resulting .x.go to disk next
// to its .gsx source. Genuine operational errors (I/O, module-graph failures)
// are recorded in Result.Errs; error-severity diagnostics (type errors, codegen
// errors) are recorded in Result.Diags. The returned error is non-nil when any
// error occurred, with Result still populated for summary reporting.
func Generate(paths []string) (Result, error) {
	return generate(paths, nil, nil, nil)
}

// generate is the Generate core: it additionally takes the ordered filter
// package import paths to resolve pipelines against (last-wins by name). A nil
// or empty filterPkgs defaults to the built-in std package (codegen's
// dedupFilterPkgs applies the same empty→std default), so the public
// Generate stays stock std-only. The Main → runConfig → runGenerate path passes
// the config's WithFilters list here so a custom gsx binary's filter packages
// reach codegen.
//
// cssMin and jsMin are optional custom minifiers for holeless <style>/<script>
// blocks. When either is non-nil the incremental cache is bypassed (a func is
// not hashable), so each run re-generates. The built-in (nil) path keeps the cache.
func generate(paths []string, filterPkgs []string, cssMin, jsMin func(string) (string, error)) (Result, error) {
	return generateCached(paths, filterPkgs, nil, nil, attrclass.Builtin(), cssMin == nil && jsMin == nil, cssMin, jsMin, nil, true, true, nil)
}
