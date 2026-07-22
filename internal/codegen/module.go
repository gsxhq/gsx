package codegen

import (
	"bytes"
	"fmt"
	"go/token"
	"go/types"
	goversion "go/version"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"

	"golang.org/x/tools/go/packages"

	"github.com/gsxhq/gsx/internal/attrclass"
	"github.com/gsxhq/gsx/internal/diag"
	"github.com/gsxhq/gsx/internal/sourceview"
)

// DirOptions overrides Module-level options for a single package dir. The zero
// value means "inherit from Options".
//
// FilterPkgs, when non-nil, replaces Options.FilterPkgs for this dir's filter
// table. It must name only packages the Module already loaded — i.e. packages
// reachable from Options.FilterPkgs, Options.LoadPkgs, or the module's own
// "./..." — because the table is harvested from the loaded types with NO
// packages.Load. Naming an unloaded package is a hard error, never an empty
// table: a silently-empty table would make a "this filter must be rejected"
// test pass for the wrong reason.
type DirOptions struct {
	FilterPkgs  []string              // nil = inherit Options.FilterPkgs
	ClassMerger *ClassMergerRef       // nil = inherit Options.ClassMerger
	Classifier  *attrclass.Classifier // nil = inherit Options.Classifier
	// URLPresets names the url-attribute presets in effect for this dir (nil =
	// inherit Options.URLPresets). It is the string-identity companion to
	// Classifier: the Classifier carries the EXPANDED rules a preset contributes,
	// but the preset NAMES (e.g. "htmx") are retained separately here so a consumer
	// like the LSP can answer "is the htmx preset on?" without reverse-engineering
	// it from rule contents. See Module.urlPresetsFor.
	URLPresets []string
}

// Options configures a Module. ModuleRoot is the absolute module root (dir
// containing go.mod); ModulePath is its declared module path (from go.mod).
type Options struct {
	ModuleRoot string
	ModulePath string
	// GoCommandContext, when non-nil, supplies the immutable Go environment and
	// launcher snapshot used by normal-mode source selection. Pre-analysis
	// callers such as the persistent cache capture it once and use the same
	// context for their metadata queries, preventing split build universes.
	GoCommandContext *GoCommandContext
	// SourceManifest, when non-nil, is the immutable sourceview already consumed
	// by a pre-analysis cache metadata query. Normal codegen reuses that exact
	// manifest rather than independently walking the module a second time.
	SourceManifest *sourceview.Manifest
	// SourceOnly makes in-memory .gsx overrides the complete package source
	// universe. It is reserved for Bundle-backed virtual generation where no
	// host filesystem source may participate.
	SourceOnly bool
	// FilterPkgs is the module-wide filter set: it is both loaded into the
	// external importer AND harvested into the default filter table.
	FilterPkgs []string
	// LoadPkgs names extra packages to load into the external importer WITHOUT
	// giving them filter semantics. It is the union half of the union/per-dir
	// split: a caller that needs several dirs to see different filter tables
	// lists every filter package here (one load) and narrows each dir's table
	// via PerDir. A superset here is inert — it only makes more packages
	// importable — whereas a superset in a dir's table silently widens the
	// filter whitelist.
	LoadPkgs []string
	// PerDir maps a package dir to its option overrides. Keys are matched
	// against the dir strings passed to Generate/Package (cleaned), and are
	// also consulted for dirs reached transitively through imports, so an
	// imported sibling package resolves its own filter table. Unsupported in
	// Bundle mode (the Bundle carries exactly one prebuilt table).
	PerDir  map[string]DirOptions
	Aliases []FilterAlias
	// Renderers is the module-wide [renderers]/WithRenderer registration list:
	// each entry is harvested alongside FilterPkgs/Aliases (one packages.Load,
	// via harvestFilters) into the funcTables.renderers every dir's analyze
	// consults. Unlike FilterPkgs, Renderers has no PerDir override — a
	// registered renderer applies module-wide.
	Renderers  []RendererAlias
	Classifier *attrclass.Classifier
	// URLPresets names the module-wide url-attribute presets in effect (e.g.
	// "htmx"). It is the string-identity companion to Classifier: Classifier
	// carries the expanded rules; URLPresets retains the names so a consumer can
	// tell WHICH presets are on (see Module.urlPresetsFor / PackageResult.URLPresets).
	// A PerDir entry with non-nil URLPresets overrides this for that dir.
	URLPresets []string
	CSSMin     func(string) (string, error) // custom static-CSS minifier (nil = built-in when CSSMinify)
	JSMin      func(string) (string, error) // custom static-JS minifier (nil = built-in when JSMinify)
	// JSONMin minifies a JSON-shaped body (a data-island <script> and a
	// JSON-shaped js`…` attribute value). It follows the JS gate: callers set it
	// whenever JSMin's level is "full" (see gen's config.effectiveJSONMin), nil
	// otherwise. This field is consulted by cascadeJS and minifyJSSegmentsHoley
	// in jsmin to optimize JSON-valued attributes (htmx hx-vals/hx-headers/hx-vars).
	JSONMin   func(string) (string, error)
	CSSMinify bool // minify static <style> CSS
	JSMinify  bool // minify static <script> JS
	// Bundle, when non-nil, supplies the external importer and filter table
	// directly (a prebuilt Bundle) so the Module type-checks skeletons
	// with NO packages.Load / `go list` — the mode a WASM build uses. The Module
	// can additionally operate override-only when SourceOnly is set. Bundle mode
	// is GENERATION-ONLY: the bundle's *types.Package values live in a foreign
	// FileSet, so imported-object positions do not resolve against m.fset; use
	// Generate, not Package, in this mode.
	Bundle *Bundle
	// ClassMerger, when non-nil, names an exported package-level func of type
	// func([]string) string that codegen emits in place of gsx.DefaultClassMerge.
	// Codegen imports the package under the reserved alias _gsxcm and emits
	// _gsxcm.<FuncName> at every class merge site.
	ClassMerger *ClassMergerRef
}

