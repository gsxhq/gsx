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

	_, _, err := dirty.regenerate(func(dirs map[string]bool, depDirty bool) ([]cycleResult, error) {
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
	results, goChanged, err := dirty.regenerate(func(dirs map[string]bool, depDirty bool) ([]cycleResult, error) {
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

	results, _, err := dirty.regenerate(func(map[string]bool, bool) ([]cycleResult, error) {
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

	results, _, err := dirty.regenerate(func(map[string]bool, bool) ([]cycleResult, error) {
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

	results, _, err := dirty.regenerate(func(map[string]bool, bool) ([]cycleResult, error) {
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

	results, _, err = dirty.regenerate(func(map[string]bool, bool) ([]cycleResult, error) {
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

	results, _, err := dirty.regenerate(func(map[string]bool, bool) ([]cycleResult, error) {
		return []cycleResult{{Dir: "/module/a", Removed: []string{"/module/a/a.x.go"}, OK: true}}, refreshErr
	})
	if !errors.Is(err, refreshErr) || len(results) != 0 {
		t.Fatalf("failed cycle = (%+v, %v), want no committed results and refresh error", results, err)
	}
	results, _, err = dirty.regenerate(func(map[string]bool, bool) ([]cycleResult, error) {
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
