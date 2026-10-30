package gen

import (
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/gsxhq/gsx/internal/attrclass"
	"github.com/gsxhq/gsx/internal/codegen"
	"github.com/gsxhq/gsx/internal/diag"
	"github.com/gsxhq/gsx/internal/sourceview"
)

type moduleGenerateConfig struct {
	filterPkgs   []string
	aliases      []codegen.FilterAlias
	renderers    []codegen.RendererAlias
	classifier   *attrclass.Classifier
	useCache     bool
	cssMin       func(string) (string, error)
	jsMin        func(string) (string, error)
	jsonMin      func(string) (string, error)
	cssMinify    bool
	jsMinify     bool
	verbatimTags bool
	classMerger  *codegen.ClassMergerRef
	lint         diag.LintPolicy
	// configPath is the gsx.toml these knobs were resolved from ("" when none
	// or when the config is programmatic); see moduleGenerateConfig.inheritedFrom.
	configPath string
}

type cachePreparation struct {
	root         string
	dirs         []string
	cacheDir     string
	goContext    *codegen.GoCommandContext
	manifest     *sourceview.Manifest
	projection   *sourceview.CacheProjection
	genOpts      codegen.Options
	keyConfig    cacheKeyConfig
	cacheReady   bool
	bypassReason cacheReason
	bypassDetail string
}

type cacheClassification struct {
	keys        map[string]string
	hits        map[string]cacheEntry
	misses      []string
	uncacheable []string
	dirReports  []cacheDirReport
}

type cacheStoreCandidate struct {
	dir   string
	key   string
	entry cacheEntry
}

// prepareCache captures the semantic source and Go-command inputs exactly once,
// then derives the shared cache projection when persistent caching is safe.
func prepareCache(g moduleGroup, config moduleGenerateConfig) (prep cachePreparation, report moduleCacheReport, err error) {
	prepareStart := time.Now()
	defer func() {
		report.Durations.Prepare = time.Since(prepareStart)
	}()

	prep.root = g.root
	prep.dirs = append([]string(nil), g.dirs...)
	report.Root = g.root
	cacheAdmitted := false
	switch {
	case !config.useCache:
		prep.bypassReason = cacheReasonDisabledByOption
		prep.bypassDetail = "cache disabled by option"
		report.BypassReason = prep.bypassReason
		report.BypassDetail = prep.bypassDetail
	case g.modPath == "":
		prep.bypassReason = cacheReasonMissingModulePath
		prep.bypassDetail = "module path is empty"
		report.BypassReason = prep.bypassReason
		report.BypassDetail = prep.bypassDetail
	default:
		var cacheEnabled bool
		prep.cacheDir, cacheEnabled = cacheDir()
		if cacheEnabled {
			report.Enabled = true
			cacheAdmitted = true
		} else {
			prep.bypassReason = cacheReasonDisabledByOption
			prep.bypassDetail = "cache unavailable"
			report.BypassReason = prep.bypassReason
			report.BypassDetail = prep.bypassDetail
		}
	}

	prep.goContext = codegen.CaptureGoCommandContext(g.root)
	prep.manifest, err = sourceview.Build(sourceview.BuildOptions{
		ModuleRoot: g.root,
		ModulePath: g.modPath,
	})
	if err != nil {
		return prep, report, fmt.Errorf("gen: build source manifest: %w", err)
	}
	prep.genOpts = config.codegenOptions()
	prep.genOpts.ModulePath = g.modPath
	prep.genOpts.GoCommandContext = prep.goContext
	prep.genOpts.SourceManifest = prep.manifest

	if !cacheAdmitted {
		return prep, report, nil
	}

	buildContext, contextErr := prep.goContext.CacheFingerprint()
	if contextErr != nil {
		if errors.Is(contextErr, codegen.ErrUncacheableGoContext) {
			prep.bypassReason = cacheReasonGoContextUncacheable
			prep.bypassDetail = contextErr.Error()
			report.BypassReason = prep.bypassReason
			report.BypassDetail = prep.bypassDetail
			return prep, report, nil
		}
		return prep, report, fmt.Errorf("gen: fingerprint Go command context: %w", contextErr)
	}

	prep.keyConfig = cacheKeyConfig{
		buildContext:          buildContext,
		codegenIdentity:       codegenIdentity(),
		additionalSourceRoots: []string{"github.com/gsxhq/gsx"},
		filterPackages:        config.filterPkgs,
		aliases:               config.aliases,
		renderers:             config.renderers,
		classifierFingerprint: config.classifier.Fingerprint(),
		cssMinify:             config.cssMinify,
		jsMinify:              config.jsMinify,
		verbatimTags:          config.verbatimTags,
		classMerger:           config.classMerger,
		lint:                  config.lint.Fingerprint(),
	}
	graphRoots := []string{"github.com/gsxhq/gsx"}
	graphRoots = append(graphRoots, configuredPackagePaths(config.filterPkgs, config.aliases, config.renderers, config.classMerger)...)
	graph, graphOutcome, graphErr := loadGraphCached(prep, dedupSorted(graphRoots))
	report.Graph = graphOutcome
	if graphErr != nil {
		prep.bypassReason = cacheReasonGraphQueryFailed
		prep.bypassDetail = graphErr.Error()
		report.BypassReason = prep.bypassReason
		report.BypassDetail = prep.bypassDetail
		return prep, report, nil
	}
	prep.projection, err = sourceview.NewCacheProjection(prep.manifest, graph)
	if err != nil {
		prep.bypassReason = cacheReasonProjectionFailed
		prep.bypassDetail = err.Error()
		report.BypassReason = prep.bypassReason
		report.BypassDetail = prep.bypassDetail
		return prep, report, nil
	}
	prep.cacheReady = true
	return prep, report, nil
}

