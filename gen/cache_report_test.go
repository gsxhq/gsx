package gen

import (
	"strings"
	"testing"
)

func TestCacheReportCountsAndVerboseLines(t *testing.T) {
	report := cacheReport{Modules: []moduleCacheReport{
		{Root: "/one", Graph: graphCacheOutcome{Reason: graphHit}, Dirs: []cacheDirReport{
			{Dir: "/one/a", Decision: cacheDecisionHit, Reason: cacheReasonEntryHit},
			{Dir: "/one/b", Decision: cacheDecisionMiss, Reason: cacheReasonEntryMissing},
		}},
		{Root: "/two", Graph: graphCacheOutcome{Reason: graphUncacheable, Detail: "directory-replaced module"}, Dirs: []cacheDirReport{
			{Dir: "/two/c", Decision: cacheDecisionUncacheable, Reason: cacheReasonKeyFailed, Detail: "reachable mutable cgo"},
		}},
	}}
	hits, misses, uncacheable := report.counts()
	if hits != 1 || misses != 1 || uncacheable != 1 {
		t.Fatalf("counts = %d/%d/%d, want 1/1/1", hits, misses, uncacheable)
	}
	lines := strings.Join(report.verboseLines(), "\n")
	for _, want := range []string{"1 hit, 1 miss, 1 uncacheable", "/two/c", "key-failed", "reachable mutable cgo",
		"gsx cache: graph hit\n", "gsx cache: graph miss (graph-uncacheable: directory-replaced module)\n"} {
		if !strings.Contains(lines, want) {
			t.Fatalf("verbose report %q does not contain %q", lines, want)
		}
	}
}

func TestCacheReportGraphLineOmittedWithoutGraphQuery(t *testing.T) {
	report := cacheReport{Modules: []moduleCacheReport{
		{Root: "/one", Graph: graphCacheOutcome{Reason: graphEntryMissing}},
		{Root: "/two", BypassReason: cacheReasonDisabledByOption},
	}}
	var graphLines []string
	for _, line := range report.verboseLines() {
		if strings.Contains(line, "graph") {
			graphLines = append(graphLines, line)
		}
	}
	if want := []string{"gsx cache: graph miss (graph-entry-missing)"}; strings.Join(graphLines, "|") != strings.Join(want, "|") {
		t.Fatalf("graph lines = %q, want %q", graphLines, want)
	}
}
