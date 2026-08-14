package gen

import (
	"errors"
	"fmt"
	"maps"
	"sort"
)

// watchDirtySet is the uncommitted source state observed by a watch loop. A
// regeneration is transactional: an operational failure retains the complete
// set — dirs, goDirs and depDirty alike — for the next relevant event. A cycle
// containing only authored diagnostics is complete and commits the clear; a
// per-directory operational Err retains the transaction just like a top-level
// regeneration error. Watch and dev mutate it only on their respective
// event-loop goroutine.
type watchDirtySet struct {
	dirs map[string]bool
	// goDirs holds the directories whose authored .go source changed (not
	// go.mod/go.sum). They are tracked apart from dirs because regenPending
	// routes them differently: a Go dir refreshes through
	// Module.RefreshGoSourcesAndInvalidate (whose warm syntax swap can keep the
	// cycle reload-free) and contributes the GSX projection of its PRE-change
	// closure, while a dirs entry refreshes its own .gsx facts and contributes
	// Dependents. A directory saved on both paths at once (helper.go + page.gsx
	// in one editor save-all) appears in both maps and is refreshed exactly
	// once — see regenPending.
	goDirs   map[string]bool
	depDirty bool
	// goDirty is the rebuild latch for the goDirs lane: it stays true for the
	// whole transaction even when a later partial classification would not
	// re-seed goDirs. It does not route through reopen() — regenPending
	// regenerates only the dependent closure — but it still forces a server
	// rebuild, since the closure's generated .x.go can carry fresh types even
	// when no .gsx byte changed, and since a warm (reload-free) Go cycle can
	// legitimately produce no cycleResult at all.
	goDirty bool
	effects map[string]*watchEffects
}

type watchEffects struct {
	written map[string]bool
	removed map[string]bool
}

func newWatchDirtySet() *watchDirtySet {
	return &watchDirtySet{dirs: map[string]bool{}, goDirs: map[string]bool{}, effects: map[string]*watchEffects{}}
}

// empty reports whether the set holds nothing to regenerate. Callers that only
// run a cycle for observed work (gsx dev) must consult all three lanes: an
// authored-Go save seeds goDirs alone, and skipping it would strand both the
// closure regeneration and the server rebuild.
func (d *watchDirtySet) empty() bool {
	return len(d.dirs) == 0 && len(d.goDirs) == 0 && !d.depDirty
}

// regenerate runs one regeneration pass via regen (sess.regenPending), and
// reports whether the caller must rebuild the server binary. rebuild is true
// whenever depDirty or goDirty was set for this cycle: both a dep-surface
// reopen and an in-place go-edit closure regen can change compiled Go types
// that server.rebuild must pick up, even on cycles that wrote zero .x.go
// bytes (a signature-only change with unchanged text output). It is NOT
// cleared when the cycle fails — see the two error returns below — so a
// retained failed go-cycle retries as a go-cycle and still forces the rebuild
// once it lands.
func (d *watchDirtySet) regenerate(regen func(map[string]bool, map[string]bool, bool) ([]cycleResult, error)) ([]cycleResult, bool, error) {
	dirs := maps.Clone(d.dirs)
	goDirs := maps.Clone(d.goDirs)
	depDirty := d.depDirty
	rebuild := depDirty || d.goDirty
	results, err := regen(dirs, goDirs, depDirty)
	if err != nil {
		// regenPending may discover a fatal refresh/reopen error after an earlier
		// directory already mutated generated files. The top-level error keeps the
		// whole dirty input, while these effects must also survive the retry.
		for _, result := range results {
			d.retainEffects(result)
		}
		return nil, rebuild, err
	}
	if err := cycleOperationalError(results); err != nil {
		d.retainOperational(results)
		// A cycle that performed some writes and then failed is not a commit.
		// Callers surface the operational error, while the effect provenance stays
		// private until a later complete retry commits the transaction.
		return nil, rebuild, err
	}
	results = d.commitEffects(results)
	d.dirs = map[string]bool{}
	d.goDirs = map[string]bool{}
	d.depDirty = false
	d.goDirty = false
	return results, rebuild, nil
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
		switch {
		case result.Dir == "":
			d.depDirty = true
		case d.goDirs[result.Dir]:
			// Already retained on the lane it arrived on (the whole transaction
			// survives a failed cycle), so re-seeding it into dirs would only
			// add a .gsx-path refresh the directory never asked for.
		default:
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
