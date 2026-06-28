package codegen

import (
	"fmt"
	"go/types"

	"golang.org/x/tools/go/packages"
)

// StdImportPath is the gsx built-in filter package. Re-exported from the
// internal stdImportPath constant so the public gen package (and external
// callers such as gsxplayground) can reference it without coupling to the
// internal filters.go symbol.
const StdImportPath = stdImportPath

// CachedResolver uses a prebuilt importer (no subprocess per render).
// Its dependencies are loaded once via packages.Load against moduleDir; each
// per-file check runs entirely in-process via go/types.
//
// Use NewCachedResolver to construct a CachedResolver. The zero value is not
// valid.
type CachedResolver struct {
	imp   types.Importer
	table filterTable
}

// filters returns the prebuilt filterTable so callers can skip a second
// loadFilterTableMulti when using this resolver.
func (c *CachedResolver) filters() filterTable { return c.table }

// importer returns the prebuilt external importer so the Module can type-check
// skeletons against it without packages.Load (bundle mode).
func (c *CachedResolver) importer() types.Importer { return c.imp }

// newCachedResolver loads filterPkgs (plus "github.com/gsxhq/gsx" and
// allowImports) once from moduleDir and returns a resolver whose check method
// runs without any subprocess. The returned resolver's filters() method exposes
// the prebuilt filterTable so callers can skip a second loadFilterTableMulti.
func newCachedResolver(moduleDir string, filterPkgs []string, aliases []FilterAlias, allowImports []string) (*CachedResolver, error) {
	cfg := &packages.Config{
		Mode: packages.NeedName | packages.NeedTypes | packages.NeedImports | packages.NeedDeps,
		Dir:  moduleDir,
	}
	loadPaths := []string{"github.com/gsxhq/gsx"}
	loadPaths = append(loadPaths, filterPkgs...)
	for _, a := range aliases {
		loadPaths = append(loadPaths, a.PkgPath)
	}
	loadPaths = append(loadPaths, allowImports...)
	pkgs, err := packages.Load(cfg, loadPaths...)
	if err != nil {
		return nil, err
	}
	m := map[string]*types.Package{}
	packages.Visit(pkgs, nil, func(p *packages.Package) {
		if p.Types != nil {
			m[p.PkgPath] = p.Types
		}
	})
	table, err := loadFilterTableMulti(moduleDir, filterPkgs, aliases)
	if err != nil {
		return nil, err
	}
	return &CachedResolver{imp: mapImporter(m), table: table}, nil
}

// NewCachedResolver is the public constructor for CachedResolver. It loads
// filterPkgs (plus "github.com/gsxhq/gsx" and allowImports) once from
// moduleDir and returns a resolver ready for in-process generation with no
// per-render subprocess.
func NewCachedResolver(moduleDir string, filterPkgs []string, aliases []FilterAlias, allowImports []string) (*CachedResolver, error) {
	return newCachedResolver(moduleDir, filterPkgs, aliases, allowImports)
}

// mapImporter implements types.Importer using a prebuilt map of package paths
// to *types.Package values.
type mapImporter map[string]*types.Package

func (m mapImporter) Import(path string) (*types.Package, error) {
	if p, ok := m[path]; ok {
		return p, nil
	}
	return nil, fmt.Errorf("cached importer: %q not loaded", path)
}

