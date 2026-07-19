package gen

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/gsxhq/gsx/internal/attrclass"
	"github.com/gsxhq/gsx/internal/codegen"
	"github.com/gsxhq/gsx/internal/diag"
	"github.com/gsxhq/gsx/internal/sourceview"
)

func cachePipelineProjection(t *testing.T) (string, string, string, *sourceview.CacheProjection, cacheKeyConfig) {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/cache-pipeline\n\ngo 1.26.1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cacheableDir := filepath.Join(root, "cacheable")
	uncacheableDir := filepath.Join(root, "uncacheable")
	for _, dir := range []string{cacheableDir, uncacheableDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		name := filepath.Base(dir)
		source := fmt.Sprintf("package %s\n\ncomponent View() { <p/> }\n", name)
		if err := os.WriteFile(filepath.Join(dir, name+".gsx"), []byte(source), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	manifest, err := sourceview.Build(sourceview.BuildOptions{
		ModuleRoot: root,
		ModulePath: "example.com/cache-pipeline",
	})
	if err != nil {
		t.Fatal(err)
	}
	module := &pkgModule{Path: "example.com/cache-pipeline", Dir: root, Main: true}
	graph := sourceview.Graph{
		"example.com/cache-pipeline/cacheable": {
			ImportPath: "example.com/cache-pipeline/cacheable",
			Dir:        cacheableDir,
			Module:     module,
		},
		"example.com/cache-pipeline/uncacheable": {
			ImportPath: "example.com/cache-pipeline/uncacheable",
			Dir:        uncacheableDir,
			CgoFiles:   []string{"bridge.go"},
			Module:     module,
		},
	}
	projection, err := sourceview.NewCacheProjection(manifest, graph)
	if err != nil {
		t.Fatal(err)
	}
	return root, cacheableDir, uncacheableDir, projection, cacheKeyConfig{
		buildContext:          "context",
		codegenIdentity:       "generator",
		classifierFingerprint: "classifier",
	}
}

func TestCachePipelinePrepareNoCacheReusesSemanticInputsWithoutMetadata(t *testing.T) {
	root, cacheableDir, _, _, _ := cachePipelineProjection(t)
	compiler := filepath.Join(t.TempDir(), "compile")
	if err := os.WriteFile(compiler, []byte("compiler version one"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeCacheBoundaryGoCommand(t, compiler)
	marker := filepath.Join(t.TempDir(), "graph-query")
	t.Setenv("GSX_FAIL_GRAPH_MARKER", marker)

	prep, report, err := prepareCache(moduleGroup{
		root:    root,
		modPath: "example.com/cache-pipeline",
		dirs:    []string{cacheableDir},
	}, moduleGenerateConfig{
		classifier: attrclass.Builtin(),
		useCache:   false,
	})
	if err != nil {
		t.Fatal(err)
	}
	if prep.goContext == nil || prep.manifest == nil {
		t.Fatalf("no-cache preparation omitted semantic inputs: %+v", prep)
	}
	if prep.genOpts.GoCommandContext != prep.goContext || prep.genOpts.SourceManifest != prep.manifest {
		t.Fatal("generation options do not reuse the captured context and manifest")
	}
	if prep.cacheDir != "" || prep.cacheReady || prep.projection != nil || prep.keyConfig.buildContext != "" || prep.keyConfig.codegenIdentity != "" {
		t.Fatalf("no-cache preparation queried cache metadata: %+v", prep)
	}
	if report.Enabled || report.BypassReason != cacheReasonDisabledByOption {
		t.Fatalf("no-cache report = %+v", report)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("no-cache preparation ran graph query; marker stat error = %v", err)
	}
}

func TestCachePipelinePrepareManifestFailurePreservesCacheAdmission(t *testing.T) {
	root := filepath.Join(t.TempDir(), "missing-module-root")
	t.Setenv("GSXCACHE", t.TempDir())

	_, report, err := prepareCache(moduleGroup{
		root:    root,
		modPath: "example.com/missing",
	}, moduleGenerateConfig{
		classifier: attrclass.Builtin(),
		useCache:   true,
	})
	if err == nil || !strings.Contains(err.Error(), "build source manifest") {
		t.Fatalf("prepare error = %v, want source-manifest failure", err)
	}
	if !report.Enabled {
		t.Fatalf("manifest-failure report = %+v, want cache admission preserved", report)
	}
	if report.BypassReason != "" || report.BypassDetail != "" {
		t.Fatalf("admitted manifest-failure report has bypass state: %+v", report)
	}
}

func TestCachePipelineGenerateModuleReturnsReportOnPrepareFailure(t *testing.T) {
	root := filepath.Join(t.TempDir(), "missing-module-root")
	var result Result
	report := generateModule(moduleGroup{
		root:    root,
		modPath: "example.com/missing",
	}, moduleGenerateConfig{
		classifier: attrclass.Builtin(),
		useCache:   false,
	}, &result)

	if report.Root != root {
		t.Fatalf("early report root = %q, want %q", report.Root, root)
	}
	if len(result.Errs) != 1 || !strings.Contains(result.Errs[0].Error(), "build source manifest") {
		t.Fatalf("early result errors = %v, want source-manifest failure", result.Errs)
	}
	if len(report.Dirs) != 0 || report.SemanticGeneration {
		t.Fatalf("prepare failure report published later-phase state: %+v", report)
	}
}

func TestCachePipelineSharedGraphFailureMarksEveryDirUncacheable(t *testing.T) {
	dirs := []string{"/selected/z", "/selected/a"}
	classification := classifyCache(cachePreparation{
		dirs:         dirs,
		cacheReady:   false,
		bypassReason: cacheReasonGraphQueryFailed,
		bypassDetail: "go list failed",
	})

	if len(classification.hits) != 0 || len(classification.misses) != 0 {
		t.Fatalf("shared graph failure classification = %+v, want only uncacheable dirs", classification)
	}
	if want := []string{"/selected/a", "/selected/z"}; !reflect.DeepEqual(classification.uncacheable, want) {
		t.Fatalf("uncacheable = %v, want %v", classification.uncacheable, want)
	}
	if len(classification.dirReports) != len(dirs) {
		t.Fatalf("dir reports = %v, want one per selected dir", classification.dirReports)
	}
	for i, dirReport := range classification.dirReports {
		if dirReport.Dir != dirs[i] || dirReport.Decision != cacheDecisionUncacheable || dirReport.Reason != cacheReasonGraphQueryFailed {
			t.Fatalf("dir report %d = %+v, want graph failure for %s", i, dirReport, dirs[i])
		}
	}
}

func TestCachePipelineKeyFailurePreservesSiblingHit(t *testing.T) {
	_, cacheableDir, uncacheableDir, projection, keyConfig := cachePipelineProjection(t)
	cacheRoot := t.TempDir()
	key, err := computeKey(cacheableDir, projection, keyConfig)
	if err != nil {
		t.Fatal(err)
	}
	wantHit := pkgOutput{"cacheable.x.go": []byte("package cacheable\n")}
	if err := storePut(cacheRoot, key, wantHit); err != nil {
		t.Fatal(err)
	}

	classification := classifyCache(cachePreparation{
		dirs:       []string{uncacheableDir, cacheableDir},
		cacheDir:   cacheRoot,
		projection: projection,
		keyConfig:  keyConfig,
		cacheReady: true,
	})

	if got := classification.hits[cacheableDir]; !reflect.DeepEqual(got, wantHit) {
		t.Fatalf("cacheable sibling hit = %v, want %v", got, wantHit)
	}
	if _, ok := classification.keys[uncacheableDir]; ok {
		t.Fatalf("uncacheable dir received a cache key: %v", classification.keys)
	}
	if want := []string{uncacheableDir}; !reflect.DeepEqual(classification.uncacheable, want) {
		t.Fatalf("uncacheable = %v, want %v", classification.uncacheable, want)
	}
	if len(classification.misses) != 0 {
		t.Fatalf("misses = %v, want none", classification.misses)
	}
}

func TestCachePipelineGenerationDirsAreStableUnion(t *testing.T) {
	classification := cacheClassification{
		misses:      []string{"/c", "/a", "/c"},
		uncacheable: []string{"/b", "/a"},
	}
	if got, want := classification.generationDirs(), []string{"/a", "/b", "/c"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("generation dirs = %v, want stable union %v", got, want)
	}
}

func TestCachePipelineWriteStageCompletesBeforeStoreCandidates(t *testing.T) {
	cacheableDir, cacheableGsx := mkGsxDir(t, "cacheable.gsx", "package cacheable\n\ncomponent View() { <p/> }\n")
	poisonDir, poisonGsx := mkGsxDir(t, "poison.gsx", "package poison\n\ncomponent View() { <p/> }\n")
	generationDirs := []string{cacheableDir, poisonDir}
	generated := map[string]codegen.DirResult{
		cacheableDir: {
			Files: map[string][]byte{cacheableGsx: []byte("package cacheable\n")},
		},
		poisonDir: {
			Diags: []diag.Diagnostic{errDiag(poisonGsx, 3, 1, "broken component")},
		},
	}
	classification := cacheClassification{
		keys:        map[string]string{cacheableDir: "cacheable-key", poisonDir: "must-not-store"},
		misses:      []string{cacheableDir},
		uncacheable: []string{poisonDir},
	}

	var result Result
	candidates := writeGeneratedOutputs(generationDirs, generated, classification, &result)
	for _, target := range []string{
		filepath.Join(cacheableDir, "cacheable.x.go"),
		filepath.Join(poisonDir, "poison.x.go"),
	} {
		if _, err := os.Stat(target); err != nil {
			t.Fatalf("write stage returned before processing %s: %v", target, err)
		}
	}
	if len(candidates) != 1 {
		t.Fatalf("store candidates = %+v, want only successful cacheable miss", candidates)
	}
	if candidate := candidates[0]; candidate.dir != cacheableDir || candidate.key != "cacheable-key" || len(candidate.output) != 1 {
		t.Fatalf("store candidate = %+v", candidate)
	}
	if len(result.Diags) != 1 || result.Diags[0].Message != "broken component" {
		t.Fatalf("write stage diagnostics = %v", result.Diags)
	}
	cacheRoot := t.TempDir()
	report := moduleCacheReport{}
	storeGeneratedOutputs(cacheRoot, candidates, &report)
	if _, status, err := storeGet(cacheRoot, "cacheable-key"); err != nil || status != cacheLookupHit {
		t.Fatal("cacheable sibling was not stored after the complete write stage")
	}
	if _, status, err := storeGet(cacheRoot, "must-not-store"); err != nil || status != cacheLookupMissing {
		t.Fatal("poisoned uncacheable directory was stored")
	}
	if len(report.StoreFailures) != 0 {
		t.Fatalf("store failures = %v", report.StoreFailures)
	}
}

func TestCachePipelineCommitValidatesBeforeRestoringHits(t *testing.T) {
	root, cacheableDir, _, projection, keyConfig := cachePipelineProjection(t)
	cacheRoot := t.TempDir()
	key, err := computeKey(cacheableDir, projection, keyConfig)
	if err != nil {
		t.Fatal(err)
	}
	if err := storePut(cacheRoot, key, pkgOutput{"cacheable.x.go": []byte("package restored\n")}); err != nil {
		t.Fatal(err)
	}
	prep := cachePreparation{
		root:       root,
		dirs:       []string{cacheableDir},
		cacheDir:   cacheRoot,
		goContext:  codegen.CaptureGoCommandContext(root),
		projection: projection,
		keyConfig:  keyConfig,
		cacheReady: true,
	}
	classification := classifyCache(prep)
	target := filepath.Join(cacheableDir, "cacheable.x.go")
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("classify restored hit before commit; stat error = %v", err)
	}
	if err := os.Mkdir(filepath.Join(root, "vendor"), 0o755); err != nil {
		t.Fatal(err)
	}

	var result Result
	report := moduleCacheReport{Root: root}
	commitCache(prep, classification, &report, &result)
	if len(result.Errs) != 1 || !strings.Contains(result.Errs[0].Error(), "vendor directory state changed") {
		t.Fatalf("commit errors = %v, want failed context revalidation", result.Errs)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("failed commit validation restored hit; stat error = %v", err)
	}
	if report.SemanticGeneration {
		t.Fatal("failed commit validation started semantic generation")
	}
}

func TestCachePipelineCommitStoresOnlyCacheableMisses(t *testing.T) {
	repoRoot, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	goMod := "module example.com/cache-commit\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => " + repoRoot + "\n"
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte(goMod), 0o644); err != nil {
		t.Fatal(err)
	}
	cacheableDir := filepath.Join(root, "cacheable")
	uncacheableDir := filepath.Join(root, "uncacheable")
	for _, dir := range []string{cacheableDir, uncacheableDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		name := filepath.Base(dir)
		source := fmt.Sprintf("package %s\n\ncomponent View() { <p>%s</p> }\n", name, name)
		if err := os.WriteFile(filepath.Join(dir, name+".gsx"), []byte(source), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	manifest, err := sourceview.Build(sourceview.BuildOptions{ModuleRoot: root, ModulePath: "example.com/cache-commit"})
	if err != nil {
		t.Fatal(err)
	}
	context := codegen.CaptureGoCommandContext(root)
	cacheRoot := t.TempDir()
	prep := cachePreparation{
		root:      root,
		dirs:      []string{cacheableDir, uncacheableDir},
		cacheDir:  cacheRoot,
		goContext: context,
		manifest:  manifest,
		genOpts: codegen.Options{
			ModulePath:       "example.com/cache-commit",
			GoCommandContext: context,
			SourceManifest:   manifest,
			Classifier:       attrclass.Builtin(),
			CSSMinify:        true,
			JSMinify:         true,
		},
	}
	const cacheableKey = "aa-cacheable"
	const uncacheableKey = "bb-must-not-store"
	classification := cacheClassification{
		keys: map[string]string{
			cacheableDir:   cacheableKey,
			uncacheableDir: uncacheableKey,
		},
		misses:      []string{cacheableDir},
		uncacheable: []string{uncacheableDir},
	}

	var result Result
	report := moduleCacheReport{Root: root}
	commitCache(prep, classification, &report, &result)
	if len(result.Errs) != 0 {
		t.Fatalf("commit errors = %v", result.Errs)
	}
	if !report.SemanticGeneration {
		t.Fatal("commit did not record semantic generation")
	}
	if _, status, err := storeGet(cacheRoot, cacheableKey); err != nil || status != cacheLookupHit {
		t.Fatal("cacheable miss output was not stored")
	}
	if _, status, err := storeGet(cacheRoot, uncacheableKey); err != nil || status != cacheLookupMissing {
		t.Fatal("uncacheable generated output was stored")
	}
	for _, dir := range []string{cacheableDir, uncacheableDir} {
		target := filepath.Join(dir, filepath.Base(dir)+".x.go")
		if _, err := os.Stat(target); err != nil {
			t.Fatalf("generated output %s: %v", target, err)
		}
	}
}

func TestCacheCorruptEntryRegeneratesAndReplaces(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping module-resolution test in -short mode")
	}
	mod := newModule(t, "gsxcachecorrupt")
	pkgDir := filepath.Join(mod, "views")
	writeFile(t, pkgDir, "page.gsx", "package views\n\ncomponent Page() { <p>fresh</p> }\n")
	cacheRoot := t.TempDir()
	t.Setenv("GSXCACHE", cacheRoot)

	generate := func() (Result, cacheReport, error) {
		return generateCachedWithReport([]string{pkgDir}, nil, nil, nil, attrclass.Builtin(), true, nil, nil, nil, false, false, nil)
	}
	if result, _, err := generate(); err != nil || len(result.Errs) != 0 {
		t.Fatalf("seed generation = (%+v, %v)", result, err)
	}

	prep, _, err := prepareCache(moduleGroup{
		root:    mod,
		modPath: "gsxcachecorrupt",
		dirs:    []string{pkgDir},
	}, moduleGenerateConfig{classifier: attrclass.Builtin(), useCache: true})
	if err != nil || !prep.cacheReady {
		t.Fatalf("cache preparation = (%+v, %v)", prep, err)
	}
	key, err := computeKey(pkgDir, prep.projection, prep.keyConfig)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(entryPath(cacheRoot, key), []byte("truncated"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(pkgDir, "page.x.go")); err != nil {
		t.Fatal(err)
	}

	result, report, err := generate()
	if err != nil || len(result.Errs) != 0 {
		t.Fatalf("corrupt-entry generation = (%+v, %v)", result, err)
	}
	if !report.semanticGeneration() {
		t.Fatal("corrupt entry did not trigger semantic generation")
	}
	if got := report.Modules[0].Dirs; len(got) != 1 || got[0].Decision != cacheDecisionMiss || got[0].Reason != cacheReasonEntryCorrupt {
		t.Fatalf("corrupt-entry report = %+v, want one entry-corrupt miss", got)
	}
	generated, err := os.ReadFile(filepath.Join(pkgDir, "page.x.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(generated), "fresh") {
		t.Fatalf("regenerated output omitted source content:\n%s", generated)
	}
	if _, status, err := storeGet(cacheRoot, key); err != nil || status != cacheLookupHit {
		t.Fatalf("replacement lookup = (%v, %v), want hit, nil", status, err)
	}
}