// Module is a warm, in-process analysis graph for one module root. It is the
// single analysis core consumed by generate, watch, the LSP, fmt, and the
// playground.
//
// Concurrency contract (Phase 1): analysisMu serializes the three top-level
// analysis entry points — Package, Generate, and typesPackage — so that only
// one analysis runs on a given Module at a time. mu guards the overrides, ext,
// pkgTypes, and targetDeclTypes map fields and is acquired independently of
// analysisMu (it is
// also acquired inside externalImporter and typesPackageWith, which are called
// from within a held analysisMu). ResolveImportCandidates is a fourth top-level
// analysis entry point: its complete authoritative enumeration and optional
// source recheck are serialized by analysisMu too. The internal recursive path
// (typesPackageWith → analyze → moduleImporter.Import → typesPackageWith) does
// NOT acquire analysisMu — those functions run within a held analysisMu and
// re-acquiring would deadlock. True fine-grained concurrent analysis (multiple
// roots in parallel or partial invalidation) is deferred to Phase 2.
//
// Cache invalidation: SetOverride and ClearOverride compare against the frozen
// saved-source state beneath the buffer and return the exact sorted affected
// closure for effective byte or membership transitions. Package and Generate
// call applyDirty at the start of each run: it drops that
// reverse-reflexive-transitive closure from both type-package caches, then
// clears dirty. This means only the affected subgraph is re-type-checked;
// unchanged packages and the warm ext importer stay cached. A configured
// module-local renderer dir is the intentional exception: its result
// classification is module-wide, so its declaration/table caches and every
// retained package analysis are dropped while the ext importer stays warm.
// RefreshDiskSources is the explicit saved-source transition for watch events;
// RefreshDiskSourcesAndInvalidate is the atomic saved-source plus retained-fact
// transition used by concurrent callers such as the LSP. Invalidate is the
// public entry point for callers that only need to evict a directory without
// changing the source snapshot.
//
// FileSet: the Module uses ONE *token.FileSet (m.fset) for its whole lifetime,
// covering BOTH the external packages.Load AND every project analyze() call. So
// every type-object position — package A, sibling B, external dep — resolves
// unambiguously against the single fset, exactly like the Module's own
// packages.Load fset. This is what makes cross-package go-to-def (the expression
// path) resolve a sibling's obj.Pos() to the sibling's source rather than a
// random spot in the importing package.
//
// Growth bounding: because the fset is Module-lifetime, re-analyzing a project
// package each edit (applyDirty clears pkgTypes → re-parse into the same fset)
// accumulates fset entries (token.FileSet is append-only). maybeRebuildFset (called
// at the start of Package/Generate) bounds this: when project re-parse growth
// (fset.Base() - fsetBaseline) exceeds fsetRebuildBytes, rebuildFset replaces the
// fset AND drops ext+pkgTypes+targetDeclTypes+pkgResults TOGETHER, so nothing live holds positions
// into the discarded fset. The import graph, dirty set, and overrides survive
// (path/content-based). Do NOT rebuild the fset per edit, and never reset the fset
// while keeping ext, pkgTypes, targetDeclTypes, or pkgResults: that would orphan their positions.
type Module struct {
	opts                      Options
	buildEnv                  []string                            // immutable process environment used by the Module's authoritative Go build selection
	buildEnvErr               error                               // immutable Open-time normal-mode environment validation; surfaced only by semantic cold loads
	goContext                 *GoCommandContext                   // complete Open-time Go command provenance; validated around every cold load
	bundleProjectImportChecks map[string]bundleProjectImportCheck // Bundle local-Go transitive GSX guard, versioned by source membership epoch
	overrides                 map[string][]byte                   // abs .gsx path -> in-memory source
	ephemeral                 map[string][]byte                   // one-shot source overlay for AnalyzeEphemeral; non-nil only while it runs (under analysisMu)
	ext                       types.Importer                      // lazily built external importer (stdlib + third-party)
	extPkgs                   map[string]*types.Package           // the types behind ext, kept for subprocess-free filter-table harvests
	externalImportPaths       map[string]bool                     // exact path set published by ext; safe retained superset for later GSX import edits
	extErrs                   map[string][]packages.Error         // per-package load/type errors from the ext load (filter packages must not be silently partial)
	externalBackedges         map[string][]string                 // external path -> transitive main-module imports; rejected semantic boundary
	sourcePackages            map[string]projectSourcePackage     // abs dir -> authoritative active compiled Go files from the ext load
	sourcePackageDirs         map[string]string                   // exact module-local import path -> clean abs dir, built once with sourcePackages
	sourceGsxDirs             map[string]bool                     // authoritative module-owned dirs containing GSX source; public watch output filter
	savedSourceManifest       *sourceview.Manifest                // immutable explicitly-refreshed saved bytes/membership beneath overrides
	savedFileSnapshots        map[string]sourceview.FileSnapshot  // exact pre-manifest saved states captured when buffer authority begins
	sourceManifest            *sourceview.Manifest                // immutable facts published by the last successful cold source selection
	helperGoSourceManifest    *sourceview.Manifest                // latest authoritative immutable helper-name Go view; refreshed even when build selection stays warm
	directHelperGoViews       map[string]helperGoView             // abs dir -> immutable helper Go view keyed by its manifest identity
	sourceInventoryFacts      map[string]gsxSourceInventoryFact   // current per-GSX package/import facts, including unpublished overrides/disk refreshes
	sourceReloadReasons       map[string]sourceview.ReloadReason  // current path -> exact reason it differs incompatibly from sourceManifest
	sourceSnapshotEpoch       uint64                              // increments for every effective transition and first buffer-authority capture; guards coherent snapshot publication
	sourceManifestEpoch       uint64                              // increments when an override or saved-disk refresh changes package/import/path facts used by the cold manifest
	sourceInventoryReady      bool                                // distinguishes an authoritative empty selection from Bundle/uninitialized mode
	sourceInventoryDirty      bool                                // source selection or an unpublished import changed; rebuild before the next analysis
	goSourceReload            bool                                // an effective Go override transition requires the authoritative cold overlay to reload
	extLoads                  int                                 // count of external packages.Load calls (observability; test hook)
	funcTbl                   funcTables                          // lazily built filter-only fmt table (see cachedFuncTables)
	funcTblErr                error                               // error from the func-tables load (cached alongside funcTbl)
	funcTblDone               bool                                // true once the func tables have been loaded (success or error)
	rendererPkgs              map[string]*types.Package           // final renderer packages, with module-local GSX packages replaced by declaration skeleton types
	rendererLocal             map[string]bool                     // renderer package path -> module-local GSX ownership
	rendererPkgsErr           error                               // cached renderer package resolution error
	rendererPkgsDone          bool                                // true once renderer packages have been resolved (success or error)
	rendererTbl               rendererTable                       // unlocalized, alias-free completed renderer table
	rendererTblErr            error                               // cached renderer harvest/global-validation error
	rendererTblDone           bool                                // true once the completed renderer table has been built (success or error)
	rendererDirs              map[string]bool                     // configured module-owned renderer dirs; source kind is resolved lazily
	configuredSourceDirs      map[string]bool                     // local configured filter/alias/renderer/merger roots resolved from authoritative source
	filterLoads               int                                 // count of filter-table loads performed (observability; test hook)
	dirFuncTbls               map[string]funcTables               // per-dir func-tables memo, keyed by consuming package + canonical FilterPkgs key
	classMergersErr           error                               // cached result of validateConfiguredMergers
	classMergersDone          bool                                // true once every configured merger has been validated
	fset                      *token.FileSet                      // module-wide shared FileSet (see "FileSet" / "Growth" notes above)
	pkgTypes                  map[string]*types.Package           // abs dir -> shipping-universe package cache, including retained Go-only intermediaries
	targetDeclTypes           map[string]*types.Package           // abs dir -> exact-signature declarations; never aliases the shipping Props cache
	targetDeclProvenance      componentTargetProvenanceCache      // abs dir -> logical component key -> exact authored declarations
	configuredDeclTypes       map[string]*types.Package           // abs dir -> configured declaration-universe package cache
	pkgResults                map[string]*PackageResult           // abs dir -> cached full analysis result (Package path only)
	imports                   map[string][]string                 // dir -> authoritative module-local shipping dependencies (forward edges)
	importedBy                map[string]map[string]bool          // dep dir -> set of importer dirs (reverse edges)
	targetImports             map[string][]string                 // exact-target declaration graph forward edges
	targetImportedBy          map[string]map[string]bool          // exact-target declaration graph reverse edges
	sourceDeclImports         map[string][]string                 // configured declaration-source graph forward edges
	sourceDeclImportedBy      map[string]map[string]bool          // configured declaration-source graph reverse edges
	dirty                     map[string]bool                     // dirs with a pending content change (consumed by applyDirty)
	fsetBaseline              int                                 // m.fset.Base() captured after the last packages.Load (growth measured since here)
	fsetRebuildBytes          int                                 // rebuild fset when fset.Base()-fsetBaseline exceeds this; 0 disables
	rebuildCount              int                                 // count of fset rebuilds performed (observability; exposed via rebuilds())
	sourceIndexBuildCount     int                                 // count of retained semantic index builds (observability; test hook)
	gcImporter                types.Importer                      // lazily built export-data importer for ResolveImportCandidates (see exportDataImporter); never used on the Package() hot path
	mu                        sync.Mutex                          // guards overrides, ext, both type caches/results/facts, both import graphs, dirty, and gcImporter publication
	analysisMu                sync.Mutex                          // serializes Package/Generate/typesPackage (see concurrency contract)
}

// defaultFsetRebuildBytes bounds the module-lifetime FileSet's project re-parse
// growth: when fset.Base() climbs this many bytes past the post-load baseline, the
// Module rebuilds fset+ext+pkgTypes+pkgResults. 256 MiB is generous enough that a rebuild is
// rare (tens of full re-analyses of a large package) yet caps leaked token.File
// memory. Internal perf knob (not gsx.toml / computeKey); overridable via
// GSX_FSET_REBUILD_BYTES (0 disables; like GSXCACHE).
const defaultFsetRebuildBytes = 256 << 20

// fsetRebuildBytesFromEnv returns the GSX_FSET_REBUILD_BYTES override if set to a
// valid non-negative integer (0 disables rebuilding), else defaultFsetRebuildBytes.
func fsetRebuildBytesFromEnv() int {
	if v, ok := os.LookupEnv("GSX_FSET_REBUILD_BYTES"); ok {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			return n
		}
	}
	return defaultFsetRebuildBytes
}

