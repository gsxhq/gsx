package gen

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/gsxhq/gsx/internal/codegen"
	"github.com/gsxhq/gsx/internal/sourceview"
)

var (
	selfHashOnce sync.Once
	selfHashVal  string
)

// selfHash returns a content hash of the running gsx executable, memoized for
// the process. Folded into every cache key so ANY change to the gsx binary —
// including a codegen/emit change, even an uncommitted dev rebuild — invalidates
// cached output automatically. This is the robust backstop the manual
// codegen.Version constant alone cannot provide: a human who forgets to bump the
// constant after changing emit (e.g. 5aed1ba) would otherwise serve stale .x.go.
// Returns "" if the executable can't be located or read; the caller still has
// codegen.Version() as a coarse lever in that case.
func selfHash() string {
	selfHashOnce.Do(func() {
		exe, err := os.Executable()
		if err != nil {
			return
		}
		data, err := os.ReadFile(exe)
		if err != nil {
			return
		}
		sum := sha256.Sum256(data)
		selfHashVal = hex.EncodeToString(sum[:])
	})
	return selfHashVal
}

// codegenIdentity is the cache pin for "which generator produced this output".
// It composes the manual codegen.Version() (a coarse, explicit invalidation
// lever for project-wide busts) with selfHash() (automatic invalidation on any
// binary change). Folded into every cache key via computeKey.
func codegenIdentity() string {
	return "v=" + codegen.Version() + "\x00bin=" + selfHash()
}

type pkgModule = sourceview.ModuleMetadata
type pkgInfo = sourceview.PackageMetadata

// loadGraph runs the graph query under a freshly captured authoritative Go
// command context. Production uses loadGraphWithContext so this metadata query
// and the Module consume the exact same snapshot.
func loadGraph(root string) (sourceview.Graph, error) {
	moduleDir, modulePath, err := moduleRoot(root)
	if err != nil {
		return nil, err
	}
	manifest, err := sourceview.Build(sourceview.BuildOptions{ModuleRoot: moduleDir, ModulePath: modulePath})
	if err != nil {
		return nil, err
	}
	packageDirs := manifest.PackageDirs()
	dirs := make([]string, 0, len(packageDirs))
	for _, dir := range packageDirs {
		dirs = append(dirs, dir)
	}
	return loadGraphWithContext(codegen.CaptureGoCommandContext(moduleDir), manifest, dirs, nil)
}

// loadGraphWithContext runs `go list -deps -json` (metadata only — NO -export)
// from the captured module root. dirs selects the requested authored GSX
// closure. roots adds configured semantic packages that may not be reachable
// from that graph, so local replacements behind those inputs are visible to
// cache hashing.
func loadGraphWithContext(context *codegen.GoCommandContext, manifest *sourceview.Manifest, dirs []string, roots []string) (sourceview.Graph, error) {
	if manifest == nil {
		return nil, fmt.Errorf("gen: cache metadata requires a source manifest")
	}
	patterns, err := graphQueryPatterns(manifest, dirs, roots)
	if err != nil {
		return nil, err
	}
	overlay, err := manifest.MaterializeGoOverlay()
	if err != nil {
		return nil, fmt.Errorf("gen: materialize source manifest: %w", err)
	}
	defer overlay.Close()
	args := append([]string{"list", "-deps", "-json", "-compiled", "-overlay=" + overlay.Path()}, patterns...)
	out, err := context.Run(args...)
	if err != nil {
		return nil, fmt.Errorf("gen: go list: %w", err)
	}
	graph, err := sourceview.DecodeGraph(strings.NewReader(string(out)))
	if err != nil {
		return nil, fmt.Errorf("gen: decode go list: %w", err)
	}
	return graph, nil
}

// graphQueryPatterns converts only manifest-proven main-module GSX packages to
// filesystem patterns. cmd/go resolves an import-path argument through module
// requirements before an overlay-only sentinel can make that package visible;
// the relative pattern enters the owned directory and lets -overlay provide the
// selected Go file. External and otherwise unproven roots retain import-path
// semantics.
func graphQueryPatterns(manifest *sourceview.Manifest, dirs []string, roots []string) ([]string, error) {
	patterns := make([]string, 0)
	seen := map[string]bool{"C": true, "": true}
	add := func(path string) error {
		if dir, owned := manifest.PackageDir(path); owned {
			rel, err := filepath.Rel(manifest.ModuleRoot(), dir)
			if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				return fmt.Errorf("gen: manifest package %q directory %q is outside module root %q", path, dir, manifest.ModuleRoot())
			}
			path = "."
			if rel != "." {
				path = "./" + filepath.ToSlash(rel)
			}
		}
		if !seen[path] {
			seen[path] = true
			patterns = append(patterns, path)
		}
		return nil
	}
	selectedRoots, err := manifest.SelectedLoadRoots(dirs)
	if err != nil {
		return nil, err
	}
	for _, path := range selectedRoots {
		if err := add(path); err != nil {
			return nil, err
		}
	}
	for _, path := range roots {
		if err := add(path); err != nil {
			return nil, err
		}
	}
	sort.Strings(patterns)
	return patterns, nil
}

