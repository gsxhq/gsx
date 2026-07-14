package gen

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/gsxhq/gsx/internal/codegen"
	"golang.org/x/mod/module"
)

// moduleGroup is a set of package directories that share one enclosing module,
// rooted at root with the declared module path modPath. All loading (go/packages,
// cache keys, manifest) for these dirs must be anchored at root.
type moduleGroup struct {
	root    string
	modPath string
	dirs    []string
}

// groupByModule partitions dirs by their nearest enclosing module (go.mod): each
// dir is mapped via moduleRoot to its owning module, and dirs sharing a root are
// grouped. A dir inside a nested module groups under that nested module, not its
// ancestor — so descending past a go.mod boundary starts a new, separately-loaded
// module. Groups are returned sorted by root for deterministic processing; within
// a group the input dir order is preserved. Dirs with no go.mod above them are
// returned in noModule (input order preserved).
func groupByModule(dirs []string) (groups []moduleGroup, noModule []string) {
	byRoot := map[string]*moduleGroup{}
	var order []string
	for _, dir := range dirs {
		root, modPath, err := moduleRoot(dir)
		if err != nil {
			noModule = append(noModule, dir)
			continue
		}
		g, ok := byRoot[root]
		if !ok {
			g = &moduleGroup{root: root, modPath: modPath}
			byRoot[root] = g
			order = append(order, root)
		}
		g.dirs = append(g.dirs, dir)
	}
	sort.Strings(order)
	for _, root := range order {
		groups = append(groups, *byRoot[root])
	}
	return groups, noModule
}

// moduleRoot walks up from dir to the nearest go.mod, returning its directory
// and the declared module path.
func moduleRoot(dir string) (string, string, error) {
	d, err := filepath.Abs(dir)
	if err != nil {
		return "", "", err
	}
	for {
		gomod := filepath.Join(d, "go.mod")
		if data, err := os.ReadFile(gomod); err == nil {
			return d, codegen.ModulePathFromGoMod(data), nil
		}
		parent := filepath.Dir(d)
		if parent == d {
			return "", "", fmt.Errorf("gen: no go.mod found above %s", dir)
		}
		d = parent
	}
}

// moduleDirForImportPath maps an exact module import path to an existing
// directory contained by moduleRoot. It validates the raw slash-separated path
// before converting it to a filesystem path so filepath.Clean cannot turn dot
// segments into traversal. Both the lexical candidate and its resolved symlink
// target must remain under the corresponding module root; an in-root symlink is
// allowed, while a symlink escape is not.
func moduleDirForImportPath(moduleRoot, modulePath, importPath string) (string, bool) {
	if moduleRoot == "" || module.CheckImportPath(modulePath) != nil || module.CheckImportPath(importPath) != nil {
		return "", false
	}
	var rel string
	switch {
	case importPath == modulePath:
	case strings.HasPrefix(importPath, modulePath+"/"):
		rel = strings.TrimPrefix(importPath, modulePath+"/")
	default:
		return "", false
	}
	root, err := filepath.Abs(moduleRoot)
	if err != nil {
		return "", false
	}
	root = filepath.Clean(root)
	candidate := root
	if rel != "" {
		candidate = filepath.Join(root, filepath.FromSlash(rel))
	}
	if !dirContainedBy(root, candidate) {
		return "", false
	}
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", false
	}
	resolvedCandidate, err := filepath.EvalSymlinks(candidate)
	if err != nil || !dirContainedBy(resolvedRoot, resolvedCandidate) {
		return "", false
	}
	info, err := os.Stat(candidate)
	if err != nil || !info.IsDir() {
		return "", false
	}
	return candidate, true
}

func dirContainedBy(root, candidate string) bool {
	rel, err := filepath.Rel(root, candidate)
	if err != nil || filepath.IsAbs(rel) {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