// Open constructs a Module. It captures the normal-mode Go environment but
// does not load packages; semantic package analysis remains lazy.
func Open(opts Options) (*Module, error) {
	cls := opts.Classifier
	if cls == nil {
		cls = attrclass.Builtin()
		opts.Classifier = cls
	}
	if opts.Bundle != nil {
		switch {
		case opts.Bundle.imp == nil:
			return nil, fmt.Errorf("codegen: Options.Bundle has no importer")
		case opts.Bundle.sizes == nil:
			return nil, fmt.Errorf("codegen: Options.Bundle has no target type sizes")
		case !goversion.IsValid(opts.Bundle.goVersion):
			return nil, fmt.Errorf("codegen: Options.Bundle has invalid Go language version %q", opts.Bundle.goVersion)
		}
	}
	if opts.SourceOnly && opts.Bundle == nil {
		return nil, fmt.Errorf("codegen: Options.SourceOnly requires Options.Bundle")
	}
	if opts.SourceManifest != nil {
		if opts.Bundle != nil {
			return nil, fmt.Errorf("codegen: Options.SourceManifest is unavailable in Bundle mode")
		}
		if filepath.Clean(opts.SourceManifest.ModuleRoot()) != filepath.Clean(opts.ModuleRoot) || opts.SourceManifest.ModulePath() != opts.ModulePath {
			return nil, fmt.Errorf("codegen: source manifest belongs to module %q at %q, not %q at %q",
				opts.SourceManifest.ModulePath(), opts.SourceManifest.ModuleRoot(), opts.ModulePath, opts.ModuleRoot)
		}
	}
	// A Bundle carries exactly one prebuilt importer and one prebuilt set of
	// func tables (filters + renderers), so per-dir narrowing has nothing to
	// narrow. Silently ignoring PerDir here would hand a dir the Bundle's whole
	// table — the union leak this design exists to prevent — so reject the
	// combination outright.
	if opts.Bundle != nil && (len(opts.PerDir) > 0 || len(opts.LoadPkgs) > 0) {
		return nil, fmt.Errorf("codegen: Options.Bundle is incompatible with PerDir/LoadPkgs (a Bundle carries one prebuilt set of func tables — filters and renderers)")
	}
	buildEnv := append([]string(nil), os.Environ()...)
	var buildEnvErr error
	var goContext *GoCommandContext
	if opts.Bundle == nil {
		goContext = opts.GoCommandContext
		if goContext == nil {
			goContext = CaptureGoCommandContext(opts.ModuleRoot)
		}
		if filepath.Clean(opts.ModuleRoot) != goContext.moduleRoot {
			buildEnvErr = fmt.Errorf("codegen: Go command context belongs to module root %q, not %q", goContext.moduleRoot, filepath.Clean(opts.ModuleRoot))
		} else {
			buildEnv = append([]string(nil), goContext.buildEnv...)
			buildEnvErr = goContext.buildEnvErr
		}
	}
	return &Module{
		opts:                      opts,
		buildEnv:                  buildEnv,
		buildEnvErr:               buildEnvErr,
		goContext:                 goContext,
		bundleProjectImportChecks: map[string]bundleProjectImportCheck{},
		overrides:                 map[string][]byte{},
		fset:                      token.NewFileSet(),
		dirFuncTbls:               map[string]funcTables{},
		rendererDirs:              map[string]bool{},
		configuredSourceDirs:      map[string]bool{},
		targetDeclTypes:           map[string]*types.Package{},
		targetDeclProvenance:      componentTargetProvenanceCache{},
		configuredDeclTypes:       map[string]*types.Package{},
		sourcePackages:            map[string]projectSourcePackage{},
		sourcePackageDirs:         map[string]string{},
		sourceGsxDirs:             map[string]bool{},
		savedSourceManifest:       opts.SourceManifest,
		savedFileSnapshots:        map[string]sourceview.FileSnapshot{},
		sourceInventoryFacts:      map[string]gsxSourceInventoryFact{},
		sourceReloadReasons:       map[string]sourceview.ReloadReason{},
		directHelperGoViews:       map[string]helperGoView{},
		externalImportPaths:       map[string]bool{},
		externalBackedges:         map[string][]string{},
		pkgResults:                map[string]*PackageResult{},
		imports:                   map[string][]string{},
		importedBy:                map[string]map[string]bool{},
		targetImports:             map[string][]string{},
		targetImportedBy:          map[string]map[string]bool{},
		sourceDeclImports:         map[string][]string{},
		sourceDeclImportedBy:      map[string]map[string]bool{},
		dirty:                     map[string]bool{},
		fsetRebuildBytes:          fsetRebuildBytesFromEnv(),
	}, nil
}

// SetOverride records one in-memory .gsx or .go source, shadowing the immutable
// saved state captured when buffer authority begins. It returns the exact sorted
// invalidation scope for an effective source transition; identical effective
// bytes return nil. Invalidation itself remains lazy until Package/Generate.
func (m *Module) SetOverride(absPath string, src []byte) []string {
	absPath = filepath.Clean(absPath)
	if !strings.HasSuffix(absPath, ".gsx") && !strings.HasSuffix(absPath, ".go") {
		return nil
	}
	owned, ownershipErr := sourceview.OwnsPath(m.opts.ModuleRoot, absPath)
	if ownershipErr != nil || !owned {
		return nil
	}
	src = bytes.Clone(src)
	for {
		m.mu.Lock()
		override, hadOverride := m.overrides[absPath]
		override = bytes.Clone(override)
		saved := m.savedSourceManifest
		savedSnapshot, savedSnapshotKnown := m.savedFileSnapshots[absPath]
		snapshotEpoch := m.sourceSnapshotEpoch
		m.mu.Unlock()

		base := sourceview.PresentFile(override)
		captureSaved := false
		if !hadOverride {
			base = savedSnapshot
			if !savedSnapshotKnown && saved != nil {
				base, savedSnapshotKnown = saved.FileSnapshot(absPath)
			}
			if !savedSnapshotKnown {
				if m.opts.SourceOnly {
					base = sourceview.AbsentFile()
				} else {
					base = sourceview.ReadFileSnapshot(absPath)
				}
				captureSaved = true
			}
		}
		changed := !fileSnapshotEquals(base, sourceview.PresentFile(src))
		tracksInventoryFact := strings.HasSuffix(absPath, ".gsx") && pathWithin(m.opts.ModuleRoot, absPath)
		baseSource, basePresent := base.Source()
		oldInventoryFact, _ := inspectGsxSourceInventory(absPath, baseSource, tracksInventoryFact && basePresent)
		newInventoryFact, _ := inspectGsxSourceInventory(absPath, src, tracksInventoryFact)

		m.mu.Lock()
		current, hasCurrent := m.overrides[absPath]
		if hasCurrent != hadOverride || hasCurrent && !bytes.Equal(current, override) ||
			m.savedSourceManifest != saved || m.sourceSnapshotEpoch != snapshotEpoch {
			m.mu.Unlock()
			continue
		}
		if captureSaved {
			m.savedFileSnapshots[absPath] = cloneSourceFileSnapshot(base)
		}
		m.overrides[absPath] = src
		var affected []string
		if changed {
			affected = m.affectedLocked([]string{filepath.Dir(absPath)}).sorted()
			if m.dirty == nil {
				m.dirty = map[string]bool{}
			}
			m.dirty[filepath.Dir(absPath)] = true
		}
		// Even when the bytes are equal, first buffer authority freezes the
		// saved state beneath it. Reject a cold load that began from live disk
		// before this capture; its result cannot be published over the now-frozen
		// source layers.
		if changed || !hadOverride {
			m.sourceSnapshotEpoch++
		}
		if tracksInventoryFact && oldInventoryFact != newInventoryFact {
			m.sourceManifestEpoch++
		}
		if tracksInventoryFact {
			if m.sourceInventoryFacts == nil {
				m.sourceInventoryFacts = map[string]gsxSourceInventoryFact{}
			}
			m.sourceInventoryFacts[absPath] = newInventoryFact
			m.updateSourceReloadReasonLocked(absPath, newInventoryFact, true)
		}
		if strings.HasSuffix(absPath, ".go") && changed {
			m.sourceManifestEpoch++
			m.goSourceReload = true
			m.sourceInventoryDirty = true
		}
		m.mu.Unlock()
		return affected
	}
}

