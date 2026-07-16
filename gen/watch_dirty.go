package gen

import (
	"errors"
	"fmt"
	"maps"
	"sort"
)

// watchDirtySet is the uncommitted source state observed by a watch loop. A
// regeneration is transactional: an operational failure retains the complete
// set for the next relevant event. A cycle containing only authored diagnostics
// is complete and commits the clear; a per-directory operational Err retains the
// transaction just like a top-level regeneration error. Watch and dev mutate it
// only on their respective event-loop goroutine.
type watchDirtySet struct {
	dirs     map[string]bool
	depDirty bool
	effects  map[string]*watchEffects
}

type watchEffects struct {
	written map[string]bool
	removed map[string]bool
}

func newWatchDirtySet() *watchDirtySet {
	return &watchDirtySet{dirs: map[string]bool{}, effects: map[string]*watchEffects{}}
}

func (d *watchDirtySet) regenerate(regen func(map[string]bool, bool) ([]cycleResult, error)) ([]cycleResult, bool, error) {
	dirs := maps.Clone(d.dirs)
	depDirty := d.depDirty
	results, err := regen(dirs, depDirty)
	if err != nil {
		// regenPending may discover a fatal refresh/reopen error after an earlier
		// directory already mutated generated files. The top-level error keeps the
		// whole dirty input, while these effects must also survive the retry.
		for _, result := range results {
			d.retainEffects(result)
		}
		return nil, depDirty, err
	}
	if err := cycleOperationalError(results); err != nil {
		d.retainOperational(results)
		// A cycle that performed some writes and then failed is not a commit.
		// Callers surface the operational error, while the effect provenance stays
		// private until a later complete retry commits the transaction.
		return nil, depDirty, err
	}
	results = d.commitEffects(results)
	d.dirs = map[string]bool{}
	d.depDirty = false
	return results, depDirty, nil
}

// retainOperational seeds or extends an uncommitted transaction from a cycle
// containing per-directory operational failures. Scoped failures retry their
// directory; an unscoped failure (for example the startup orphan sweep) forces
// a complete reopen because no narrower authoritative retry exists. Every
// filesystem mutation is retained, including mutations from successful
// siblings in a mixed failed cycle.
func (d *watchDirtySet) retainOperational(results []cycleResult) {
	failed := false
	for _, result := range results {
		if result.Err == nil {
			continue
		}
		failed = true
		if result.Dir == "" {
			d.depDirty = true
		} else {
			d.dirs[result.Dir] = true
		}
	}
	if !failed {
		return
	}
	for _, result := range results {
		d.retainEffects(result)
	}
}

func (d *watchDirtySet) retainEffects(result cycleResult) {
	if len(result.Written) == 0 && len(result.Removed) == 0 {
		return
	}
	effects := d.effects[result.Dir]
	if effects == nil {
		effects = &watchEffects{written: map[string]bool{}, removed: map[string]bool{}}
		d.effects[result.Dir] = effects
	}
	for _, path := range result.Written {
		effects.written[path] = true
	}
	for _, path := range result.Removed {
		effects.removed[path] = true
	}
}

func (d *watchDirtySet) commitEffects(results []cycleResult) []cycleResult {
	if len(d.effects) == 0 {
		return results
	}
	byDir := make(map[string]int, len(results))
	for i := range results {
		byDir[results[i].Dir] = i
	}
	dirs := make([]string, 0, len(d.effects))
	for dir := range d.effects {
		dirs = append(dirs, dir)
	}
	sort.Strings(dirs)
	for _, dir := range dirs {
		i, ok := byDir[dir]
		if !ok {
			results = append(results, cycleResult{Dir: dir, OK: true})
			i = len(results) - 1
		}
		results[i].Written = appendSet(results[i].Written, d.effects[dir].written)
		results[i].Removed = appendSet(results[i].Removed, d.effects[dir].removed)
	}
	d.effects = map[string]*watchEffects{}
	return results
}

func appendSet(paths []string, retained map[string]bool) []string {
	set := make(map[string]bool, len(paths)+len(retained))
	for _, path := range paths {
		set[path] = true
	}
	for path := range retained {
		set[path] = true
	}
	paths = paths[:0]
	for path := range set {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths
}

func cycleOperationalError(results []cycleResult) error {
	var errs []error
	for _, result := range results {
		if result.Err == nil {
			continue
		}
		if result.Dir == "" {
			errs = append(errs, result.Err)
		} else {
			errs = append(errs, fmt.Errorf("regenerate %s: %w", result.Dir, result.Err))
		}
	}
	return errors.Join(errs...)
}

// publishableStartupResults preserves ordinary successful startup reporting,
// but an operationally failed mixed startup has not committed its filesystem
// transaction. In that case only diagnostics/errors are surfaced and all
// Written/Removed provenance remains private in watchDirtySet until a complete
// retry succeeds.
func publishableStartupResults(results []cycleResult) []cycleResult {
	if cycleOperationalError(results) == nil {
		return results
	}
	published := make([]cycleResult, 0, len(results))
	for _, result := range results {
		if result.Err == nil && result.OK && len(result.Diags) == 0 {
			continue
		}
		result.Written = nil
		result.Removed = nil
		published = append(published, result)
	}
	return published
}
