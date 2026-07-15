package gen

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/gsxhq/gsx/internal/codegen"
	"github.com/gsxhq/gsx/internal/sourceview"
	"github.com/gsxhq/gsx/internal/typebundle"
)

// BundledResolver transforms complete in-memory source sets against an embedded
// external type universe. It has no project root and exposes no disk-backed
// generation surface.
type BundledResolver struct {
	engine resolverEngine
}

// NewBundledResolver builds a BundledResolver from an embedded type bundle (see
// internal/typebundle) instead of packages.Load — no `go list`, no subprocess.
// This is the resolver a WASM build uses: the bundle, produced at build time,
// MUST contain the gsx runtime, every filterPkg, and every import a snippet may
// reference. Empty filterPkgs defaults to the built-in std filter package.
func NewBundledResolver(bundle []byte, filterPkgs []string) (*BundledResolver, error) {
	typeUniverse, err := typebundle.Read(bundle)
	if err != nil {
		return nil, fmt.Errorf("gen: read type bundle: %w", err)
	}
	inner, err := codegen.NewCachedResolverFromTypes(typeUniverse.Packages, typeUniverse.Sizes, typeUniverse.Target.LanguageVersion, filterPkgs, nil)
	if err != nil {
		return nil, err
	}
	return &BundledResolver{engine: resolverEngine{bundle: inner}}, nil
}

type resolverEngine struct {
	bundle *codegen.Bundle
}

// DefaultPlaygroundImports is the fixed allowlist the playground caches types
// for. It covers the standard-library packages a playground component is likely
// to reference. Users may supply a custom list to NewCachedResolver instead.
var DefaultPlaygroundImports = []string{
	"context", "io", "strconv", "fmt", "strings", "time", "sort", "errors",
	"math", "math/rand", "unicode", "unicode/utf8", "html",
}

// CachedResolver wraps an internal codegen.Bundle and exposes an
// in-process Generate method. Dependencies are loaded once at construction time
// (via NewCachedResolver); each Generate call runs entirely in-process with no
// per-render go list or subprocess.
type CachedResolver struct {
	engine         resolverEngine
	moduleRoot     string
	modulePath     string
	moduleRootInfo os.FileInfo
}

// NewCachedResolver constructs a CachedResolver for a module rooted at
// moduleDir, pre-loading the gsx std filter package and allowImports (e.g.
// DefaultPlaygroundImports). The one-time load runs packages.Load; subsequent
// Generate calls are fully in-process.
func NewCachedResolver(moduleDir string, allowImports []string) (*CachedResolver, error) {
	root, modulePath, err := moduleRoot(moduleDir)
	if err != nil {
		return nil, err
	}
	absModuleDir, err := filepath.Abs(moduleDir)
	if err != nil {
		return nil, err
	}
	if root != filepath.Clean(absModuleDir) {
		return nil, fmt.Errorf("gen: cached resolver moduleDir %s is not a module root", moduleDir)
	}
	rootInfo, err := os.Stat(root)
	if err != nil {
		return nil, fmt.Errorf("gen: stat cached resolver module root %s: %w", root, err)
	}
	r, err := codegen.NewCachedResolver(root, []string{codegen.StdImportPath}, nil, allowImports)
	if err != nil {
		return nil, err
	}
	return &CachedResolver{
		engine:         resolverEngine{bundle: r},
		moduleRoot:     root,
		modulePath:     modulePath,
		moduleRootInfo: rootInfo,
	}, nil
}

// Generate runs codegen in-process for the package under dir, using the
// preloaded resolver (no go list / subprocess per call). srcOverride maps
// .gsx paths to their in-memory source. Keys may be:
//   - relative paths like "views/comp.gsx" (resolved relative to dir's parent)
//   - absolute paths
//
// nil srcOverride means read all source from disk.
//
// The returned Result.Files maps each input .gsx path (using the same key form
// as the srcOverride entries) to its generated .x.go bytes. Type errors and
// parse errors are returned in Result.Diags. The returned error is non-nil when
// any error-severity diagnostic was produced or when an operational (I/O)
// failure occurred.
func (c *CachedResolver) Generate(dir string, srcOverride map[string][]byte) (Result, error) {
	moduleRoot, modulePath, err := c.moduleForDir(dir)
	if err != nil {
		return Result{}, err
	}
	return c.engine.generateBound(dir, srcOverride, moduleRoot, modulePath)
}

func (c *CachedResolver) moduleForDir(dir string) (string, string, error) {
	if c.moduleRoot == "" || c.moduleRootInfo == nil {
		return "", "", fmt.Errorf("gen: cached resolver has no bound module root")
	}
	root, modulePath, err := moduleRoot(dir)
	if err != nil {
		return "", "", err
	}
	rootInfo, err := os.Stat(root)
	if err != nil {
		return "", "", fmt.Errorf("gen: stat target module root %s: %w", root, err)
	}
	if !os.SameFile(c.moduleRootInfo, rootInfo) {
		return "", "", fmt.Errorf("gen: cached resolver is bound to module root %s; target directory %s belongs to %s", c.moduleRoot, dir, root)
	}
	owned, err := sourceview.OwnsDir(root, dir)
	if err != nil {
		return "", "", fmt.Errorf("gen: validate cached resolver target directory %s: %w", dir, err)
	}
	if !owned {
		return "", "", fmt.Errorf("gen: cached resolver target directory %s resolves outside its physical module root %s", dir, root)
	}
	if modulePath != c.modulePath {
		return "", "", fmt.Errorf("gen: cached resolver module path changed from %q to %q at %s", c.modulePath, modulePath, root)
	}
	return root, modulePath, nil
}