// ClearOverride always ends buffer authority and exposes its exact saved state:
// present, absent, or unreadable. It returns the pre-clear affected closure even
// when unreadability also returns an operational error; callers must evict that
// stale scope rather than treating the error as a rollback signal.
func (m *Module) ClearOverride(absPath string) ([]string, error) {
	absPath = filepath.Clean(absPath)
	for {
		m.mu.Lock()
		override, found := m.overrides[absPath]
		override = bytes.Clone(override)
		saved := m.savedSourceManifest
		savedSnapshot, savedSnapshotKnown := m.savedFileSnapshots[absPath]
		snapshotEpoch := m.sourceSnapshotEpoch
		m.mu.Unlock()
		if !found {
			return nil, nil
		}

		base := savedSnapshot
		captureSaved := false
		if !savedSnapshotKnown && saved != nil {
			base, savedSnapshotKnown = saved.FileSnapshot(absPath)
		}
		if !savedSnapshotKnown {
			if m.opts.SourceOnly {
				base = sourceview.AbsentFile()
			} else {
				base = sourceview.ReadFileSnapshot(absPath)
			}
			captureSaved = true
		}
		changed := !fileSnapshotEquals(sourceview.PresentFile(override), base)
		tracksInventoryFact := strings.HasSuffix(absPath, ".gsx") && pathWithin(m.opts.ModuleRoot, absPath)
		oldFact, _ := inspectGsxSourceInventory(absPath, override, tracksInventoryFact)
		baseSource, basePresent := base.Source()
		newFact, _ := inspectGsxSourceInventory(absPath, baseSource, tracksInventoryFact && basePresent)

		m.mu.Lock()
		current, stillFound := m.overrides[absPath]
		if !stillFound {
			m.mu.Unlock()
			return nil, nil
		}
		if !bytes.Equal(current, override) || m.savedSourceManifest != saved || m.sourceSnapshotEpoch != snapshotEpoch {
			m.mu.Unlock()
			continue
		}
		if captureSaved {
			m.savedFileSnapshots[absPath] = cloneSourceFileSnapshot(base)
		}
		var affected []string
		if changed {
			affected = m.affectedLocked([]string{filepath.Dir(absPath)}).sorted()
		}
		delete(m.overrides, absPath)
		// Ending buffer authority is itself a snapshot-authority transition: the
		// saved layer becomes authoritative again, so a cold load that began under
		// the frozen-override view can no longer be published coherently. Advance
		// the snapshot epoch on every clear that removed an override, mirroring
		// SetOverride's first-authority bump, even when the effective bytes are
		// unchanged. Invalidation (the dirty mark and the returned affected scope)
		// stays gated on an effective byte change.
		m.sourceSnapshotEpoch++
		if changed {
			if m.dirty == nil {
				m.dirty = map[string]bool{}
			}
			m.dirty[filepath.Dir(absPath)] = true
		}
		if tracksInventoryFact {
			if oldFact != newFact {
				m.sourceManifestEpoch++
			}
			if basePresent {
				m.sourceInventoryFacts[absPath] = newFact
			} else {
				delete(m.sourceInventoryFacts, absPath)
			}
			m.updateSourceReloadReasonLocked(absPath, newFact, basePresent)
		}
		if strings.HasSuffix(absPath, ".go") && changed {
			m.sourceManifestEpoch++
			m.goSourceReload = true
			m.sourceInventoryDirty = true
		}
		m.mu.Unlock()
		if base.State() == sourceview.FileUnreadable {
			return affected, fmt.Errorf("codegen: clear override %s: read saved source: %w", absPath, base.Err())
		}
		return affected, nil
	}
}

func fileSnapshotEquals(left, right sourceview.FileSnapshot) bool {
	if left.State() != right.State() {
		return false
	}
	leftSource, leftPresent := left.Source()
	rightSource, rightPresent := right.Source()
	if leftPresent || rightPresent {
		return leftPresent == rightPresent && bytes.Equal(leftSource, rightSource)
	}
	return true
}

func cloneSourceFileSnapshot(snapshot sourceview.FileSnapshot) sourceview.FileSnapshot {
	switch snapshot.State() {
	case sourceview.FilePresent:
		source, _ := snapshot.Source()
		return sourceview.PresentFile(source)
	case sourceview.FileUnreadable:
		return sourceview.UnreadableFile(snapshot.Err())
	default:
		return sourceview.AbsentFile()
	}
}

func (m *Module) updateSourceReloadReasonLocked(path string, current gsxSourceInventoryFact, currentPresent bool) {
	if m.sourceReloadReasons == nil {
		m.sourceReloadReasons = map[string]sourceview.ReloadReason{}
	}
	if !m.sourceInventoryReady || m.sourceManifest == nil {
		delete(m.sourceReloadReasons, path)
		m.sourceInventoryDirty = m.goSourceReload
		return
	}
	published, publishedPresent := m.sourceManifest.Fact(path)
	if !publishedPresent {
		published = sourceview.Inspect(path, nil, false)
	}
	if !currentPresent {
		current = sourceview.Inspect(path, nil, false)
	}
	reason := sourceview.ReloadReasonFor(published, current, m.externalImportPaths)
	if reason == sourceview.ReloadNone {
		delete(m.sourceReloadReasons, path)
	} else {
		m.sourceReloadReasons[path] = reason
	}
	m.sourceInventoryDirty = m.goSourceReload || len(m.sourceReloadReasons) != 0
}

// currentSource returns the bytes currently backing absPath: the override, the
// frozen saved snapshot beneath it, or an initial saved observation when no
// manifest exists yet. It takes m.mu only briefly and performs an initial disk
// observation outside the lock.
func (m *Module) currentSource(absPath string) ([]byte, bool) {
	m.mu.Lock()
	if e, ok := m.ephemeral[absPath]; ok {
		e = bytes.Clone(e)
		m.mu.Unlock()
		return e, true
	}
	ov, ok := m.overrides[absPath]
	ov = bytes.Clone(ov)
	savedSnapshot, savedSnapshotKnown := m.savedFileSnapshots[absPath]
	saved := m.savedSourceManifest
	m.mu.Unlock()
	if ok {
		return ov, true
	}
	if savedSnapshotKnown {
		return savedSnapshot.Source()
	}
	if m.opts.SourceOnly {
		return nil, false
	}
	if saved != nil {
		if snapshot, known := saved.FileSnapshot(absPath); known {
			return snapshot.Source()
		}
	}
	return sourceview.ReadFileSnapshot(absPath).Source()
}

// source returns the effective bytes for absPath from the override/saved layers.
func (m *Module) source(absPath string) ([]byte, bool) {
	return m.currentSource(absPath)
}

// dirtyDirs returns the sorted pending-dirty dirs (test hook; does not clear).
func (m *Module) dirtyDirs() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, 0, len(m.dirty))
	for d := range m.dirty {
		out = append(out, d)
	}
	sort.Strings(out)
	return out
}

