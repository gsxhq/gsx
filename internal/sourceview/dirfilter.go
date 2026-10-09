package sourceview

import (
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/mod/modfile"
)

// ExcludedDirName reports whether the CLI walks (generate, fmt, orphan sweep,
// watch module scan) skip a directory by name: dot-prefixed (the go command
// ignores them and no import path can name one), node_modules, vendor, or
// testdata. These walks select what `gsx generate .` covers, like a ./...
// pattern; the source manifest instead owns every importable package (see
// importableDirName).
func ExcludedDirName(name string) bool {
	if name == "" || name == "." || name == ".." {
		return false
	}
	if strings.HasPrefix(name, ".") {
		return true
	}
	switch name {
	case "node_modules", "vendor", "testdata":
		return true
	}
	return false
}

// DirFilter is the directory rule the CLI walks share: a directory is excluded
// when its name is (ExcludedDirName) or when the go.mod of the module that
// contains it ignores it (the go.mod ignore directive, Go 1.25). Like testdata,
// an ignored directory stays importable, so the manifest does not apply this.
// Module lookups are cached, so one filter serves a whole walk; it is not safe
// for concurrent use.
type DirFilter struct {
	moduleOf map[string]string          // dir -> enclosing module root ("" when none)
	ignores  map[string]*ignorePatterns // module root -> its go.mod ignore patterns
}

// NewDirFilter returns an empty filter.
func NewDirFilter() *DirFilter {
	return &DirFilter{moduleOf: map[string]string{}, ignores: map[string]*ignorePatterns{}}
}

// Excluded reports whether a walk must not descend into dir. It is a per-step
// check on dir itself, so a walk stops at the first excluded directory.
// Callers apply it below their walk root only: an explicitly requested root is
// always walked.
func (f *DirFilter) Excluded(dir string) bool {
	if ExcludedDirName(filepath.Base(dir)) {
		return true
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return false
	}
	// The module seen from the parent: a nested module's own go.mod does not
	// decide whether its parent module ignores it.
	root := f.module(filepath.Dir(abs))
	if root == "" {
		return false
	}
	rel, err := filepath.Rel(root, abs)
	if err != nil {
		return false
	}
	return f.patterns(root).ignores(rel)
}

// module returns the root of the module containing dir: the nearest directory,
// dir itself included, holding a go.mod.
func (f *DirFilter) module(dir string) string {
	if root, ok := f.moduleOf[dir]; ok {
		return root
	}
	var root string
	if info, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil && info.Mode().IsRegular() {
		root = dir
	} else if parent := filepath.Dir(dir); parent != dir {
		root = f.module(parent)
	}
	f.moduleOf[dir] = root
	return root
}

func (f *DirFilter) patterns(root string) *ignorePatterns {
	if p, ok := f.ignores[root]; ok {
		return p
	}
	p := moduleIgnorePatterns(root)
	f.ignores[root] = p
	return p
}

// ignorePatterns is a port of cmd/go/internal/search.IgnorePatterns: an
// "./x" pattern ignores x at the module root and its subtree; an "x" pattern
// ignores a directory x at any depth. No wildcards.
type ignorePatterns struct {
	relative []string
	any      []string
}

// moduleIgnorePatterns reads the ignore directives of root's go.mod. Like the
// go command, an unreadable or unparsable go.mod ignores nothing.
func moduleIgnorePatterns(root string) *ignorePatterns {
	p := &ignorePatterns{}
	data, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		return p
	}
	file, err := modfile.Parse("go.mod", data, nil)
	if err != nil {
		return p
	}
	for _, ignore := range file.Ignore {
		path, relative := strings.CutPrefix(ignore.Path, "./")
		path = normalizeIgnorePath(path)
		if relative {
			p.relative = append(p.relative, path)
		} else {
			p.any = append(p.any, path)
		}
	}
	return p
}

// ignores reports whether the module-relative dir is ignored.
func (p *ignorePatterns) ignores(rel string) bool {
	if rel == "" || rel == "." {
		return false
	}
	dir := normalizeIgnorePath(rel)
	for _, pattern := range p.relative {
		if strings.HasPrefix(dir, pattern) {
			return true
		}
	}
	for _, pattern := range p.any {
		if strings.Contains(dir, pattern) {
			return true
		}
	}
	return false
}

// normalizeIgnorePath slashes a path and wraps it in leading and trailing
// slashes, as cmd/go's normalizePath does.
func normalizeIgnorePath(path string) string {
	path = filepath.ToSlash(path)
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	if !strings.HasSuffix(path, "/") {
		path += "/"
	}
	return path
}