// memDir is the virtual package directory GenerateSource uses. It is absolute
// so filepath.Abs never consults a working directory. SourceOnly makes its
// existence and contents irrelevant: only the supplied overrides participate.
const memDir = "/__gsxmem__"

// GenerateSource transforms a single in-memory .gsx source into its generated
// .x.go with NO filesystem and NO subprocess — the entry a js/wasm build calls.
// name is a virtual filename (default "source.gsx") used only for diagnostic
// positions. The returned Result.Files holds the generated bytes (empty when
// there were error-severity diagnostics); Result.Diags holds parse/type errors.
func (b *BundledResolver) GenerateSource(name string, src []byte) (Result, error) {
	if name == "" {
		name = "source.gsx"
	}
	if !strings.HasSuffix(name, ".gsx") {
		name += ".gsx"
	}
	path := filepath.Join(memDir, filepath.Base(name))
	return b.engine.generateSourceOnly(memDir, map[string][]byte{path: src})
}

// GenerateSources transforms several in-memory .gsx files of ONE package into
// their generated .x.go, with no filesystem and no subprocess — the multi-file
// form GenerateSource wraps. files maps a virtual filename to its source; every
// file must declare the same package. Result.Files holds one entry per input
// file (keyed by virtual .x.go path); Result.Diags holds parse/type errors.
func (b *BundledResolver) GenerateSources(files map[string][]byte) (Result, error) {
	override := make(map[string][]byte, len(files))
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	originalByPath := make(map[string]string, len(files))
	for _, original := range names {
		name := original
		if !strings.HasSuffix(name, ".gsx") {
			name += ".gsx"
		}
		base := filepath.Base(name)
		path := filepath.Join(memDir, base)
		if previous, exists := originalByPath[path]; exists {
			return Result{}, fmt.Errorf(
				"gen: GenerateSources virtual filenames %q and %q both resolve to %q",
				previous,
				original,
				base,
			)
		}
		originalByPath[path] = original
		override[path] = files[original]
	}
	return b.engine.generateSourceOnly(memDir, override)
}

func (e resolverEngine) generateBound(dir string, srcOverride map[string][]byte, moduleRoot, modulePath string) (Result, error) {
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return Result{}, err
	}
	m, err := codegen.Open(codegen.Options{
		ModuleRoot: moduleRoot,
		ModulePath: modulePath,
		FilterPkgs: []string{codegen.StdImportPath},
		Bundle:     e.bundle,
	})
	if err != nil {
		return Result{}, err
	}
	return generateWithModule(m, absDir, srcOverride)
}

func (e resolverEngine) generateSourceOnly(dir string, srcOverride map[string][]byte) (Result, error) {
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return Result{}, err
	}
	m, err := codegen.Open(codegen.Options{
		ModuleRoot: absDir,
		ModulePath: absDir,
		SourceOnly: true,
		FilterPkgs: []string{codegen.StdImportPath},
		Bundle:     e.bundle,
	})
	if err != nil {
		return Result{}, err
	}
	return generateWithModule(m, absDir, srcOverride)
}

// generateWithModule drives a fresh per-call Module with a prebuilt bundle and
// maps its output to the public gen.Result. The caller has already selected
// either a root-bound project module or a source-only module; this shared path
// has no resolver mode of its own.
func generateWithModule(m *codegen.Module, absDir string, srcOverride map[string][]byte) (Result, error) {

	// Resolve srcOverride keys to absolute paths (unchanged): relative keys like
	// "views/comp.gsx" resolve against the directory CONTAINING dir; absolute keys
	// pass through.
	absOverride := make(map[string][]byte, len(srcOverride))
	for k, v := range srcOverride {
		if filepath.IsAbs(k) {
			absOverride[k] = v
		} else {
			absOverride[filepath.Join(filepath.Dir(absDir), filepath.FromSlash(k))] = v
		}
	}

	for p, srcBytes := range absOverride {
		m.SetOverride(p, srcBytes)
	}

	out, diags, err := m.Generate(absDir)
	if err != nil {
		return Result{}, err
	}

	// Map out (abs .gsx path -> .x.go bytes) to Result.Files, preferring the
	// relative key form when the caller used relative keys (unchanged mapping).
	files := make(map[string][]byte, len(out))
	for absPath, content := range out {
		base := strings.TrimSuffix(absPath, ".gsx")
		absXGo := base + ".x.go"
		if len(srcOverride) > 0 {
			rel, relErr := filepath.Rel(filepath.Dir(absDir), absXGo)
			if relErr == nil && !strings.HasPrefix(rel, "..") {
				files[rel] = content
				continue
			}
		}
		files[absXGo] = content
	}

	var retErr error
	if anyErrorDiag(diags) {
		retErr = errInProcessDiagnostics
	}
	return Result{Files: files, Diags: diags}, retErr
}

// errInProcessDiagnostics is the sentinel returned when in-process generation
// produced at least one error-severity diagnostic.
var errInProcessDiagnostics = errors.New("gen: diagnostics reported")