// externalImporter lazily loads dependency types and the authoritative project
// source inventory once. Semantic importers never retain its module-local type
// packages: they re-check every local directory from retained source in their
// own declaration universe and use this importer only outside the module.
func (m *Module) externalImporter() (types.Importer, error) {
	if m.opts.Bundle != nil {
		// Bundle mode: the importer is prebuilt; no packages.Load. Returned
		// directly (not cached into m.ext) so rebuildFset's reset is harmless.
		return m.opts.Bundle.importer(), nil
	}
	m.mu.Lock()
	if m.ext != nil {
		ext := m.ext
		m.mu.Unlock()
		return ext, nil
	}
	m.mu.Unlock()
	if m.buildEnvErr != nil {
		return nil, m.buildEnvErr
	}
	buildEnv := m.buildEnv
	for {
		m.mu.Lock()
		if m.ext != nil {
			ext := m.ext
			m.mu.Unlock()
			return ext, nil
		}
		epoch := m.sourceManifestEpoch
		snapshotEpoch := m.sourceSnapshotEpoch
		fset := m.fset
		m.mu.Unlock()
		savedManifest, manifest, err := m.buildSourceInventorySnapshots()
		if err != nil {
			return nil, err
		}
		packagesOverlay, err := manifest.PackagesOverlay()
		if err != nil {
			return nil, fmt.Errorf("codegen: project source overlay: %w", err)
		}
		// Use the Module-wide shared FileSet for packages.Load so that every imported
		// dependency's type-object positions live in the SAME fset as the project
		// packages analyze() type-checks. One fset for the whole Module means an
		// object from any package — project A, sibling B, or external dep — resolves
		// unambiguously via m.fset.Position(obj.Pos()).
		cfg := &packages.Config{
			Mode:    packages.NeedName | packages.NeedCompiledGoFiles | packages.NeedSyntax | packages.NeedTypes | packages.NeedTypesSizes | packages.NeedImports | packages.NeedDeps | packages.NeedModule,
			Fset:    fset,
			Dir:     m.opts.ModuleRoot,
			Env:     buildEnv,
			Overlay: packagesOverlay,
		}
		// Always load the gsx runtime ("github.com/gsxhq/gsx") so that skeleton
		// type-checking can resolve gsx.Node / gsx.Attrs / etc. The skeleton file
		// every buildSkeleton emits always begins with
		//   import _gsxrt "github.com/gsxhq/gsx"
		// so the importer must carry that package. This mirrors newCachedResolver
		// (resolver.go) which lists "github.com/gsxhq/gsx" first for the same reason.
		loadPaths := append([]string{"github.com/gsxhq/gsx", stdImportPath}, m.opts.FilterPkgs...)
		loadPaths = append(loadPaths, m.opts.LoadPkgs...)
		// Explicit WithFilter aliases name packages that need not appear anywhere
		// else. They must be in the load set for configured filter resolution to classify their
		// target func's signature without a second packages.Load.
		for _, a := range m.opts.Aliases {
			loadPaths = append(loadPaths, a.PkgPath)
		}
		// [renderers]/WithRenderer registrations name packages the same way an
		// explicit alias does: they must be in this ONE load set so
		// rendererPackagesFromExt can classify their target func's signature without
		// a second packages.Load.
		for _, r := range finalRendererAliases(m.opts.Renderers) {
			loadPaths = append(loadPaths, r.PkgPath)
		}
		if m.opts.ClassMerger != nil {
			loadPaths = append(loadPaths, m.opts.ClassMerger.PkgPath)
		}
		for _, options := range m.opts.PerDir {
			loadPaths = append(loadPaths, options.FilterPkgs...)
			if options.ClassMerger != nil {
				loadPaths = append(loadPaths, options.ClassMerger.PkgPath)
			}
		}
		loadPaths = append(loadPaths, manifest.LoadRoots()...)
		loadPaths = append(loadPaths, "./...")
		seenLoadPath := make(map[string]bool, len(loadPaths))
		uniqueLoadPaths := loadPaths[:0]
		for _, path := range loadPaths {
			if path != "" && !seenLoadPath[path] {
				seenLoadPath[path] = true
				uniqueLoadPaths = append(uniqueLoadPaths, path)
			}
		}
		loadPaths = uniqueLoadPaths
		if err := m.validateGoCommandContext(); err != nil {
			return nil, err
		}
		pkgs, loadErr := packages.Load(cfg, loadPaths...)
		contextErr := m.validateGoCommandContext()
		m.mu.Lock()
		m.extLoads++
		staleManifest := m.sourceManifestEpoch != epoch || m.sourceSnapshotEpoch != snapshotEpoch || m.fset != fset
		m.mu.Unlock()
		if contextErr != nil {
			return nil, contextErr
		}
		if staleManifest {
			m.rebuildFset()
			continue
		}
		if loadErr != nil {
			return nil, loadErr
		}
		allTypes := map[string]*types.Package{}
		errs := map[string][]packages.Error{}
		packages.Visit(pkgs, nil, func(p *packages.Package) {
			if p.Types != nil {
				allTypes[p.PkgPath] = p.Types
			}
			if len(p.Errors) > 0 {
				errs[p.PkgPath] = p.Errors
			}
		})
		externalImportPaths := make(map[string]bool, len(allTypes))
		for importPath := range allTypes {
			externalImportPaths[importPath] = true
		}
		sentinelFiles := make(map[string]bool)
		for _, path := range manifest.SentinelFiles() {
			sentinelFiles[path] = true
		}
		sourcePackages := projectSourcePackages(pkgs, manifest.ModuleRoot(), manifest.PhysicalRoot(), m.opts.ModulePath, sentinelFiles)
		sourcePackageDirs := make(map[string]string, len(sourcePackages))
		for dir, sourcePackage := range sourcePackages {
			sourcePackageDirs[sourcePackage.pkgPath] = dir
		}
		externalBackedges := externalBackedgePackages(pkgs, sourcePackageDirs)
		// Cold main-module packages are rebuilt from authoritative source. External
		// packages whose dependency graph re-enters the main module cross the
		// explicit one-way boundary and are rejected rather than published or
		// reconstructed in a phase-local universe.
		mp := make(map[string]*types.Package, len(allTypes))
		for path, pkg := range allTypes {
			if _, local := sourcePackageDirs[path]; local {
				continue
			}
			if _, boundary := externalBackedges[path]; boundary {
				continue
			}
			mp[path] = pkg
		}
		for path := range sourcePackageDirs {
			delete(errs, path)
		}
		for path := range externalBackedges {
			delete(errs, path)
		}
		ext := externalBackedgeImporter{packages: mapImporter(mp), backedges: externalBackedges}
		m.mu.Lock()
		if m.sourceManifestEpoch != epoch || m.sourceSnapshotEpoch != snapshotEpoch || m.fset != fset {
			m.mu.Unlock()
			m.rebuildFset()
			continue
		}
		m.ext = ext
		m.extPkgs = mp
		m.externalImportPaths = externalImportPaths
		m.extErrs = errs
		m.externalBackedges = externalBackedges
		m.sourcePackages = sourcePackages
		m.sourcePackageDirs = sourcePackageDirs
		m.savedSourceManifest = savedManifest
		m.sourceManifest = manifest
		m.helperGoSourceManifest = manifest
		m.sourceGsxDirs = manifest.GSXDirs()
		m.sourceInventoryFacts = manifest.Facts()
		m.sourceReloadReasons = map[string]sourceview.ReloadReason{}
		m.sourceInventoryReady = true
		m.sourceInventoryDirty = false
		m.goSourceReload = false
		m.fsetBaseline = fset.Base()
		m.mu.Unlock()
		// Return the local, not m.ext: a concurrent rebuildFset (which nils m.ext
		// under m.mu) could otherwise be interleaved between the Unlock above and
		// an unguarded re-read of the field, racing with that write. ext is a
		// value we hold outside the map, so reading it needs no lock.
		return ext, nil
	}
}

// externalLoads returns the number of external packages.Load calls performed
// (test hook). Together with filterTableLoads it guards the warm-regen perf
// invariant: a warm regeneration must trigger ZERO go-list reloads.
func (m *Module) externalLoads() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.extLoads
}

// cachedFuncTables memoizes the filter table for buildPackageSkeletons' fmt
// fast path. It deliberately excludes renderers: declaration resolution needs
// the full external importer and local GSX package graph, while this path exists
// specifically to avoid that load. The table is the harvest of a packages.Load
// over only the filter packages — an external go-list + type-check that costs
// ~150ms — and it depends ONLY on inputs that are immutable for a Module:
// opts.ModuleRoot, opts.FilterPkgs, and opts.Aliases. So it is
// loaded once and reused across every analyze() call, instead of reloading on each
// warm regen (the pre-cache behaviour, which made every --watch cycle pay the full
// packages.Load and turned ~10ms warm regens into ~150ms ones).
//
// Lifetime/invalidation: cleared by rebuildFset (alongside ext), and a filter
// package is Go source — any .go/go.mod change drives the watch loop
// through reopen(), which builds fresh Modules, so an edit is naturally picked
// up. Called only from analyze, which runs under analysisMu; the m.mu
// double-check mirrors externalImporter.
func (m *Module) cachedFuncTables() (funcTables, error) {
	if m.opts.Bundle != nil {
		return m.opts.Bundle.tables(), nil
	}
	m.mu.Lock()
	if m.funcTblDone {
		defer m.mu.Unlock()
		return m.funcTbl, m.funcTblErr
	}
	m.mu.Unlock()
	filters, _, err := loadFilterTableMulti(m.opts.ModuleRoot, dedupFilterPkgs(m.opts.FilterPkgs), m.opts.Aliases, nil)
	tbl := funcTables{filters: filters, renderers: rendererTable{}}
	m.mu.Lock()
	m.funcTbl, m.funcTblErr, m.funcTblDone = tbl, err, true
	m.filterLoads++
	m.mu.Unlock()
	return tbl, err
}

// filterTableLoads returns the number of filter-table loads performed (test hook).
func (m *Module) filterTableLoads() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.filterLoads
}

// validateConfiguredMergers validates the module-wide merger and every PerDir
// override from authoritative configured declaration packages.
func (m *Module) validateConfiguredMergers() error {
	m.mu.Lock()
	if m.classMergersDone {
		defer m.mu.Unlock()
		return m.classMergersErr
	}
	m.mu.Unlock()

	var refs []*ClassMergerRef
	if m.opts.ClassMerger != nil {
		refs = append(refs, m.opts.ClassMerger)
	}
	dirs := make([]string, 0, len(m.opts.PerDir))
	for dir := range m.opts.PerDir {
		dirs = append(dirs, dir)
	}
	sort.Strings(dirs)
	for _, dir := range dirs {
		if merger := m.opts.PerDir[dir].ClassMerger; merger != nil {
			refs = append(refs, merger)
		}
	}
	if len(refs) == 0 {
		m.mu.Lock()
		m.classMergersDone = true
		m.mu.Unlock()
		return nil
	}
	requests := make([]configuredPackageRequest, 0, len(refs))
	for _, ref := range refs {
		requests = append(requests, configuredPackageRequest{
			path:  ref.PkgPath,
			where: fmt.Sprintf("class_merger package %q", ref.PkgPath),
		})
	}
	packages, _, err := m.configuredSourcePackages(requests)
	if err != nil {
		return err // not memoized: an operational load failure is worth retrying
	}
	for _, ref := range refs {
		pkg := packages[ref.PkgPath]
		if pkg == nil {
			err = fmt.Errorf("class_merger: package %q was not loaded", ref.PkgPath)
			break
		}
		if err = validateClassMergerObj(pkg, ref); err != nil {
			break
		}
	}
	m.mu.Lock()
	m.classMergersErr, m.classMergersDone = err, true
	m.mu.Unlock()
	return err
}

