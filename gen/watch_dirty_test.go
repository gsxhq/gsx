package gen

import (
	"errors"
	"maps"
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
	if err != nil {
		t.Fatalf("per-directory failure became top-level regeneration error: %v", err)
	}
	if len(results) != 1 || !errors.Is(results[0].Err, wantErr) {
		t.Fatalf("operational result = %+v, want %v", results, wantErr)
	}
	if !maps.Equal(dirty.dirs, map[string]bool{"/module/ui": true}) {
		t.Fatalf("per-directory operational failure committed dirty state: %v", dirty.dirs)
	}
}
