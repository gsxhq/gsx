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
// .x.go files written to disk; Errs holds the per-directory errors encountered
// (each .x.go is written only when its whole package generated successfully).
type Result struct {
	Written []string
	Errs    []error
}

// Generate discovers .gsx files under the given paths (default ["."]), runs
// codegen per Go package directory, and writes each resulting .x.go to disk next
// to its .gsx source. One package's codegen failure is recorded in the returned
// Result.Errs and does not abort the others nor write a partial .x.go for that
// package. The returned error is non-nil when any error occurred (so callers can
// detect failure), with Result still populated for summary reporting.
func Generate(paths []string) (Result, error) {
	return generate(paths, nil, nil)
}

// generate is the Generate core: it additionally takes the ordered filter
// package import paths to resolve pipelines against (last-wins by name). A nil
// or empty filterPkgs defaults to the built-in std package (codegen's
// GeneratePackageWithFilters applies the same empty→std default), so the public
// Generate stays stock std-only. The Main → runConfig → runGenerate path passes
// the config's WithFilters list here so a custom gsx binary's filter packages
// reach codegen.
//
// cssMin is an optional custom CSS minifier for holeless <style> blocks. When
// non-nil the incremental cache is bypassed (a func is not hashable), so each
// run re-generates. The built-in (nil) path keeps the cache.
func generate(paths []string, filterPkgs []string, cssMin func(string) (string, error)) (Result, error) {
	return generateCached(paths, filterPkgs, cssMin == nil, cssMin)
}
