package lsp

import (
	"cmp"
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
)

type moduleSymbolCache struct {
	symbols []Symbol
	valid   bool
}

type workspaceSymbolResult struct {
	info       SymbolInformation
	name       string
	sourcePath string
	start      int
	kind       int
	module     string
	packageID  string
}

// handleWorkspaceSymbol returns symbols from every initialized Go module whose
// name contains the query (case-insensitive substring; empty returns all).
// Each module is cached independently and every location is encoded from one
// immutable authoritative-source snapshot for this request.
func (s *Server) handleWorkspaceSymbol(f frame) error {
	sources := s.sourceSnapshot()
	var p workspaceSymbolParams
	if err := json.Unmarshal(f.Params, &p); err != nil {
		return s.reply(f.ID, []SymbolInformation{})
	}
	if !s.diskViewValid {
		return s.reply(f.ID, []SymbolInformation{})
	}
	if s.moduleSyms == nil {
		s.moduleSyms = make(map[string]moduleSymbolCache)
	}

	all := make([]workspaceSymbolResult, 0)
	ambiguousPackages := make(map[string]map[string]struct{})
	q := strings.ToLower(p.Query)
	for _, module := range s.workspaceModules {
		cached := s.moduleSyms[module]
		if !cached.valid {
			syms, err := s.analyzer.ModuleSymbols(module, sources.openGSXOverridesForModule(module, s.workspaceModules))
			if err != nil {
				return s.reply(f.ID, []SymbolInformation{})
			}
			cached = moduleSymbolCache{symbols: slices.Clone(syms), valid: true}
			s.moduleSyms[module] = cached
		}
		for _, sym := range cached.symbols {
			span, ok := authoredSpanForPosition(sym.NamePos, len(sym.Name))
			if !ok || workspaceModuleForPath(s.workspaceModules, span.Path) != module {
				continue
			}
			if sym.Kind != symKindMethod {
				modules := ambiguousPackages[sym.Container]
				if modules == nil {
					modules = make(map[string]struct{})
					ambiguousPackages[sym.Container] = modules
				}
				modules[module] = struct{}{}
			}
			if q != "" && !strings.Contains(strings.ToLower(sym.Name), q) {
				continue
			}
			location, ok := sources.locationForSpan(span)
			if !ok {
				continue
			}
			all = append(all, workspaceSymbolResult{
				info: SymbolInformation{
					Name: sym.Name, Kind: sym.Kind, ContainerName: sym.Container, Location: location,
				},
				name:       sym.Name,
				sourcePath: s.workspaceRelativeSourcePath(module, span.Path),
				start:      span.Start,
				kind:       sym.Kind,
				module:     module,
				packageID:  sym.Container,
			})
		}
	}

	for i := range all {
		if all[i].kind != symKindMethod && len(ambiguousPackages[all[i].packageID]) > 1 {
			all[i].info.ContainerName = s.modulePackagePath(all[i].module, uriToPath(all[i].info.Location.URI))
		}
	}

	slices.SortStableFunc(all, func(a, b workspaceSymbolResult) int {
		if n := cmp.Compare(a.name, b.name); n != 0 {
			return n
		}
		if n := cmp.Compare(a.sourcePath, b.sourcePath); n != 0 {
			return n
		}
		if n := cmp.Compare(a.start, b.start); n != 0 {
			return n
		}
		return cmp.Compare(a.kind, b.kind)
	})
	out := make([]SymbolInformation, len(all))
	for i := range all {
		out[i] = all[i].info
	}
	return s.reply(f.ID, out)
}

func (snapshot *requestSourceSnapshot) openGSXOverridesForModule(module string, modules []string) map[string][]byte {
	overrides := make(map[string][]byte)
	for path, source := range snapshot.sources {
		if !source.open || !strings.HasSuffix(path, ".gsx") || workspaceModuleForPath(modules, path) != module {
			continue
		}
		if text, ok := snapshot.sourceText(path); ok {
			overrides[path] = text
		}
	}
	return overrides
}

// workspaceModuleForPath uses filesystem path boundaries, then chooses the
// deepest containing initialized module. This makes nested go.work use modules
// authoritative without admitting prefix siblings such as module-other.
func workspaceModuleForPath(modules []string, path string) string {
	path = sourcePath(path)
	best := ""
	for _, module := range modules {
		module = sourcePath(module)
		if !pathWithin(module, path) {
			continue
		}
		if best == "" || pathWithin(best, module) {
			best = module
		}
	}
	return best
}

func pathWithin(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil || filepath.IsAbs(rel) {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func (s *Server) workspaceRelativeSourcePath(module, path string) string {
	owner := s.workspaceModuleOwners[module]
	if owner == "" {
		owner = module
	}
	rel, err := filepath.Rel(owner, sourcePath(path))
	if err != nil {
		return filepath.ToSlash(sourcePath(path))
	}
	return filepath.ToSlash(rel)
}

func (s *Server) modulePackagePath(module, sourcePath string) string {
	modulePath := s.workspaceModulePaths[module]
	dir := filepath.Dir(sourcePath)
	rel, err := filepath.Rel(module, dir)
	if modulePath == "" || err != nil || !pathWithin(module, dir) {
		return filepath.ToSlash(rel)
	}
	if rel == "." {
		return modulePath
	}
	return strings.TrimSuffix(modulePath, "/") + "/" + filepath.ToSlash(rel)
}