// beforeListGraph and afterListGraph, when set by a test, run just before and
// just after go list on a graph cache miss.
var beforeListGraph, afterListGraph func()

// loadGraphCached returns the go list graph for prep's selected closure plus
// roots, reusing a stored graph whose witness matches the manifest. On a miss
// it runs go list and stores the result only when the witnessed files still
// match the manifest afterwards. Inputs the cache cannot key or witness fall
// through to a live go list. Errors are go list's (or the patterns'), as with
// loadGraphWithContext.
func loadGraphCached(prep cachePreparation, roots []string) (sourceview.Graph, graphCacheOutcome, error) {
	patterns, err := graphQueryPatterns(prep.manifest, prep.dirs, roots)
	if err != nil {
		return nil, graphCacheOutcome{Reason: graphUncacheable, Detail: err.Error()}, err
	}
	key, goModDir, err := graphCacheKey(prep, patterns)
	if err != nil {
		graph, listErr := listGraph(prep.goContext, prep.manifest, patterns)
		return graph, graphCacheOutcome{Reason: graphUncacheable, Detail: err.Error()}, listErr
	}
	graph, outcome := lookupGraph(prep.cacheDir, key, prep.manifest, goModDir)
	if outcome.Hit() {
		return graph, outcome, nil
	}
	if beforeListGraph != nil {
		beforeListGraph()
	}
	graph, err = listGraph(prep.goContext, prep.manifest, patterns)
	if err != nil {
		return nil, outcome, err
	}
	if afterListGraph != nil {
		afterListGraph()
	}
	// go list re-reads go.mod and go.sum: a graph listed after they changed
	// must not be stored under the key of their earlier bytes.
	if after, _, err := graphCacheKey(prep, patterns); err != nil {
		return graph, graphCacheOutcome{Reason: graphUncacheable, Detail: err.Error()}, nil
	} else if after != key {
		return graph, graphCacheOutcome{Reason: graphStoreSkipped, Detail: "graph key inputs changed while go list ran"}, nil
	}
	if stored := storeListedGraph(prep, key, goModDir, graph); stored.Reason != "" {
		outcome = stored
	}
	return graph, outcome, nil
}

