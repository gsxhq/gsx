package codegen

import (
	"go/types"
	"slices"
	"strings"

	"github.com/gsxhq/gsx/internal/codegen/stdpath"
)

// SymbolKind is a coarse classification of an exported package-level symbol,
// carried out of the codegen graph to the LSP without leaking a *types.Object.
// The LSP maps it to an LSP CompletionItemKind. Only the categories a package's
// top-level exported scope can hold are represented (no method / package-name /
// builtin — those never appear at package scope).
type SymbolKind int

const (
	SymbolFunc SymbolKind = iota
	SymbolVar
	SymbolConst
	SymbolTypeStruct
	SymbolTypeInterface
	SymbolTypeOther // named non-struct/interface type, alias, or basic type
)

// ExportedSymbol is one exported top-level declaration of a package, described
// by value (name, coarse kind, formatted type/signature) so the caller never
// touches a graph *types.Object outside the analysis lock.
type ExportedSymbol struct {
	Name   string
	Kind   SymbolKind
	Detail string
}

// PackageName is one importable package: its declared name (the qualifier a
// file would use) and its import path.
type PackageName struct {
	Name string
	Path string
}

// PackageExportedSymbols returns the exported top-level declarations of the
// package at importPath — for auto-import completion of an UNIMPORTED qualifier
// (`fmt.▮` where fmt is not yet imported). Like ResolveImportCandidates it is a
// user-triggered slow path (never the Package() hot path) and serializes the
// whole read on analysisMu against one analysis snapshot: candidate names,
// package identities, scopes, and the shared FileSet must all come from the same
// generation.
//
// The symbols come from the LOADED module dependency graph — a COMPLETE
// *types.Package whose scope is populated even though the asking .gsx file does
// not import it (the graph is packages.Load's full transitive closure, a
// superset of what any single file imports) — or, for a std package the graph
// never reached, from cached gc export data (the one expensive branch,
// ~46–78ms cold per distinct package, ~90µs warm thereafter). A main-module
// source package resolves through the source declaration resolver. An
// unknown/unloadable/incomplete path returns nil.
//
// Detail is formatted with every package qualified by its own name (no "current
// package" here, since the asking file does not import this one), matching how
// packageMemberItems renders an imported package's members.
func (m *Module) PackageExportedSymbols(importPath string) []ExportedSymbol {
	if importPath == "" {
		return nil
	}
	m.analysisMu.Lock()
	defer m.analysisMu.Unlock()
	m.maybeRebuildFset()
	m.applyDirty()

	pkg := m.exportSymbolPackage(importPath)
	if pkg == nil || !pkg.Complete() {
		return nil
	}
	scope := pkg.Scope()
	qf := func(p *types.Package) string { return p.Name() }
	var out []ExportedSymbol
	for _, name := range scope.Names() {
		obj := scope.Lookup(name)
		if obj == nil || !obj.Exported() {
			continue
		}
		out = append(out, ExportedSymbol{
			Name:   name,
			Kind:   classifyExportedSymbol(obj),
			Detail: exportedSymbolDetail(obj, qf),
		})
	}
	slices.SortFunc(out, func(a, b ExportedSymbol) int { return strings.Compare(a.Name, b.Name) })
	return out
}

// exportSymbolPackage resolves importPath to a complete *types.Package via the
// same three sources ResolveImportCandidates's packageExports uses, in the same
// precedence: an already-loaded external graph package (free, in-memory), a
// main-module source package (rebuilt through the source declaration resolver),
// or cached gc export data (the stdlib-table-only tail). Called under
// analysisMu.
func (m *Module) exportSymbolPackage(importPath string) *types.Package {
	graph := m.depGraphPackages()
	if pkg, ok := graph[importPath]; ok {
		return pkg
	}
	external, err := m.externalImporter()
	if err == nil {
		m.mu.Lock()
		_, local := m.sourcePackageDirs[importPath]
		m.mu.Unlock()
		if local {
			resolver := newSourceDeclResolver(m, external)
			pkg, err := resolver.Import(importPath)
			if err != nil {
				return nil
			}
			return pkg
		}
	}
	pkg, err := m.importExportData(importPath)
	if err != nil {
		return nil
	}
	return pkg
}

// classifyExportedSymbol maps a package-level exported object to a coarse
// SymbolKind. It mirrors the LSP's goObjectPresentation kind logic for the
// object categories that can appear at package scope.
func classifyExportedSymbol(obj types.Object) SymbolKind {
	switch o := obj.(type) {
	case *types.Func:
		return SymbolFunc
	case *types.Var:
		return SymbolVar
	case *types.Const:
		return SymbolConst
	case *types.TypeName:
		switch types.Unalias(o.Type()).Underlying().(type) {
		case *types.Struct:
			return SymbolTypeStruct
		case *types.Interface:
			return SymbolTypeInterface
		default:
			return SymbolTypeOther
		}
	default:
		return SymbolVar
	}
}

// exportedSymbolDetail formats an object's type/signature string, mirroring the
// LSP's goObjectPresentation detail: a func/const/type shows its full object
// string, a var shows just its type.
func exportedSymbolDetail(obj types.Object, qf types.Qualifier) string {
	switch o := obj.(type) {
	case *types.Var:
		return types.TypeString(o.Type(), qf)
	default:
		return types.ObjectString(obj, qf)
	}
}

// ImportablePackageNames returns every package that dir could import: its
// declared name and import path, from the loaded dependency graph and the baked
// stdlib table, filtered by Go's internal-visibility rule (stdpath.InternalVisible)
// for dir and excluding dir's own package (a self-import would be invalid Go).
//
// User-triggered slow path (auto-import package-name completion), serialized on
// analysisMu like ResolveImportCandidates. All lookups, never a filesystem scan.
// The result may be large (~1000 for a real module); the caller prefix-filters.
func (m *Module) ImportablePackageNames(dir string) []PackageName {
	m.analysisMu.Lock()
	defer m.analysisMu.Unlock()
	m.maybeRebuildFset()
	m.applyDirty()

	importerPath, _ := importPathForDir(m.opts.ModuleRoot, m.opts.ModulePath, dir)
	graph := m.depGraphPackages()
	names := m.importCandidatePackageNames(graph)

	seen := map[string]bool{}
	var out []PackageName
	add := func(path, name string) {
		if name == "" || path == "" || path == importerPath || seen[path] {
			return
		}
		if !stdpath.InternalVisible(path, importerPath) {
			return
		}
		seen[path] = true
		out = append(out, PackageName{Name: name, Path: path})
	}
	for path, name := range names {
		add(path, name)
	}
	for name, paths := range stdlibIndex {
		for _, path := range paths {
			add(path, name)
		}
	}
	slices.SortFunc(out, func(a, b PackageName) int {
		if a.Name != b.Name {
			return strings.Compare(a.Name, b.Name)
		}
		return strings.Compare(a.Path, b.Path)
	})
	return out
}
