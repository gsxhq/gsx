package gen

import "maps"

// watchDirtySet is the uncommitted source state observed by a watch loop. A
// regeneration is transactional: an operational failure retains the complete
// set for the next relevant event. A cycle containing only authored diagnostics
// is complete and commits the clear; a per-directory operational Err retains the
// transaction just like a top-level regeneration error. Watch and dev mutate it
// only on their respective event-loop goroutine.
type watchDirtySet struct {
	dirs     map[string]bool
	depDirty bool
}

func newWatchDirtySet() *watchDirtySet {
	return &watchDirtySet{dirs: map[string]bool{}}
}

func (d *watchDirtySet) regenerate(regen func(map[string]bool, bool) ([]cycleResult, error)) ([]cycleResult, bool, error) {
	dirs := maps.Clone(d.dirs)
	depDirty := d.depDirty
	results, err := regen(dirs, depDirty)
	if err != nil {
		return nil, depDirty, err
	}
	for _, result := range results {
		if result.Err != nil {
			return results, depDirty, nil
		}
	}
	d.dirs = map[string]bool{}
	d.depDirty = false
	return results, depDirty, nil
}