// dirOptionsFor returns the PerDir entry for dir, if any.
func (m *Module) dirOptionsFor(dir string) (DirOptions, bool) {
	if len(m.opts.PerDir) == 0 {
		return DirOptions{}, false
	}
	d, ok := m.opts.PerDir[filepath.Clean(dir)]
	return d, ok
}

// classMergerFor returns the class merger that applies to dir.
func (m *Module) classMergerFor(dir string) *ClassMergerRef {
	if d, ok := m.dirOptionsFor(dir); ok && d.ClassMerger != nil {
		return d.ClassMerger
	}
	return m.opts.ClassMerger
}

// classifierFor returns the attrclass.Classifier that applies to dir, mirroring
// classMergerFor: a PerDir entry with a non-nil Classifier overrides
// Options.Classifier for that dir only; every other dir keeps the module-wide
// default (Open always resolves opts.Classifier to attrclass.Builtin() when the
// caller leaves it nil, so this never returns nil).
func (m *Module) classifierFor(dir string) *attrclass.Classifier {
	if d, ok := m.dirOptionsFor(dir); ok && d.Classifier != nil {
		return d.Classifier
	}
	return m.opts.Classifier
}

// urlPresetsFor returns the url-attribute preset NAMES that apply to dir,
// mirroring classifierFor exactly: a PerDir entry with a non-nil URLPresets
// overrides Options.URLPresets for that dir only; every other dir keeps the
// module-wide default. This is the string-identity companion to classifierFor
// — the same effective configuration, but the preset names rather than the
// expanded classifier rules — so a consumer (the LSP) can answer "is the htmx
// preset on for this dir?" without inferring it from rule contents.
func (m *Module) urlPresetsFor(dir string) []string {
	if d, ok := m.dirOptionsFor(dir); ok && d.URLPresets != nil {
		return d.URLPresets
	}
	return m.opts.URLPresets
}

// filterTableFor returns the filter+renderer tables that apply to dir.
//
// withExt says whether the caller is on a path that loads the external importer.
// Every such caller (Generate, Package, typesPackage → analyze) already paid one
// packages.Load that includes the gsx runtime, FilterPkgs, LoadPkgs, the
// Aliases' packages, AND the Renderers' packages — so the tables are HARVESTED
// from those types rather than re-loaded. That kills a second `go list` per
// Module: filter-table loads were running 1:1 with importer loads (148 vs 127
// across the gen suite alone).
//
// buildPackageSkeletons passes withExt=false. That path is `gsx fmt`'s syntactic
// fast lane, which deliberately never loads the importer (it is what took
// `gsx fmt -l` from ~16s to 0.58s); harvesting from types there would ADD the
// full "./..." load it exists to avoid. It keeps the standalone
// loadFilterTableMulti, which loads only the filter packages. Renderer
// resolution is intentionally absent from this path.
//
// A PerDir override always harvests from types, forcing the importer if needed:
// N dirs with N different filter sets then cost ONE load between them. Renderers
// have no PerDir override (Options.Renderers is module-wide), so the per-dir
// memo key below combines the consuming package import path (renderer locality)
// with its canonical filter package set (reserved alias allocation).
//
// A dir naming a filter package the importer never loaded is an error. It must
// never degrade to an empty table — a corpus case that asserts "this filter is
// rejected because its package is not whitelisted" would then pass while
// testing nothing.
func (m *Module) filterTableFor(dir string, withExt bool) (funcTables, error) {
	if m.opts.Bundle != nil {
		return m.opts.Bundle.tables(), nil
	}
	pkgs := m.opts.FilterPkgs
	if d, ok := m.dirOptionsFor(dir); ok && d.FilterPkgs != nil {
		pkgs, withExt = d.FilterPkgs, true
	}
	if !withExt {
		return m.cachedFuncTables()
	}
	pkgs = dedupFilterPkgs(pkgs)
	pkgPath, ok := importPathForDir(m.opts.ModuleRoot, m.opts.ModulePath, dir)
	if !ok {
		return funcTables{}, fmt.Errorf("codegen: package dir %s is outside module root %s", dir, m.opts.ModuleRoot)
	}
	key := pkgPath + "\x00" + strings.Join(pkgs, "\x00")

	m.mu.Lock()
	if tbl, hit := m.dirFuncTbls[key]; hit {
		m.mu.Unlock()
		return tbl, nil
	}
	m.mu.Unlock()

	// The importer's types are the harvest source, so it must be loaded first.
	// Generate/Package already did this; the call is a cache hit there.
	if _, err := m.externalImporter(); err != nil {
		return funcTables{}, err
	}
	tbl, err := m.funcTablesFromSource(dir, pkgs)
	if err != nil {
		return funcTables{}, fmt.Errorf("codegen: filter table for %s: %w", dir, err)
	}
	m.mu.Lock()
	m.dirFuncTbls[key] = tbl
	m.mu.Unlock()
	return tbl, nil
}

// filterTableFromSource harvests filters from configured declaration packages.
// Module-local packages come from declaration-only shipping skeletons or
// retained Go syntax; only packages outside the module use the cold external
// importer. Missing or invalid packages remain hard errors rather than silently
// producing a thinner table.
func (m *Module) filterTableFromSource(pkgs []string) (filterTable, error) {
	requests := make([]configuredPackageRequest, 0, len(pkgs)+len(m.opts.Aliases))
	for _, p := range pkgs {
		requests = append(requests, configuredPackageRequest{path: p, where: fmt.Sprintf("filter package %q", p)})
	}
	for _, a := range m.opts.Aliases {
		requests = append(requests, configuredPackageRequest{path: a.PkgPath, where: fmt.Sprintf("WithFilter %q: package %q", a.Name, a.PkgPath)})
	}
	packages, _, err := m.configuredSourcePackages(requests)
	if err != nil {
		return nil, err
	}
	table, _, err := loadFilterTableFromTypes(packages, pkgs, m.opts.Aliases, nil)
	return table, err
}

// funcTablesFromSource harvests both configured function-table kinds from
// their authoritative declaration sources.
func (m *Module) funcTablesFromSource(dir string, pkgs []string) (funcTables, error) {
	filters, err := m.filterTableFromSource(pkgs)
	if err != nil {
		return funcTables{}, err
	}
	renderers, err := m.rendererTableFor(dir, pkgs)
	if err != nil {
		return funcTables{}, err
	}
	return funcTables{filters: filters, renderers: renderers}, nil
}

// finalRendererAliases returns only the last registration for each TypeKey,
// preserving the relative order of those winning registrations. Package
// resolution and alias assignment operate on this completed registry: a
// shadowed registration cannot require or invalidate an otherwise unused
// package.
func finalRendererAliases(renderers []RendererAlias) []RendererAlias {
	seen := make(map[string]bool, len(renderers))
	winners := make([]RendererAlias, 0, len(renderers))
	for i := len(renderers) - 1; i >= 0; i-- {
		r := renderers[i]
		if seen[r.TypeKey] {
			continue
		}
		seen[r.TypeKey] = true
		winners = append(winners, r)
	}
	for i, j := 0, len(winners)-1; i < j; i, j = i+1, j-1 {
		winners[i], winners[j] = winners[j], winners[i]
	}
	return winners
}

// rendererPackagesFromExt resolves the completed last-wins registry through
// the shared configured-source resolver. Despite the historical name, local
// packages and semantic external boundaries are rebuilt from retained source;
// only source-independent dependencies come directly from the external load.
func (m *Module) rendererPackagesFromExt() (map[string]*types.Package, map[string]bool, error) {
	m.mu.Lock()
	if m.rendererPkgsDone {
		defer m.mu.Unlock()
		return m.rendererPkgs, m.rendererLocal, m.rendererPkgsErr
	}
	m.mu.Unlock()

	winners := finalRendererAliases(m.opts.Renderers)
	requests := make([]configuredPackageRequest, 0, len(winners))
	seen := make(map[string]bool, len(winners))
	for _, r := range winners {
		if seen[r.PkgPath] {
			continue
		}
		seen[r.PkgPath] = true
		requests = append(requests, configuredPackageRequest{
			path:  r.PkgPath,
			where: fmt.Sprintf("renderer for %q: package %q", r.TypeKey, r.PkgPath),
		})
	}
	byPath, local, err := m.configuredSourcePackages(requests)
	if err == nil {
		m.mu.Lock()
		for _, request := range requests {
			if dir := m.sourcePackageDirs[request.path]; dir != "" {
				m.rendererDirs[dir] = true
			}
		}
		m.mu.Unlock()
	}
	if err != nil {
		byPath = nil
		local = nil
	}

	m.mu.Lock()
	m.rendererPkgs, m.rendererLocal = byPath, local
	m.rendererPkgsErr, m.rendererPkgsDone = err, true
	m.mu.Unlock()
	return byPath, local, err
}

