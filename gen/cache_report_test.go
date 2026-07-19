package gen

import (
	"strings"
	"testing"
)

func TestCacheReportCountsAndVerboseLines(t *testing.T) {
	report := cacheReport{Modules: []moduleCacheReport{
		{Root: "/one", Dirs: []cacheDirReport{
			{Dir: "/one/a", Decision: cacheDecisionHit, Reason: cacheReasonEntryHit},
			{Dir: "/one/b", Decision: cacheDecisionMiss, Reason: cacheReasonEntryMissing},
		}},
		{Root: "/two", Dirs: []cacheDirReport{
			{Dir: "/two/c", Decision: cacheDecisionUncacheable, Reason: cacheReasonKeyFailed, Detail: "reachable mutable cgo"},
		}},
	}}
	hits, misses, uncacheable := report.counts()
	if hits != 1 || misses != 1 || uncacheable != 1 {
		t.Fatalf("counts = %d/%d/%d, want 1/1/1", hits, misses, uncacheable)
	}
	lines := strings.Join(report.verboseLines(), "\n")
	for _, want := range []string{"1 hit, 1 miss, 1 uncacheable", "/two/c", "key-failed", "reachable mutable cgo"} {
		if !strings.Contains(lines, want) {
			t.Fatalf("verbose report %q does not contain %q", lines, want)
		}
	}
}