func configuredPackagePaths(filterPkgs []string, aliases []codegen.FilterAlias, renderers []codegen.RendererAlias, classMerger *codegen.ClassMergerRef) []string {
	paths := append([]string(nil), filterPkgs...)
	for _, alias := range aliases {
		paths = append(paths, alias.PkgPath)
	}
	finalRenderers := map[string]codegen.RendererAlias{}
	for _, renderer := range renderers {
		finalRenderers[renderer.TypeKey] = renderer
	}
	for _, renderer := range finalRenderers {
		paths = append(paths, renderer.PkgPath)
	}
	if classMerger != nil {
		paths = append(paths, classMerger.PkgPath)
	}
	return dedupSorted(paths)
}

type cacheKeyConfig struct {
	buildContext          string
	codegenIdentity       string
	additionalSourceRoots []string
	filterPackages        []string
	aliases               []codegen.FilterAlias
	renderers             []codegen.RendererAlias
	classifierFingerprint string
	cssMinify             bool
	jsMinify              bool
	verbatimTags          bool
	classMerger           *codegen.ClassMergerRef
}

// computeKey combines generator/configuration identity with the authoritative
// source projection shared by cache metadata and normal codegen.
func computeKey(dir string, projection *sourceview.CacheProjection, config cacheKeyConfig) (string, error) {
	configuredPaths := configuredPackagePaths(config.filterPackages, config.aliases, config.renderers, config.classMerger)
	semanticRoots := append(append([]string(nil), config.additionalSourceRoots...), configuredPaths...)
	sourceIdentity, err := projection.Digest(dir, semanticRoots)
	if err != nil {
		return "", err
	}
	pins := dedupSorted(config.filterPackages)
	// Fold the explicit WithFilter aliases (name+pkgPath+funcName, in registration
	// order) into the key so a changed alias invalidates cached output. Order is
	// significant (last-wins), so this is NOT sorted — mirror the registration
	// order the resolver sees.
	var aliasPins []string
	for _, a := range config.aliases {
		aliasPins = append(aliasPins, a.Name+"="+a.PkgPath+"."+a.FuncName)
	}
	// renderers= pin: last-wins per TypeKey resolved FIRST, then sorted by
	// TypeKey — UNLIKE aliases= above (registration order is meaning there),
	// the renderer table is a per-key map, so two configs with the same final
	// table (regardless of file/option split or registration order) must hash
	// identically.
	finalRenderers := map[string]codegen.RendererAlias{}
	for _, r := range config.renderers {
		finalRenderers[r.TypeKey] = r
	}
	rendererTypeKeys := make([]string, 0, len(finalRenderers))
	for k := range finalRenderers {
		rendererTypeKeys = append(rendererTypeKeys, k)
	}
	sort.Strings(rendererTypeKeys)
	var rendererPins []string
	for _, k := range rendererTypeKeys {
		r := finalRenderers[k]
		rendererPins = append(rendererPins, k+"="+r.PkgPath+"."+r.FuncName)
	}
	cm := "cm="
	if config.classMerger != nil {
		cm += config.classMerger.PkgPath + "." + config.classMerger.FuncName
	}
	h := sha256.New()
	// codegenID (codegenIdentity) folds in BOTH the manual codegen.Version and the
	// gsx binary hash, so it supersedes a bare Version() pin: any emit/lowering
	// change auto-invalidates even when the constant is not bumped.
	fmt.Fprintf(h, "gsxcache-v3\x00%s\x00%s\x00source=%s\x00", config.codegenIdentity, config.buildContext, sourceIdentity)
	fmt.Fprintf(h, "filters=%s\x00aliases=%s\x00renderers=%s\x00cls=%s\x00minify=css:%d,js:%d\x00serial=%d\x00%s\x00", strings.Join(pins, "\x00"), strings.Join(aliasPins, "\x00"), strings.Join(rendererPins, "\x00"), config.classifierFingerprint, b2i(config.cssMinify), b2i(config.jsMinify), b2i(config.verbatimTags), cm)
	return hex.EncodeToString(h.Sum(nil)), nil
}

func dedupSorted(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}

// b2i maps a bool to 1/0 for stable inclusion in the cache-key digest.
func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}