// rendererBaseTable resolves, harvests, and globally validates the completed
// registry exactly once. It intentionally stores neither consuming-package
// locality nor reserved aliases; both are presentation details applied to a
// cloned table by rendererTableFor.
func (m *Module) rendererBaseTable() (rendererTable, error) {
	m.mu.Lock()
	if m.rendererTblDone {
		defer m.mu.Unlock()
		return m.rendererTbl, m.rendererTblErr
	}
	m.mu.Unlock()

	byPath, _, err := m.rendererPackagesFromExt()
	var table rendererTable
	if err == nil {
		table, err = harvestRendererEntries(byPath, finalRendererAliases(m.opts.Renderers), nil)
		if err == nil {
			err = validateRendererTable(table)
		}
	}
	m.mu.Lock()
	m.rendererTbl = table
	m.rendererTblErr, m.rendererTblDone = err, true
	m.mu.Unlock()
	return table, err
}

// rendererTableFor clones the module-wide base registry for one consuming
// package, assigning the reserved aliases implied by that package's filter set
// and marking only exact package ownership as a local direct call.
func (m *Module) rendererTableFor(dir string, filterPkgs []string) (rendererTable, error) {
	base, err := m.rendererBaseTable()
	if err != nil {
		return nil, err
	}
	pkgPath, ok := importPathForDir(m.opts.ModuleRoot, m.opts.ModulePath, dir)
	if !ok {
		return nil, fmt.Errorf("codegen: package dir %s is outside module root %s", dir, m.opts.ModuleRoot)
	}
	aliasPaths := append([]string{}, filterPkgs...)
	for _, a := range m.opts.Aliases {
		aliasPaths = append(aliasPaths, a.PkgPath)
	}
	for _, r := range finalRendererAliases(m.opts.Renderers) {
		aliasPaths = append(aliasPaths, r.PkgPath)
	}
	aliases := filterAliases(aliasPaths)
	aliased := make(rendererTable, len(base))
	for key, entry := range base {
		entry.alias = aliases[entry.pkgPath]
		aliased[key] = entry
	}
	return aliased.forPackage(pkgPath), nil
}

// maybeRebuildFset rebuilds the FileSet (and ext/pkgTypes/pkgResults) when project re-parse
// growth since the last load exceeds fsetRebuildBytes. A zero threshold disables it.
// Called at the start of Package/Generate (under analysisMu), before applyDirty.
func (m *Module) maybeRebuildFset() {
	m.mu.Lock()
	over := m.sourceInventoryDirty || m.fsetRebuildBytes > 0 && m.fset.Base()-m.fsetBaseline > m.fsetRebuildBytes
	m.mu.Unlock()
	if over {
		m.rebuildFset()
	}
}

// rebuildFset discards the grown FileSet and the caches that hold positions into it
// — ext, pkgTypes, targetDeclTypes, and pkgResults — together, so nothing live references the old fset (no orphaned
// positions). The next externalImporter reloads ext into the fresh fset and recaptures
// fsetBaseline; analyze re-parses into it. The import graph, dirty set, and overrides
// survive (path/content-based), so reverse-dependency invalidation keeps working.
// Assumes analysisMu held by the caller (Package/Generate); takes m.mu for the writes.
func (m *Module) rebuildFset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.fset = token.NewFileSet()
	m.ext = nil
	m.extPkgs = nil
	m.externalImportPaths = map[string]bool{}
	m.extErrs = nil
	m.externalBackedges = map[string][]string{}
	m.sourcePackages = map[string]projectSourcePackage{}
	m.sourcePackageDirs = map[string]string{}
	m.sourceGsxDirs = map[string]bool{}
	m.sourceManifest = nil
	m.helperGoSourceManifest = nil
	m.directHelperGoViews = map[string]helperGoView{}
	m.sourceInventoryFacts = map[string]gsxSourceInventoryFact{}
	m.sourceReloadReasons = map[string]sourceview.ReloadReason{}
	m.sourceInventoryReady = false
	m.sourceInventoryDirty = false
	m.goSourceReload = false
	m.funcTbl, m.funcTblErr, m.funcTblDone = funcTables{}, nil, false
	m.rendererPkgs, m.rendererLocal = nil, nil
	m.rendererPkgsErr, m.rendererPkgsDone = nil, false
	m.rendererTbl, m.rendererTblErr, m.rendererTblDone = nil, nil, false
	m.dirFuncTbls = map[string]funcTables{}
	m.classMergersErr, m.classMergersDone = nil, false
	m.pkgTypes = map[string]*types.Package{}
	m.targetDeclTypes = map[string]*types.Package{}
	m.targetDeclProvenance = componentTargetProvenanceCache{}
	m.configuredDeclTypes = map[string]*types.Package{}
	m.pkgResults = map[string]*PackageResult{}
	m.fsetBaseline = 0
	m.rebuildCount++
}

// rebuilds returns the number of fset rebuilds performed (test hook).
func (m *Module) rebuilds() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.rebuildCount
}

// sourceIndexBuilds returns the number of retained semantic indexes built by
// this Module (test hook).
func (m *Module) sourceIndexBuilds() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sourceIndexBuildCount
}

// Package returns the full retained analysis for a single gsx package dir,
// without codegen (Files stays empty; Generate fills it). It populates the
// FileSets, *types.Info,
// *types.Package, ExprMap, GSXFiles, and the cross/nav indexes used by the LSP.
func (m *Module) Package(dir string) (*PackageResult, error) {
	m.analysisMu.Lock()
	defer m.analysisMu.Unlock()
	m.maybeRebuildFset()
	m.applyDirty()
	m.mu.Lock()
	cached := m.pkgResults[dir]
	m.mu.Unlock()
	if cached != nil {
		return cached, nil
	}
	if err := m.validateConfiguredMergers(); err != nil {
		return nil, err
	}
	ext, err := m.externalImporter()
	if err != nil {
		return nil, err
	}
	a, err := m.analyze(dir, newModuleImporter(m, ext), analysisRetainedPackage)
	if err != nil {
		if diags, ok := diagnosticsFromSourceError(err); ok {
			return &PackageResult{Files: map[string][]byte{}, Diags: diags}, nil
		}
		return nil, err
	}
	res := &PackageResult{
		Files:       map[string][]byte{},
		GSXFset:     a.gsxFset,
		Fset:        a.skelFset,
		Info:        a.info,
		Types:       a.pkg,
		GSXFiles:    a.gsxFiles,
		ExprMap:     a.exprMap,
		CtrlMap:     a.ctrlMap,
		SigTypes:    a.sigTypes,
		SourceIndex: a.sourceIndex,
	}
	// Run emit for side-effect diagnostics only (unknown filter, attr-error, etc.).
	// Gated on len(a.typeErrs)==0, exactly like Generate: running generateFile on a
	// type-error package adds spurious secondary diagnostics (e.g. "could not resolve
	// type of interpolation") because resolved lacks entries for identifiers the
	// type-checker flagged. The type-error diagnostics themselves are already in the
	// bag (added in analyze). We discard the generated bytes; only bag side-effects matter.
	//
	// Also gated on !a.bag.HasErrors(): an analyze-time bag.Errorf (e.g.
	// "unsupported-node", "literal-in-stage-args") can leave a skeleton that
	// STILL type-checks (typeErrs==0) while the SAME AST shape would splice
	// invalid Go if generateFile's independent, real lowering ran over it —
	// running generateFile anyway would either silently emit that invalid Go or
	// have it choke gofmt's format.Source, adding a spurious, unpositioned
	// "format generated source" diagnostic on top of the real one. Skipping
	// generateFile whenever the bag already holds an error mirrors the
	// type-error semantics one level up: any analyze-time error, not just a
	// type-check one, blocks emission for the whole package.
	//
	// Safe despite emit's in-place AST mutation: analyze re-parses a fresh gsx AST
	// on every call, so there is no previously-mutated tree that could be corrupted
	// by a concurrent or repeated generateFile pass on the same nodes.
	if len(a.typeErrs) == 0 && !a.bag.HasErrors() {
		for _, f := range a.gsxFiles {
			generateFile(f, a.pkg, a.resolved, a.table, a.gsxFset, a.classifier, a.bag, nil, nil, nil, true, true, a.merger, a.componentPlan, a.positionalPlan)
		}
	}
	res.Diags = a.bag.Sorted()
	res.Filters = filterCandidates(a.table)
	res.URLPresets = m.urlPresetsFor(dir)
	res.CrossIndex, res.NavIndex = buildCrossNav(a.compByKey, a.objKey, a.gsxFiles, a.gsxFset, a.skelFset, a.info)
	res.ComponentCalls = componentCallFacts(a.positionalPlan)
	res.ComponentDecls = a.componentDecls
	addLocalComponentCallRefs(res.CrossIndex, res.ComponentCalls, a.gsxFset, a.pkg.Path())
	if !a.bag.HasErrors() && len(a.typeErrs) == 0 {
		res.ComponentParamDecls, err = componentParamDeclarationFacts(
			a.compByKey, a.objKey, a.compsByXGo, a.goFiles, &a.componentPlan, a.info, a.gsxFset, a.pkg.Path(),
		)
		if err != nil {
			return nil, err
		}
		res.ComponentParamRefs = componentParamReferenceFacts(res.ComponentCalls, res.ComponentParamDecls, a.gsxFset)
		res.ComponentParamRefs = append(res.ComponentParamRefs, componentParamBodyReferenceFacts(
			res.ComponentParamDecls, a.objKey, a.exprMap, a.ctrlMap, a.info, a.gsxFset,
		)...)
	}
	// Unused imports come from analyze's syntactic classifier (unusedFromSkeletons,
	// computed alongside the type-check) — the same classifier the `gsx fmt` CLI
	// trusts (Module.UnusedImports) — never from correlating raw type-error
	// positions. See docs/superpowers/specs/2026-07-09-lsp-unused-imports-design.md.
	res.UnusedImports = a.unusedImports
	// Missing imports come from the same type-checked skeletons, alongside the
	// unused-import classification above (missingFromSkeletons). See
	// MissingImport's doc for why the Name is left unresolved to an import path.
	res.MissingImports = a.missingImports
	m.mu.Lock()
	m.pkgResults[dir] = res
	m.mu.Unlock()
	return res, nil
}

