package gen

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
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
			return d, modulePathFromGoMod(data), nil
		}
		parent := filepath.Dir(d)
		if parent == d {
			return "", "", fmt.Errorf("gen: no go.mod found above %s", dir)
		}
		d = parent
	}
}

func modulePathFromGoMod(data []byte) string {
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "module ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "module "))
		}
	}
	return ""
}