// graphCacheKey returns the graph key for patterns and the directory that
// main-module paths are stored relative to: filepath.Dir of this run's GOMOD,
// the root go list spells them from.
func graphCacheKey(prep cachePreparation, patterns []string) (string, string, error) {
	if prep.cacheDir == "" {
		return "", "", errors.New("cache disabled")
	}
	goMod, err := prep.goContext.GoMod()
	if err != nil {
		return "", "", err
	}
	fingerprint, err := prep.goContext.CacheFingerprint()
	if err != nil {
		return "", "", err
	}
	key, err := graphKey(fingerprint, prep.manifest, patterns)
	if err != nil {
		return "", "", err
	}
	return key, filepath.Dir(goMod), nil
}

// storeListedGraph stores a freshly listed graph under the manifest's witness
// when a re-read of the witnessed files still matches it. It returns the zero
// outcome when the graph was stored.
func storeListedGraph(prep cachePreparation, key, goModDir string, graph sourceview.Graph) graphCacheOutcome {
	uncacheable := func(err error) graphCacheOutcome {
		return graphCacheOutcome{Reason: graphUncacheable, Detail: err.Error()}
	}
	input, err := witnessInputForGraph(graph, prep.manifest)
	if err != nil {
		return uncacheable(err)
	}
	digest, err := prep.manifest.GraphWitness(input)
	if err != nil {
		return uncacheable(err)
	}
	stored, err := relativizeGraph(graph, goModDir)
	if err != nil {
		return uncacheable(err)
	}
	disk, err := prep.manifest.DiskGraphWitness(input)
	if err != nil {
		return uncacheable(err)
	}
	if disk != digest {
		return graphCacheOutcome{Reason: graphStoreSkipped, Detail: "witnessed files changed while go list ran"}
	}
	if err := storeGraph(prep.cacheDir, key, graphVariant{Witness: input, Digest: digest, Graph: stored}); err != nil {
		return graphCacheOutcome{Reason: graphStoreFailed, Detail: err.Error()}
	}
	return graphCacheOutcome{}
}

// classifyCache makes independent per-directory cache decisions from the
// immutable preparation. It never restores or generates output.
func classifyCache(prep cachePreparation) cacheClassification {
	classification := cacheClassification{
		keys: make(map[string]string),
		hits: make(map[string]cacheEntry),
	}
	for _, dir := range prep.dirs {
		if !prep.cacheReady {
			classification.uncacheable = append(classification.uncacheable, dir)
			classification.dirReports = append(classification.dirReports, cacheDirReport{
				Dir:      dir,
				Decision: cacheDecisionUncacheable,
				Reason:   prep.bypassReason,
				Detail:   prep.bypassDetail,
			})
			continue
		}
		key, err := computeKey(dir, prep.projection, prep.keyConfig)
		if err != nil {
			classification.uncacheable = append(classification.uncacheable, dir)
			classification.dirReports = append(classification.dirReports, cacheDirReport{
				Dir:      dir,
				Decision: cacheDecisionUncacheable,
				Reason:   cacheReasonKeyFailed,
				Detail:   err.Error(),
			})
			continue
		}
		classification.keys[dir] = key
		cached, status, lookupErr := storeGet(prep.cacheDir, key)
		if status == cacheLookupHit {
			classification.hits[dir] = cached
			classification.dirReports = append(classification.dirReports, cacheDirReport{
				Dir:      dir,
				Decision: cacheDecisionHit,
				Reason:   cacheReasonEntryHit,
			})
			continue
		}
		var reason cacheReason
		var detail string
		switch status {
		case cacheLookupMissing:
			reason = cacheReasonEntryMissing
		case cacheLookupCorrupt:
			reason = cacheReasonEntryCorrupt
		case cacheLookupUnreadable:
			reason = cacheReasonEntryReadFailed
			detail = lookupErr.Error()
		default:
			panic(fmt.Sprintf("gen: unknown cache lookup status %d", status))
		}
		classification.misses = append(classification.misses, dir)
		classification.dirReports = append(classification.dirReports, cacheDirReport{
			Dir:      dir,
			Decision: cacheDecisionMiss,
			Reason:   reason,
			Detail:   detail,
		})
	}
	classification.misses = dedupSorted(classification.misses)
	classification.uncacheable = dedupSorted(classification.uncacheable)
	return classification
}

