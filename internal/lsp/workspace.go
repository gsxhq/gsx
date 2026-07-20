package lsp

import (
	"cmp"
	"encoding/json"
	"fmt"
	"maps"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"golang.org/x/mod/modfile"
)

type workspaceModuleState struct {
	root       string
	ownerRoot  string
	modulePath string
}

type preparedWorkspace struct {
	folders     []workspaceFolder
	roots       []string
	modules     []string
	owners      map[string]string
	modulePaths map[string]string
}

// discoverWorkspaceModules resolves only the Go modules explicitly owned by
// roots. A go.work at a root owns its use directives. Without one, the nearest
// go.mod at or above the root owns it. Nested modules are never searched for.
func discoverWorkspaceModules(roots []string) ([]string, error) {
	states, err := discoverWorkspaceModuleStates(roots)
	if err != nil {
		return nil, err
	}
	modules := make([]string, len(states))
	for i, state := range states {
		modules[i] = state.root
	}
	return modules, nil
}

// discoverWorkspaceModuleStates retains the exact initialized workspace root
// that declared each Go module and the parsed module directive validated during
// discovery. When several roots declare one module, the deepest root wins;
// equal-depth roots are resolved lexically so input order cannot affect sorting.
func discoverWorkspaceModuleStates(roots []string) ([]workspaceModuleState, error) {
	byModule := make(map[string]workspaceModuleState)
	for _, rawRoot := range roots {
		root, err := normalizeWorkspacePath(rawRoot)
		if err != nil {
			return nil, fmt.Errorf("workspace root %q: %w", rawRoot, err)
		}
		info, err := os.Stat(root)
		if err != nil {
			return nil, fmt.Errorf("workspace root %q: %w", root, err)
		}
		if !info.IsDir() {
			return nil, fmt.Errorf("workspace root %q is not a directory", root)
		}

		workPath := filepath.Join(root, "go.work")
		workSource, err := os.ReadFile(workPath)
		switch {
		case err == nil:
			work, parseErr := modfile.ParseWork(workPath, workSource, nil)
			if parseErr != nil {
				return nil, fmt.Errorf("parse workspace file %q: %w", workPath, parseErr)
			}
			for _, use := range work.Use {
				moduleRoot := use.Path
				if !filepath.IsAbs(moduleRoot) {
					moduleRoot = filepath.Join(root, moduleRoot)
				}
				moduleRoot, err = normalizeWorkspacePath(moduleRoot)
				if err != nil {
					return nil, fmt.Errorf("workspace file %q use %q: %w", workPath, use.Path, err)
				}
				modulePath, err := workspaceModulePath(moduleRoot)
				if err != nil {
					return nil, fmt.Errorf("workspace file %q use %q: %w", workPath, use.Path, err)
				}
				retainWorkspaceModuleState(byModule, workspaceModuleState{root: moduleRoot, ownerRoot: root, modulePath: modulePath})
			}
			continue
		case !os.IsNotExist(err):
			return nil, fmt.Errorf("read workspace file %q: %w", workPath, err)
		}

		moduleRoot, modulePath, found, err := nearestWorkspaceModule(root)
		if err != nil {
			return nil, err
		}
		if found {
			retainWorkspaceModuleState(byModule, workspaceModuleState{root: moduleRoot, ownerRoot: root, modulePath: modulePath})
		}
	}
	states := make([]workspaceModuleState, 0, len(byModule))
	for _, state := range byModule {
		states = append(states, state)
	}
	slices.SortFunc(states, func(a, b workspaceModuleState) int { return cmp.Compare(a.root, b.root) })
	return states, nil
}

func retainWorkspaceModuleState(states map[string]workspaceModuleState, candidate workspaceModuleState) {
	current, exists := states[candidate.root]
	if !exists || workspacePathDepth(candidate.ownerRoot) > workspacePathDepth(current.ownerRoot) ||
		(workspacePathDepth(candidate.ownerRoot) == workspacePathDepth(current.ownerRoot) && candidate.ownerRoot < current.ownerRoot) {
		states[candidate.root] = candidate
	}
}

