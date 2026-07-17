package codegen

import (
	"fmt"
	"go/types"
	goversion "go/version"
	"os"
	"path/filepath"

	"golang.org/x/mod/modfile"
	"golang.org/x/tools/go/packages"
)

// StdImportPath is the gsx built-in filter package. Re-exported from the
// internal stdImportPath constant so the public gen package (and external
// callers such as gsxplayground) can reference it without coupling to the
// internal filters.go symbol.
const StdImportPath = stdImportPath

// Bundle carries a prebuilt external importer and filter table so the Module can
// type-check skeletons with no `go list`/packages.Load. A WASM build (browser,
// no toolchain) constructs a Bundle once via NewCachedResolver/NewCachedResolverFromTypes
// and injects it through Options.Bundle. Passive data — it resolves nothing itself.
// The zero value is invalid.
type Bundle struct {
	imp       types.Importer
	table     funcTables
	sizes     types.Sizes
	goVersion string
}

// tables returns the prebuilt funcTables (filters + renderers) so callers can
// skip a second loadFilterTableMulti/loadFilterTableFromTypes when using this
// resolver.
func (b *Bundle) tables() funcTables { return b.table }

// importer returns the prebuilt external importer so the Module can type-check
// skeletons against it without packages.Load (bundle mode).
func (b *Bundle) importer() types.Importer { return b.imp }

// newCachedResolver loads filterPkgs (plus "github.com/gsxhq/gsx" and
// allowImports) once from moduleDir and returns a resolver whose check method
// runs without any subprocess. The returned resolver's tables() method exposes
// the prebuilt funcTables so callers can skip a second loadFilterTableMulti.
func newCachedResolver(moduleDir string, filterPkgs []string, aliases []FilterAlias, allowImports []string) (*Bundle, error) {
	filterPkgs = dedupFilterPkgs(filterPkgs)
	goVersion, err := moduleLanguageVersion(moduleDir)
	if err != nil {
		return nil, err
	}
	if !goversion.IsValid(goVersion) {
		return nil, fmt.Errorf("codegen: cached resolver module has invalid Go language version %q", goVersion)
	}
	cfg := &packages.Config{
		Mode: packages.NeedName | packages.NeedTypes | packages.NeedTypesSizes | packages.NeedImports | packages.NeedDeps,
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
	loadedByPath := make(map[string]*packages.Package, len(pkgs))
	for _, pkg := range pkgs {
		loadedByPath[pkg.PkgPath] = pkg
	}
	if err := checkLoadedPkg(loadedByPath["github.com/gsxhq/gsx"], "cached resolver runtime package \"github.com/gsxhq/gsx\"", moduleDir); err != nil {
		return nil, err
	}
	for _, path := range filterPkgs {
		if err := checkFilterPkg(loadedByPath[path], path, moduleDir, ""); err != nil {
			return nil, err
		}
	}
	for _, alias := range aliases {
		if err := checkFilterPkg(loadedByPath[alias.PkgPath], alias.PkgPath, moduleDir, alias.Name); err != nil {
			return nil, err
		}
	}
	for _, path := range allowImports {
		if err := checkLoadedPkg(loadedByPath[path], fmt.Sprintf("cached resolver allowed import %q", path), moduleDir); err != nil {
			return nil, err
		}
	}
	m := map[string]*types.Package{}
	var sizes types.Sizes
	packages.Visit(pkgs, nil, func(p *packages.Package) {
		if p.Types != nil {
			m[p.PkgPath] = p.Types
		}
		if sizes == nil && p.TypesSizes != nil {
			sizes = p.TypesSizes
		}
	})
	if sizes == nil {
		return nil, fmt.Errorf("codegen: cached resolver load returned no target type sizes")
	}
	if goVersion == "" {
		return nil, fmt.Errorf("codegen: cached resolver load returned no Go language version for %s", moduleDir)
	}
	table, rt, err := loadFilterTableFromTypes(m, filterPkgs, aliases, nil)
	if err != nil {
		return nil, err
	}
	return &Bundle{imp: mapImporter(m), table: funcTables{filters: table, renderers: rt}, sizes: sizes, goVersion: goVersion}, nil
}

func moduleLanguageVersion(moduleDir string) (string, error) {
	path := filepath.Join(moduleDir, "go.mod")
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("codegen: read cached-resolver module language version from %s: %w", path, err)
	}
	file, err := modfile.Parse(path, data, nil)
	if err != nil {
		return "", fmt.Errorf("codegen: parse cached-resolver module language version from %s: %w", path, err)
	}
	if file.Go == nil || file.Go.Version == "" {
		return "go1.16", nil
	}
	return "go" + file.Go.Version, nil
}

// NewCachedResolver is the public constructor for Bundle. It loads
// filterPkgs (plus "github.com/gsxhq/gsx" and allowImports) once from
// moduleDir and returns a Bundle ready for in-process generation with no
// per-render subprocess.
func NewCachedResolver(moduleDir string, filterPkgs []string, aliases []FilterAlias, allowImports []string) (*Bundle, error) {
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