// AnalyzeEphemeral runs one warm analysis of dir with absPath's source replaced
// by src, WITHOUT recording the result: pkgResults is never written, and the
// pkgTypes/targetDeclProvenance entries analyze writes for dir are snapshotted
// and restored afterward. Dependency packages analyzed (and cached) along the
// way use their real sources — that warmth is shared and desirable. Serialized
// under analysisMu like Package/Generate. Source-level breakage returns a
// diagnostics-only PackageResult (nil Info/Types), mirroring Package's shell
// semantics.
//
// Cache-write audit (analyze's full body, module_importer.go:1032+, and every
// function it calls with the analyzed dir): the ONLY module caches keyed by dir
// that analyze writes from the patched source are pkgTypes[dir] (line ~1501)
// and targetDeclProvenance[dir] (line ~1506); both are snapshot/restored below.
// targetDeclTypes[dir] is NOT written for the analyzed dir — analyze marks it
// loading in the componentTargetImporter, so a recursive
// targetDeclarationPackage(dir) cycle-errors before its write. The import-graph
// writes for dir — recordImports (shipping) and recordTargetImports (exact
// target, via discoverComponentTargets) — replace dir's forward edges and its
// reverse edges with the SAME set the live buffer records: the repair only
// patches bytes at the cursor, so the import specs are byte-identical and the
// rewrite is idempotent, exactly as the shipping-graph reasoning already
// accepts (recordImports' own doc: "the edited package always re-analyzes in
// the same turn"). sourceIndexBuildCount++ is a monotonic observability
// counter, not a per-dir correctness cache. All other dir-keyed writes
// (dirFuncTbls, typeEnvironment, configuredDeclTypes, recordSourceDeclImports)
// key on config/import-derived dirs whose real sources the overlay never
// touches — shared warmth, not corruption.
func (m *Module) AnalyzeEphemeral(dir, absPath string, src []byte) (*PackageResult, error) {
	m.analysisMu.Lock()
	defer m.analysisMu.Unlock()
	m.maybeRebuildFset()
	m.applyDirty()
	if err := m.validateConfiguredMergers(); err != nil {
		return nil, err
	}
	ext, err := m.externalImporter()
	if err != nil {
		return nil, err
	}

	// Install the one-shot overlay and snapshot the two cache entries analyze
	// writes for dir (see the cache-write audit above).
	m.mu.Lock()
	m.ephemeral = map[string][]byte{absPath: src}
	prevTypes, hadTypes := m.pkgTypes[dir]
	var prevProv map[string]componentTargetDeclarationProvenance
	var hadProv bool
	if m.targetDeclProvenance != nil {
		prevProv, hadProv = m.targetDeclProvenance[dir]
	}
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		m.ephemeral = nil
		if hadTypes {
			m.pkgTypes[dir] = prevTypes
		} else {
			delete(m.pkgTypes, dir)
		}
		if m.targetDeclProvenance != nil {
			if hadProv {
				m.targetDeclProvenance[dir] = prevProv
			} else {
				delete(m.targetDeclProvenance, dir)
			}
		}
		m.mu.Unlock()
	}()

	a, err := m.analyze(dir, newModuleImporter(m, ext), analysisRetainedPackage)
	if err != nil {
		if diags, ok := diagnosticsFromSourceError(err); ok {
			return &PackageResult{Files: map[string][]byte{}, Diags: diags}, nil
		}
		return nil, err
	}
	res := &PackageResult{
		Files:       map[string][]byte{},
		GSXFset:     a.gsxFset,
		Fset:        a.skelFset,
		Info:        a.info,
		Types:       a.pkg,
		GSXFiles:    a.gsxFiles,
		ExprMap:     a.exprMap,
		CtrlMap:     a.ctrlMap,
		SigTypes:    a.sigTypes,
		SourceIndex: a.sourceIndex,
	}
	res.Diags = a.bag.Sorted()
	res.CrossIndex, res.NavIndex = buildCrossNav(a.compByKey, a.objKey, a.gsxFiles, a.gsxFset, a.skelFset, a.info)
	res.ComponentCalls = componentCallFacts(a.positionalPlan)
	res.ComponentDecls = a.componentDecls
	res.Filters = filterCandidates(a.table)
	res.URLPresets = m.urlPresetsFor(dir)
	// NOT stored in m.pkgResults, NOT running generateFile (emit-side
	// diagnostics are irrelevant to completion), no param decl/ref facts
	// (rename-only surface).
	return res, nil
}

// Generate runs analysis on dir and emits a .x.go for every .gsx file in the
// package. It returns the generated bytes keyed by the gsx file's absolute path,
// any diagnostics (including script-resolution errors from analyze), and a hard
// error only when analysis itself fails (parse error, load error, etc.).
// Emit errors (per-component) are soft: they surface as diagnostics in the
// returned slice and the file is omitted from out.
//
// Type-error semantics: a package that fails to type-check emits NOTHING (the
// emit loop below is gated on len(a.typeErrs)==0), and the type-error
// diagnostics collected by checkSkeletonPackage are surfaced via the returned
// slice (analyze adds them to the bag). The golden corpus test drives this path
// directly, so type-error corpus cases are validated byte-for-byte.
func (m *Module) Generate(dir string) (map[string][]byte, []diag.Diagnostic, error) {
	m.analysisMu.Lock()
	defer m.analysisMu.Unlock()
	m.maybeRebuildFset()
	m.applyDirty()
	if err := m.validateConfiguredMergers(); err != nil {
		return nil, nil, err
	}
	ext, err := m.externalImporter()
	if err != nil {
		return nil, nil, err
	}
	a, err := m.analyze(dir, newModuleImporter(m, ext), analysisGeneration)
	if err != nil {
		if diags, ok := diagnosticsFromSourceError(err); ok {
			return map[string][]byte{}, diags, nil
		}
		return nil, nil, err
	}
	// Use the bag created in analyze (shares fset, carries script-resolution diags).
	bag := a.bag
	out := map[string][]byte{}
	// When a package has type errors, skip generateFile entirely — only the
	// type-error diagnostics are surfaced. Running generateFile on a type-error
	// package emits spurious secondary diagnostics (e.g. "could not resolve type of
	// interpolation") because resolved lacks entries for identifiers the type-checker
	// flagged as undefined. Also skip when the bag already holds an analyze-time
	// error (e.g. "literal-in-stage-args") even though typeErrs is clean — see the
	// matching gate/comment in Package above.
	if len(a.typeErrs) == 0 && !bag.HasErrors() {
		for path, f := range a.gsxFiles {
			gen, ok := generateFile(f, a.pkg, a.resolved, a.table, a.gsxFset, a.classifier, bag, m.opts.CSSMin, m.opts.JSMin, m.opts.JSONMin, m.opts.CSSMinify, m.opts.JSMinify, a.merger, a.componentPlan, a.positionalPlan)
			if !ok {
				continue
			}
			out[path] = gen
		}
	}
	return out, bag.Sorted(), nil
}
