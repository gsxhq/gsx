package gen

import (
	"fmt"
	"sort"
	"time"
)

type cacheDecisionKind uint8

const (
	cacheDecisionHit cacheDecisionKind = iota + 1
	cacheDecisionMiss
	cacheDecisionUncacheable
)

type cacheReason string

const (
	cacheReasonEntryHit             cacheReason = "entry-hit"
	cacheReasonEntryMissing         cacheReason = "entry-missing"
	cacheReasonEntryCorrupt         cacheReason = "entry-corrupt"
	cacheReasonEntryReadFailed      cacheReason = "entry-read-failed"
	cacheReasonGraphQueryFailed     cacheReason = "graph-query-failed"
	cacheReasonProjectionFailed     cacheReason = "projection-failed"
	cacheReasonKeyFailed            cacheReason = "key-failed"
	cacheReasonGoContextUncacheable cacheReason = "go-context-uncacheable"
	cacheReasonDisabledByOption     cacheReason = "disabled-by-option"
	cacheReasonMissingModulePath    cacheReason = "missing-module-path"
	cacheReasonStoreWriteFailed     cacheReason = "store-write-failed"
)

type cacheDirReport struct {
	Dir      string
	Decision cacheDecisionKind
	Reason   cacheReason
	Detail   string
}

type cacheStageDurations struct {
	Prepare  time.Duration
	Classify time.Duration
	Restore  time.Duration
	Generate time.Duration
}

type moduleCacheReport struct {
	Root               string
	Enabled            bool
	BypassReason       cacheReason
	BypassDetail       string
	Dirs               []cacheDirReport
	Durations          cacheStageDurations
	SemanticGeneration bool
	StoreFailures      []cacheDirReport
}

type cacheReport struct {
	Modules []moduleCacheReport
}

func (report cacheReport) counts() (hits, misses, uncacheable int) {
	for _, module := range report.Modules {
		for _, dir := range module.Dirs {
			switch dir.Decision {
			case cacheDecisionHit:
				hits++
			case cacheDecisionMiss:
				misses++
			case cacheDecisionUncacheable:
				uncacheable++
			}
		}
	}
	return hits, misses, uncacheable
}

func (report cacheReport) semanticGeneration() bool {
	for _, module := range report.Modules {
		if module.SemanticGeneration {
			return true
		}
	}
	return false
}

func (report cacheReport) verboseLines() []string {
	hits, misses, uncacheable := report.counts()
	var durations cacheStageDurations
	var dirs, storeFailures []cacheDirReport
	for _, module := range report.Modules {
		durations.Prepare += module.Durations.Prepare
		durations.Classify += module.Durations.Classify
		durations.Restore += module.Durations.Restore
		durations.Generate += module.Durations.Generate
		for _, dir := range module.Dirs {
			if dir.Decision == cacheDecisionUncacheable {
				dirs = append(dirs, dir)
			}
		}
		storeFailures = append(storeFailures, module.StoreFailures...)
	}

	lines := []string{fmt.Sprintf(
		"gsx cache: %d hit, %d miss, %d uncacheable; prepare=%s classify=%s restore=%s generate=%s",
		hits, misses, uncacheable,
		durations.Prepare.Round(time.Millisecond),
		durations.Classify.Round(time.Millisecond),
		durations.Restore.Round(time.Millisecond),
		durations.Generate.Round(time.Millisecond),
	)}
	sort.Slice(dirs, func(i, j int) bool { return dirs[i].Dir < dirs[j].Dir })
	for _, dir := range dirs {
		line := fmt.Sprintf("gsx cache: %s: uncacheable (%s)", dir.Dir, dir.Reason)
		if dir.Detail != "" {
			line += ": " + dir.Detail
		}
		lines = append(lines, line)
	}
	sort.Slice(storeFailures, func(i, j int) bool { return storeFailures[i].Dir < storeFailures[j].Dir })
	for _, failure := range storeFailures {
		line := fmt.Sprintf("gsx cache: %s: cache store write failed", failure.Dir)
		if failure.Detail != "" {
			line += ": " + failure.Detail
		}
		lines = append(lines, line)
	}
	return lines
}
