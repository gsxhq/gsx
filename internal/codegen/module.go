package codegen

import (
	"go/types"
	"os"
	"sync"

	"golang.org/x/tools/go/packages"

	"github.com/gsxhq/gsx/internal/attrclass"
)

// Options configures a Module. ModuleRoot is the absolute module root (dir
// containing go.mod); ModulePath is its declared module path (from go.mod).
type Options struct {
	ModuleRoot   string
	ModulePath   string
	FilterPkgs   []string
	Aliases      []FilterAlias
	FieldMatcher FieldMatcher
	Classifier   *attrclass.Classifier
}

// Module is a warm, in-process analysis graph for one module root. It is the
// single analysis core consumed by generate, watch, the LSP, fmt, and the
// playground. Not safe for concurrent mutation; callers serialize edits.
type Module struct {
	opts      Options
	overrides map[string][]byte        // abs .gsx path -> in-memory source
	ext       types.Importer           // lazily built external importer (stdlib + third-party)
	pkgTypes  map[string]*types.Package // abs dir -> checked *types.Package cache
	mu        sync.Mutex
}

// Open constructs a Module. It does not load anything yet; analysis is lazy.
func Open(opts Options) (*Module, error) {
	cls := opts.Classifier
	if cls == nil {
		cls = attrclass.Builtin()
		opts.Classifier = cls
	}
	return &Module{opts: opts, overrides: map[string][]byte{}}, nil
}

// SetOverride records in-memory source for a .gsx path (an unsaved editor buffer
// or playground source), shadowing disk content.
func (m *Module) SetOverride(absPath string, src []byte) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.overrides[absPath] = src
}

// source returns the bytes for absPath: override first, else disk.
func (m *Module) source(absPath string) ([]byte, bool) {
	m.mu.Lock()
	ov, ok := m.overrides[absPath]
	m.mu.Unlock()
	if ok {
		return ov, true
	}
	b, err := os.ReadFile(absPath)
	if err != nil {
		return nil, false
	}
	return b, true
}

// externalImporter lazily loads non-project dependency types once (stdlib,
// third-party, .go-only packages) and caches them. Project gsx packages never
// reach it (moduleImporter routes those to typesPackage).
func (m *Module) externalImporter() (types.Importer, error) {
	m.mu.Lock()
	if m.ext != nil {
		defer m.mu.Unlock()
		return m.ext, nil
	}
	m.mu.Unlock()
	cfg := &packages.Config{
		Mode: packages.NeedName | packages.NeedTypes | packages.NeedImports | packages.NeedDeps,
		Dir:  m.opts.ModuleRoot,
	}
	loadPaths := append([]string{stdImportPath}, m.opts.FilterPkgs...)
	loadPaths = append(loadPaths, "./...")
	pkgs, err := packages.Load(cfg, loadPaths...)
	if err != nil {
		return nil, err
	}
	mp := map[string]*types.Package{}
	packages.Visit(pkgs, nil, func(p *packages.Package) {
		if p.Types != nil {
			mp[p.PkgPath] = p.Types
		}
	})
	m.mu.Lock()
	m.ext = mapImporter(mp)
	m.mu.Unlock()
	return m.ext, nil
}
