package gen

import (
	"path/filepath"
	"strings"

	"github.com/gsxhq/gsx/internal/codegen"
	"github.com/gsxhq/gsx/internal/diag"
)

// DefaultPlaygroundImports is the fixed allowlist the playground caches types
// for. It covers the standard-library packages a playground component is likely
// to reference. Users may supply a custom list to NewCachedResolver instead.
var DefaultPlaygroundImports = []string{
	"context", "io", "strconv", "fmt", "strings", "time", "sort", "errors",
	"math", "math/rand", "unicode", "unicode/utf8", "html",
}

// CachedResolver wraps an internal codegen.CachedResolver and exposes an
// in-process Generate method. Dependencies are loaded once at construction time
// (via NewCachedResolver); each Generate call runs entirely in-process with no
// per-render go list or subprocess.
type CachedResolver struct {
	inner *codegen.CachedResolver
}

// NewCachedResolver constructs a CachedResolver for a module rooted at
// moduleDir, pre-loading the gsx std filter package and allowImports (e.g.
// DefaultPlaygroundImports). The one-time load runs packages.Load; subsequent
// Generate calls are fully in-process.
func NewCachedResolver(moduleDir string, allowImports []string) (*CachedResolver, error) {
	r, err := codegen.NewCachedResolver(moduleDir, []string{codegen.StdImportPath}, nil, allowImports)
	if err != nil {
		return nil, err
	}
	return &CachedResolver{inner: r}, nil
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
	return generateInProcess(c.inner, dir, srcOverride)
}

// generateInProcess implements CachedResolver.Generate. It resolves all paths
// to absolute, calls GeneratePackagesWithResolver, and maps the internal
// PackageResult back to the public gen.Result type.
func generateInProcess(resolver *codegen.CachedResolver, dir string, srcOverride map[string][]byte) (Result, error) {
	// Resolve dir to an absolute path.
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return Result{}, err
	}

	// Resolve srcOverride keys to absolute paths. Keys like "views/comp.gsx"
	// are treated as relative to the PARENT of dir (i.e., the module root or
	// the caller's working directory). Absolute keys are left unchanged.
	absOverride := make(map[string][]byte, len(srcOverride))
	for k, v := range srcOverride {
		if filepath.IsAbs(k) {
			absOverride[k] = v
		} else {
			// Relative paths: resolve against the directory CONTAINING dir so
			// that "views/comp.gsx" with dir="views" maps to absDir+"/comp.gsx".
			absOverride[filepath.Join(filepath.Dir(absDir), filepath.FromSlash(k))] = v
		}
	}

	results, err := codegen.GeneratePackagesWithResolver("", []string{absDir}, resolver, nil, absOverride)
	if err != nil {
		return Result{}, err
	}

	pr, ok := results[absDir]
	if !ok {
		return Result{}, nil
	}

	// Map PackageResult.Files (abs .gsx path -> .x.go bytes) to the public
	// Result.Files. Re-use the same key form the caller used in srcOverride:
	// if the caller used relative keys, convert back to relative; otherwise
	// use the absolute path. The key is the .x.go path (not .gsx).
	files := make(map[string][]byte, len(pr.Files))
	for absPath, content := range pr.Files {
		// absPath is an abs .gsx path. Build the .x.go key by swapping the ext.
		base := strings.TrimSuffix(absPath, ".gsx")
		absXGo := base + ".x.go"

		// Prefer the relative key form if the caller used relative keys.
		if len(srcOverride) > 0 {
			// Check whether the caller had a matching relative key.
			rel, relErr := filepath.Rel(filepath.Dir(absDir), absXGo)
			if relErr == nil && !strings.HasPrefix(rel, "..") {
				files[rel] = content
				continue
			}
		}
		files[absXGo] = content
	}

	var allDiags []diag.Diagnostic
	allDiags = append(allDiags, pr.Diags...)

	return Result{
		Files: files,
		Diags: allDiags,
	}, pr.Err
}