func (classification cacheClassification) generationDirs() []string {
	dirs := append([]string(nil), classification.misses...)
	dirs = append(dirs, classification.uncacheable...)
	return dedupSorted(dirs)
}

// commitCache establishes the final semantic boundary before consuming any
// classified hit or publishing generated output.
func commitCache(prep cachePreparation, classification cacheClassification, report *moduleCacheReport, result *Result) {
	if err := prep.goContext.ValidateCurrent(); err != nil {
		result.Errs = append(result.Errs, fmt.Errorf("gen: validate Go command context before cache commit: %w", err))
		return
	}

	restoreStart := time.Now()
	for _, dir := range prep.dirs {
		entry, ok := classification.hits[dir]
		if !ok {
			continue
		}
		written, upToDate, err := restore(dir, entry.files)
		if err != nil {
			result.Errs = append(result.Errs, err)
			report.Durations.Restore = time.Since(restoreStart)
			return
		}
		result.Diags = append(result.Diags, absWarnings(prep.root, entry.warnings)...)
		result.Written = append(result.Written, written...)
		result.UpToDate += upToDate
	}
	report.Durations.Restore = time.Since(restoreStart)

	generationDirs := classification.generationDirs()
	if len(generationDirs) == 0 {
		return
	}
	report.SemanticGeneration = true
	generateStart := time.Now()
	generated, err := codegen.GenerateDirs(prep.root, generationDirs, prep.genOpts, nil)
	report.Durations.Generate = time.Since(generateStart)
	if err != nil {
		result.Errs = append(result.Errs, err)
		return
	}
	candidates := writeGeneratedOutputs(prep.root, generationDirs, generated, classification, result)
	storeGeneratedOutputs(prep.cacheDir, candidates, report)
}

// writeGeneratedOutputs completes the entire generated-output write phase and
// returns the successful cacheable misses that become eligible for the later
// store phase. It deliberately has no cache-directory or store access.
func writeGeneratedOutputs(root string, generationDirs []string, generated map[string]codegen.DirResult, classification cacheClassification, result *Result) []cacheStoreCandidate {
	cacheableMisses := make(map[string]bool, len(classification.misses))
	for _, dir := range classification.misses {
		cacheableMisses[dir] = true
	}
	var candidates []cacheStoreCandidate
	for _, dir := range generationDirs {
		dirResult, ok := generated[dir]
		if !ok {
			continue
		}
		result.Diags = append(result.Diags, dirResult.Diags...)
		output := writeDirOutcome(dir, dirResult, result)
		if output == nil || !cacheableMisses[dir] {
			continue
		}
		key, ok := classification.keys[dir]
		if !ok {
			continue
		}
		warnings, ok := relWarnings(root, dirResult.Diags)
		if !ok {
			continue
		}
		candidates = append(candidates, cacheStoreCandidate{dir: dir, key: key, entry: cacheEntry{files: output, warnings: warnings}})
	}
	return candidates
}

func storeGeneratedOutputs(cacheDir string, candidates []cacheStoreCandidate, report *moduleCacheReport) {
	for _, candidate := range candidates {
		if err := storePut(cacheDir, candidate.key, candidate.entry); err != nil {
			report.StoreFailures = append(report.StoreFailures, cacheDirReport{
				Dir:    candidate.dir,
				Reason: cacheReasonStoreWriteFailed,
				Detail: err.Error(),
			})
		}
	}
}
