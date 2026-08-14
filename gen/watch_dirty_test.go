package gen

import (
	"errors"
	"maps"
	"slices"
	"testing"
)

func TestWatchDirtySetCommitsOnlySuccessfulRegeneration(t *testing.T) {
	dirty := newWatchDirtySet()
	dirty.dirs["/module/ui"] = true
	dirty.depDirty = true
	wantErr := errors.New("saved source temporarily unreadable")

	_, _, err := dirty.regenerate(func(dirs, goDirs map[string]bool, depDirty bool) ([]cycleResult, error) {
		if !maps.Equal(dirs, map[string]bool{"/module/ui": true}) || !depDirty {
			t.Fatalf("first attempt = (%v, %v), want original complete dirty state", dirs, depDirty)
		}
		return nil, wantErr
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("first attempt error = %v, want %v", err, wantErr)
	}
	if !maps.Equal(dirty.dirs, map[string]bool{"/module/ui": true}) || !dirty.depDirty {
		t.Fatalf("failed attempt committed dirty state: (%v, %v)", dirty.dirs, dirty.depDirty)
	}

	// A later relevant event accumulates into the retained state. The retry must
	// receive the complete union and clear it only after succeeding.
	dirty.dirs["/module/pages"] = true
	wantResults := []cycleResult{{Dir: "/module/ui", OK: true}, {Dir: "/module/pages", OK: true}}
	results, goChanged, err := dirty.regenerate(func(dirs, goDirs map[string]bool, depDirty bool) ([]cycleResult, error) {
		wantDirs := map[string]bool{"/module/ui": true, "/module/pages": true}
		if !maps.Equal(dirs, wantDirs) || !depDirty {
			t.Fatalf("retry = (%v, %v), want (%v, true)", dirs, depDirty, wantDirs)
		}
		return wantResults, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !goChanged {
		t.Fatal("successful retry lost dependency-change provenance")
	}
	if len(results) != len(wantResults) {
		t.Fatalf("results = %v, want %v", results, wantResults)
	}
	if len(dirty.dirs) != 0 || dirty.depDirty {
		t.Fatalf("successful retry did not commit clear: (%v, %v)", dirty.dirs, dirty.depDirty)
	}
}

func TestWatchDirtySetTreatsDiagnosticCycleAsCompleted(t *testing.T) {
	dirty := newWatchDirtySet()
	dirty.dirs["/module/ui"] = true

	results, _, err := dirty.regenerate(func(map[string]bool, map[string]bool, bool) ([]cycleResult, error) {
		return []cycleResult{{Dir: "/module/ui", OK: false}}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].OK {
		t.Fatalf("diagnostic result = %+v", results)
	}
	if len(dirty.dirs) != 0 || dirty.depDirty {
		t.Fatalf("completed diagnostic cycle retained dirty state: (%v, %v)", dirty.dirs, dirty.depDirty)
	}
}

func TestWatchDirtySetRetainsPerDirectoryOperationalFailure(t *testing.T) {
	dirty := newWatchDirtySet()
	dirty.dirs["/module/ui"] = true
	wantErr := errors.New("write generated output: disk full")

	results, _, err := dirty.regenerate(func(map[string]bool, map[string]bool, bool) ([]cycleResult, error) {
		return []cycleResult{{Dir: "/module/ui", OK: false, Err: wantErr}}, nil
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("per-directory failure = %v, want %v", err, wantErr)
	}
	if len(results) != 0 {
		t.Fatalf("failed partial results were published as committed: %+v", results)
	}
	if !maps.Equal(dirty.dirs, map[string]bool{"/module/ui": true}) {
		t.Fatalf("per-directory operational failure committed dirty state: %v", dirty.dirs)
	}
}

func TestWatchDirtySetCarriesFailedFilesystemEffectsIntoSuccessfulCommit(t *testing.T) {
	dirty := newWatchDirtySet()
	dirty.dirs["/module/a"] = true
	dirty.dirs["/module/b"] = true
	diskFull := errors.New("disk full")

	results, _, err := dirty.regenerate(func(map[string]bool, map[string]bool, bool) ([]cycleResult, error) {
		return []cycleResult{
			{Dir: "/module/a", Written: []string{"/module/a/a.x.go"}, Removed: []string{"/module/a/old.x.go"}, OK: true},
			{Dir: "/module/b", Err: diskFull},
		}, nil
	})
	if !errors.Is(err, diskFull) {
		t.Fatalf("failed cycle error = %v, want %v", err, diskFull)
	}
	if len(results) != 0 {
		t.Fatalf("failed partial results were published as committed: %+v", results)
	}

	results, _, err = dirty.regenerate(func(map[string]bool, map[string]bool, bool) ([]cycleResult, error) {
		// The retry is effect-free because the first attempt already changed disk.
		return []cycleResult{{Dir: "/module/a", OK: true}, {Dir: "/module/b", OK: true}}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var written, removed []string
	for _, result := range results {
		written = append(written, result.Written...)
		removed = append(removed, result.Removed...)
	}
	if !slices.Equal(written, []string{"/module/a/a.x.go"}) || !slices.Equal(removed, []string{"/module/a/old.x.go"}) {
		t.Fatalf("committed effects = written %v, removed %v", written, removed)
	}
}

func TestWatchDirtySetCarriesEffectsReturnedWithTopLevelFailure(t *testing.T) {
	dirty := newWatchDirtySet()
	dirty.dirs["/module/a"] = true
	dirty.dirs["/module/b"] = true
	refreshErr := errors.New("refresh b")

	results, _, err := dirty.regenerate(func(map[string]bool, map[string]bool, bool) ([]cycleResult, error) {
		return []cycleResult{{Dir: "/module/a", Removed: []string{"/module/a/a.x.go"}, OK: true}}, refreshErr
	})
	if !errors.Is(err, refreshErr) || len(results) != 0 {
		t.Fatalf("failed cycle = (%+v, %v), want no committed results and refresh error", results, err)
	}
	results, _, err = dirty.regenerate(func(map[string]bool, map[string]bool, bool) ([]cycleResult, error) {
		return nil, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || !slices.Equal(results[0].Removed, []string{"/module/a/a.x.go"}) {
		t.Fatalf("committed retained removal = %+v", results)
	}
}

func TestWatchDirtySetRetainsInitialOperationalFailures(t *testing.T) {
	dirty := newWatchDirtySet()
	dirty.retainOperational([]cycleResult{
		{Dir: "/module/a", OK: true},
		{Dir: "/module/b", Written: []string{"/module/b/b.x.go"}, Err: errors.New("rename failed")},
		{Err: errors.New("orphan sweep failed")},
	})
	if !maps.Equal(dirty.dirs, map[string]bool{"/module/b": true}) {
		t.Fatalf("initial dirty dirs = %v, want /module/b", dirty.dirs)
	}
	if !dirty.depDirty {
		t.Fatal("unscoped startup failure did not retain full-session dirtiness")
	}
}

// TestWatchDirtySetGoDirtyForcesRebuildWithoutDepDirty pins the goDirs lane's
// commit half: an authored-Go save seeds goDirs (never dirs — see
// classifyDirtyFile), reaches regen on the goDirs argument, forces the server
// rebuild without depDirty, and clears BOTH the lane and its latch on success.
func TestWatchDirtySetGoDirtyForcesRebuildWithoutDepDirty(t *testing.T) {
	dirty := newWatchDirtySet()
	dirty.goDirs["/module/dep"] = true
	dirty.goDirty = true

	results, rebuild, err := dirty.regenerate(func(dirs, goDirs map[string]bool, depDirty bool) ([]cycleResult, error) {
		if depDirty {
			t.Fatalf("goDirty-only cycle passed depDirty=true to regen")
		}
		if !maps.Equal(goDirs, map[string]bool{"/module/dep": true}) {
			t.Fatalf("regen goDirs = %v, want /module/dep", goDirs)
		}
		if len(dirs) != 0 {
			t.Fatalf("regen dirs = %v, want empty — an authored-Go save must not take the .gsx lane", dirs)
		}
		return []cycleResult{{Dir: "/module/page", OK: true}}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !rebuild {
		t.Fatal("goDirty cycle must report rebuild=true even though depDirty stayed false")
	}
	if len(results) != 1 || results[0].Dir != "/module/page" {
		t.Fatalf("results = %+v, want /module/page", results)
	}
	if dirty.goDirty {
		t.Fatal("successful commit did not clear goDirty")
	}
	if len(dirty.goDirs) != 0 {
		t.Fatalf("successful commit did not clear goDirs: %v — the next cycle would re-refresh and re-regenerate an already-committed Go dir", dirty.goDirs)
	}
	if len(dirty.dirs) != 0 {
		t.Fatalf("successful commit did not clear dirs: %v", dirty.dirs)
	}
}

// TestWatchDirtySetRetainsGoDirtyAcrossOperationalFailure pins the retry
// contract for a go-edit cycle: a per-directory operational failure must not
// drop goDirty, so the next relevant event's retry still regenerates as a
// go-cycle (in place) rather than silently downgrading to an ordinary .gsx-
// only regen that would leave the dependent closure on stale types.
//
// It also pins the lane itself, which the latch alone cannot express. The
// dirty set is seeded on BOTH lanes — a .go save in dep/ and a .gsx save in
// views/ inside one debounce window — and the failing directory is the Go
// seed, so the test distinguishes three things a retry could get wrong:
// dropping goDirs, migrating the failed Go dir onto the .gsx lane (which
// would refresh it through a path that cannot swap its syntax), and losing
// the unrelated .gsx dir that shared the failed transaction.
func TestWatchDirtySetRetainsGoDirtyAcrossOperationalFailure(t *testing.T) {
	dirty := newWatchDirtySet()
	dirty.goDirs["/module/dep"] = true
	dirty.goDirty = true
	dirty.dirs["/module/views"] = true
	wantErr := errors.New("refresh dep: temporarily unreadable")

	_, rebuild, err := dirty.regenerate(func(dirs, goDirs map[string]bool, depDirty bool) ([]cycleResult, error) {
		if !maps.Equal(goDirs, map[string]bool{"/module/dep": true}) {
			t.Fatalf("first attempt goDirs = %v, want /module/dep", goDirs)
		}
		if !maps.Equal(dirs, map[string]bool{"/module/views": true}) {
			t.Fatalf("first attempt dirs = %v, want /module/views", dirs)
		}
		return []cycleResult{{Dir: "/module/dep", Err: wantErr}}, nil
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("first attempt error = %v, want %v", err, wantErr)
	}
	if !rebuild {
		t.Fatal("failed go-cycle must still report rebuild=true")
	}
	if !dirty.goDirty {
		t.Fatal("failed go-cycle dropped goDirty; a retry would no longer retry as a go-cycle")
	}
	if !maps.Equal(dirty.goDirs, map[string]bool{"/module/dep": true}) {
		t.Fatalf("failed attempt did not retain goDirs: %v — the retry would regenerate the closure without refreshing the edited Go dir", dirty.goDirs)
	}
	if !maps.Equal(dirty.dirs, map[string]bool{"/module/views": true}) {
		t.Fatalf("failed attempt dirs = %v, want exactly /module/views: the failed Go seed must be retried on the lane it arrived on, not migrated to the .gsx lane", dirty.dirs)
	}

	results, rebuild, err := dirty.regenerate(func(dirs, goDirs map[string]bool, depDirty bool) ([]cycleResult, error) {
		if depDirty {
			t.Fatalf("retained go-cycle retry passed depDirty=true to regen")
		}
		if !maps.Equal(goDirs, map[string]bool{"/module/dep": true}) {
			t.Fatalf("retry goDirs = %v, want the retained /module/dep", goDirs)
		}
		if !maps.Equal(dirs, map[string]bool{"/module/views": true}) {
			t.Fatalf("retry dirs = %v, want the retained /module/views", dirs)
		}
		return []cycleResult{{Dir: "/module/page", OK: true}}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !rebuild {
		t.Fatal("successful retry of a retained go-cycle must report rebuild=true")
	}
	if len(results) != 1 || results[0].Dir != "/module/page" {
		t.Fatalf("results = %+v, want /module/page", results)
	}
	if dirty.goDirty {
		t.Fatal("successful retry did not clear goDirty")
	}
	if len(dirty.goDirs) != 0 || len(dirty.dirs) != 0 {
		t.Fatalf("successful retry did not clear both lanes: goDirs=%v dirs=%v", dirty.goDirs, dirty.dirs)
	}
}

func TestStartupPublicationHidesUncommittedFilesystemEffects(t *testing.T) {
	opErr := errors.New("rename failed")
	startup := []cycleResult{
		{Dir: "/module/a", Written: []string{"/module/a/a.x.go"}, OK: true},
		{Dir: "/module/b", Removed: []string{"/module/b/old.x.go"}, Err: opErr},
	}
	published := publishableStartupResults(startup)
	if len(published) != 1 || !errors.Is(published[0].Err, opErr) {
		t.Fatalf("published startup = %+v, want only operational failure", published)
	}
	if len(published[0].Written) != 0 || len(published[0].Removed) != 0 {
		t.Fatalf("published uncommitted effects: %+v", published[0])
	}

	committed := publishableStartupResults([]cycleResult{{Dir: "/module/a", Written: []string{"/module/a/a.x.go"}, OK: true}})
	if len(committed) != 1 || len(committed[0].Written) != 1 {
		t.Fatalf("successful startup effects were hidden: %+v", committed)
	}
}