func workspacePathDepth(path string) int {
	volume := filepath.VolumeName(path)
	rel := strings.TrimPrefix(filepath.Clean(path), volume)
	rel = strings.Trim(rel, string(filepath.Separator))
	if rel == "" {
		return 0
	}
	return len(strings.Split(rel, string(filepath.Separator)))
}

func nearestWorkspaceModule(root string) (string, string, bool, error) {
	for dir := root; ; dir = filepath.Dir(dir) {
		modPath := filepath.Join(dir, "go.mod")
		_, err := os.Stat(modPath)
		switch {
		case err == nil:
			modulePath, err := workspaceModulePath(dir)
			if err != nil {
				return "", "", false, err
			}
			return dir, modulePath, true, nil
		case !os.IsNotExist(err):
			return "", "", false, fmt.Errorf("inspect module file %q: %w", modPath, err)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", "", false, nil
		}
	}
}

func workspaceModulePath(root string) (string, error) {
	info, err := os.Stat(root)
	if err != nil {
		return "", fmt.Errorf("module root %q: %w", root, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("module root %q is not a directory", root)
	}
	modPath := filepath.Join(root, "go.mod")
	modSource, err := os.ReadFile(modPath)
	if err != nil {
		return "", fmt.Errorf("read module file %q: %w", modPath, err)
	}
	parsed, err := modfile.Parse(modPath, modSource, nil)
	if err != nil {
		return "", fmt.Errorf("parse module file %q: %w", modPath, err)
	}
	if parsed.Module == nil {
		return "", fmt.Errorf("parse module file %q: missing module directive", modPath)
	}
	return parsed.Module.Mod.Path, nil
}

func normalizeWorkspacePath(path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("path is empty")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return filepath.Clean(abs), nil
}

func normalizeWorkspaceFolder(folder workspaceFolder) (workspaceFolder, string, error) {
	parsed, err := url.Parse(folder.URI)
	if err != nil {
		return workspaceFolder{}, "", fmt.Errorf("workspace folder URI %q: %w", folder.URI, err)
	}
	hostname := parsed.Hostname()
	if !strings.EqualFold(parsed.Scheme, "file") || parsed.User != nil || parsed.Port() != "" ||
		(parsed.Host != "" && (!strings.EqualFold(hostname, "localhost") || !strings.EqualFold(parsed.Host, hostname))) ||
		parsed.Path == "" {
		return workspaceFolder{}, "", fmt.Errorf("workspace folder URI %q is not a local file URI", folder.URI)
	}
	path, err := normalizeWorkspacePath(parsed.Path)
	if err != nil {
		return workspaceFolder{}, "", fmt.Errorf("workspace folder URI %q: %w", folder.URI, err)
	}
	return workspaceFolder{URI: pathToURI(path), Name: folder.Name}, path, nil
}

func prepareWorkspaceFolders(folders []workspaceFolder) (preparedWorkspace, error) {
	type normalizedFolder struct {
		folder workspaceFolder
		path   string
	}
	normalized := make([]normalizedFolder, 0, len(folders))
	for _, folder := range folders {
		cleanFolder, path, err := normalizeWorkspaceFolder(folder)
		if err != nil {
			return preparedWorkspace{}, err
		}
		normalized = append(normalized, normalizedFolder{folder: cleanFolder, path: path})
	}
	slices.SortFunc(normalized, func(a, b normalizedFolder) int {
		if n := cmp.Compare(a.path, b.path); n != 0 {
			return n
		}
		if a.folder.Name < b.folder.Name {
			return -1
		}
		if a.folder.Name > b.folder.Name {
			return 1
		}
		return 0
	})
	cleanFolders := make([]workspaceFolder, 0, len(normalized))
	roots := make([]string, 0, len(normalized))
	for _, entry := range normalized {
		if len(roots) != 0 && roots[len(roots)-1] == entry.path {
			continue
		}
		cleanFolders = append(cleanFolders, entry.folder)
		roots = append(roots, entry.path)
	}
	modules, err := discoverWorkspaceModuleStates(roots)
	if err != nil {
		return preparedWorkspace{}, err
	}
	prepared := preparedWorkspace{
		folders: cleanFolders, roots: roots, modules: make([]string, len(modules)),
		owners: make(map[string]string, len(modules)), modulePaths: make(map[string]string, len(modules)),
	}
	for i, state := range modules {
		prepared.modules[i] = state.root
		prepared.owners[state.root] = state.ownerRoot
		prepared.modulePaths[state.root] = state.modulePath
	}
	return prepared, nil
}

func (s *Server) setWorkspaceFolders(folders []workspaceFolder) error {
	prepared, err := prepareWorkspaceFolders(folders)
	if err != nil {
		return err
	}
	s.applyPreparedWorkspace(prepared)
	s.workspaceViewValid = true
	return nil
}

func (s *Server) applyPreparedWorkspace(prepared preparedWorkspace) {
	if slices.Equal(s.workspaceFolders, prepared.folders) && slices.Equal(s.workspaceRoots, prepared.roots) && slices.Equal(s.workspaceModules, prepared.modules) &&
		maps.Equal(s.workspaceModuleOwners, prepared.owners) && maps.Equal(s.workspaceModulePaths, prepared.modulePaths) {
		return
	}
	retained := make(map[string]moduleSymbolCache, len(prepared.modules))
	for _, module := range prepared.modules {
		if s.workspaceModulePaths[module] == prepared.modulePaths[module] {
			if cached, ok := s.moduleSyms[module]; ok {
				retained[module] = cached
			}
		}
	}
	s.workspaceFolders = prepared.folders
	s.workspaceRoots = prepared.roots
	s.workspaceModules = prepared.modules
	s.workspaceModuleOwners = prepared.owners
	s.workspaceModulePaths = prepared.modulePaths
	s.moduleSyms = retained
	s.invalidateNonSymbolModuleIndexes()
}

func (s *Server) refreshWorkspaceView() error {
	prepared, err := prepareWorkspaceFolders(s.workspaceFolders)
	if err != nil {
		s.workspaceViewValid = false
		s.invalidateModuleIndexes()
		return err
	}
	s.applyPreparedWorkspace(prepared)
	s.workspaceViewValid = true
	return nil
}

func (s *Server) changeWorkspaceFolders(added, removed []workspaceFolder) error {
	byPath := make(map[string]workspaceFolder, len(s.workspaceFolders)+len(added))
	for _, folder := range s.workspaceFolders {
		cleanFolder, path, err := normalizeWorkspaceFolder(folder)
		if err != nil {
			return err
		}
		byPath[path] = cleanFolder
	}
	for _, folder := range removed {
		_, path, err := normalizeWorkspaceFolder(folder)
		if err != nil {
			return err
		}
		delete(byPath, path)
	}
	for _, folder := range added {
		cleanFolder, path, err := normalizeWorkspaceFolder(folder)
		if err != nil {
			return err
		}
		byPath[path] = cleanFolder
	}
	candidate := make([]workspaceFolder, 0, len(byPath))
	for _, folder := range byPath {
		candidate = append(candidate, folder)
	}
	return s.setWorkspaceFolders(candidate)
}

func (s *Server) handleDidChangeWorkspaceFolders(f frame) error {
	var params didChangeWorkspaceFoldersParams
	if err := json.Unmarshal(f.Params, &params); err != nil {
		return s.logWorkspaceFolderChangeError(fmt.Errorf("decode params: %w", err))
	}
	if err := s.changeWorkspaceFolders(params.Event.Added, params.Event.Removed); err != nil {
		return s.logWorkspaceFolderChangeError(err)
	}
	return nil
}

func (s *Server) logWorkspaceFolderChangeError(err error) error {
	return s.notify("window/logMessage", struct {
		Type    int    `json:"type"`
		Message string `json:"message"`
	}{Type: 1, Message: "gsx: workspace folder change rejected: " + err.Error()})
}
