package gen

import (
	"os"
	"path/filepath"
	"strings"
)

// outputSnapshot records the stat identity of every paired .x.go in a
// session's GSX dirs, absent ones included. gsx dev takes one when its
// speculative build starts: the watcher does not observe paired outputs, so an
// outside rewrite of one (a git checkout of committed .x.go, say) that leaves
// the initial generate nothing to write shows up only here.
type outputSnapshot struct {
	paths []string
	ids   []fileIdentity // nil: some identity is unknown, so treat as changed
}

func (s *watchSession) snapshotOutputs() outputSnapshot {
	dirs, err := discoverDirs(s.cfg.paths)
	if err != nil {
		return outputSnapshot{}
	}
	var paths []string
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return outputSnapshot{}
		}
		for _, e := range entries {
			if name := e.Name(); !e.IsDir() && strings.HasSuffix(name, ".gsx") {
				paths = append(paths, filepath.Join(dir, strings.TrimSuffix(name, ".gsx")+".x.go"))
			}
		}
	}
	ids, err := statSnapshot(paths)
	if err != nil {
		return outputSnapshot{}
	}
	return outputSnapshot{paths: paths, ids: ids}
}

// changed reports whether any recorded output's identity differs now.
func (o outputSnapshot) changed() bool {
	if o.ids == nil {
		return true
	}
	now, err := statSnapshot(o.paths)
	if err != nil {
		return true
	}
	_, changed := firstChanged(o.paths, o.ids, now)
	return changed
}
