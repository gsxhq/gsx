package codegen

import (
	"fmt"
	goast "go/ast"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	gsxparser "github.com/gsxhq/gsx/parser"
	"golang.org/x/tools/go/packages"
)

// projectSourcePackage is the Go command's active compiled-file selection for
// one package in the module. The inventory is retained from the Module's one
// cold packages.Load, whose Config uses the Module FileSet; exact component
// analysis type-checks those retained ASTs through its phase-specific importer.
type projectSourcePackage struct {
	pkgPath         string
	name            string
	compiledGoFiles []string
	syntaxByFile    map[string]*goast.File
	metadataErrors  []packages.Error
	invariantErrors []string
}

type sourceInventoryManifest struct {
	overlay       map[string][]byte
	loadPaths     []string
	sentinelFiles map[string]bool
}

type gsxSourceInventoryFact struct {
	present     bool
	packageName string
	importsKey  string
}

func inspectGsxSourceInventory(path string, source []byte, present bool) (gsxSourceInventoryFact, []string) {
	fact := gsxSourceInventoryFact{present: present}
	if !present {
		return fact, nil
	}
	fset := token.NewFileSet()
	file, parseErr := gsxparser.ParseFile(fset, path, source, 0)
	if file == nil || file.Package == "" {
		return fact, nil
	}
	fact.packageName = file.Package
	if parseErr != nil {
		return fact, nil
	}
	imports, _, splitErr := splitFileGoSource(file, fset)
	if splitErr != nil {
		return fact, nil
	}
	unique := map[string]bool{}
	for _, spec := range imports {
		if spec.path != "C" {
			unique[spec.path] = true
		}
	}
	paths := make([]string, 0, len(unique))
	for importPath := range unique {
		paths = append(paths, importPath)
	}
	sort.Strings(paths)
	fact.importsKey = strings.Join(paths, "\x00")
	return fact, paths
}

// buildSourceInventoryManifest describes the authoritative source surface that
// the one cold packages.Load must classify. Paired generated outputs are
// deletion-overlaid, while one synthetic, declaration-free Go file keeps each
// GSX package structurally present even when it has no active companion files.
// Exact package patterns cover directories omitted by ./... (for example
// testdata and underscore-prefixed trees), and authored GSX imports preserve
// dependency discovery that deleting generated outputs would otherwise erase.
func (m *Module) buildSourceInventoryManifest() (sourceInventoryManifest, error) {
	root := filepath.Clean(m.opts.ModuleRoot)
	gsxPaths := map[string]bool{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() && path != root {
			if entry.Name() == "vendor" {
				return filepath.SkipDir
			}
			if nested, err := directoryStartsModule(path); err != nil {
				return err
			} else if nested {
				return filepath.SkipDir
			}
		}
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".gsx") {
			gsxPaths[filepath.Clean(path)] = true
		}
		return nil
	})
	if err != nil {
		return sourceInventoryManifest{}, fmt.Errorf("codegen: discover authoritative GSX sources: %w", err)
	}

	m.mu.Lock()
	overridePaths := make([]string, 0, len(m.overrides))
	for path := range m.overrides {
		overridePaths = append(overridePaths, path)
	}
	m.mu.Unlock()
	for _, path := range overridePaths {
		clean := filepath.Clean(path)
		if !strings.HasSuffix(clean, ".gsx") || !pathWithin(root, clean) {
			continue
		}
		owned, ownershipErr := moduleOwnsPath(root, clean)
		if ownershipErr != nil {
			return sourceInventoryManifest{}, ownershipErr
		}
		if owned {
			gsxPaths[clean] = true
		}
	}

	manifest := sourceInventoryManifest{
		overlay:       map[string][]byte{},
		sentinelFiles: map[string]bool{},
	}
	paths := make([]string, 0, len(gsxPaths))
	for path := range gsxPaths {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	packageNames := map[string]string{}
	loadPaths := map[string]bool{}
	for _, gsxPath := range paths {
		paired := strings.TrimSuffix(gsxPath, ".gsx") + ".x.go"
		info, statErr := os.Stat(paired)
		switch {
		case statErr == nil && info.Mode().IsRegular():
			manifest.overlay[paired] = []byte("//go:build gsxpaired && !gsxpaired\n\npackage gsxpaired\n")
		case statErr != nil && !os.IsNotExist(statErr):
			return sourceInventoryManifest{}, fmt.Errorf("codegen: inspect paired generated output %s: %w", paired, statErr)
		}

		source, ok := m.source(gsxPath)
		if !ok {
			return sourceInventoryManifest{}, fmt.Errorf("codegen: read authoritative GSX source %s", gsxPath)
		}
		fact, imports := inspectGsxSourceInventory(gsxPath, source, true)
		if fact.packageName == "" {
			continue
		}
		dir := filepath.Dir(gsxPath)
		if packageNames[dir] == "" {
			packageNames[dir] = fact.packageName
		}
		if packagePath, ok := importPathForDir(root, m.opts.ModulePath, dir); ok {
			loadPaths[packagePath] = true
		}
		for _, importPath := range imports {
			loadPaths[importPath] = true
		}
	}
	dirs := make([]string, 0, len(packageNames))
	for dir := range packageNames {
		dirs = append(dirs, dir)
	}
	sort.Strings(dirs)
	for _, dir := range dirs {
		sentinel, err := sourceInventorySentinelPath(dir, manifest.overlay)
		if err != nil {
			return sourceInventoryManifest{}, err
		}
		manifest.overlay[sentinel] = []byte("package " + packageNames[dir] + "\n")
		manifest.sentinelFiles[sentinel] = true
	}
	manifest.loadPaths = make([]string, 0, len(loadPaths))
	for path := range loadPaths {
		manifest.loadPaths = append(manifest.loadPaths, path)
	}
	sort.Strings(manifest.loadPaths)
	return manifest, nil
}

func sourceInventorySentinelPath(dir string, overlay map[string][]byte) (string, error) {
	for index := 0; ; index++ {
		path := filepath.Join(dir, fmt.Sprintf("zz_gsx_source_inventory_%d.go", index))
		if _, occupied := overlay[path]; occupied {
			continue
		}
		_, err := os.Lstat(path)
		if os.IsNotExist(err) {
			return path, nil
		}
		if err != nil {
			return "", fmt.Errorf("codegen: inspect source-inventory sentinel %s: %w", path, err)
		}
	}
}

func directoryStartsModule(dir string) (bool, error) {
	info, err := os.Stat(filepath.Join(dir, "go.mod"))
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("codegen: inspect module boundary in %s: %w", dir, err)
	}
	return info.Mode().IsRegular(), nil
}

// moduleOwnsPath reports whether path belongs to the module rooted at root.
// A nested go.mod and any vendor segment are Go module ownership boundaries;
// overlays must never mutate package selection beyond either boundary.
func moduleOwnsPath(root, path string) (bool, error) {
	root = filepath.Clean(root)
	path = filepath.Clean(path)
	if !pathWithin(root, path) {
		return false, nil
	}
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false, err
	}
	for part := range strings.SplitSeq(rel, string(filepath.Separator)) {
		if part == "vendor" {
			return false, nil
		}
	}
	for dir := filepath.Dir(path); dir != root; dir = filepath.Dir(dir) {
		nested, err := directoryStartsModule(dir)
		if err != nil {
			return false, err
		}
		if nested {
			return false, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return false, fmt.Errorf("codegen: path %s escaped module root %s while checking ownership", path, root)
		}
	}
	return true, nil
}

func pathWithin(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func projectSourcePackages(loaded []*packages.Package, moduleRoot, modulePath string, sentinelFiles map[string]bool) map[string]projectSourcePackage {
	byDir := map[string]projectSourcePackage{}
	packages.Visit(loaded, nil, func(pkg *packages.Package) {
		if pkg == nil || pkg.Dir == "" {
			return
		}
		dir := filepath.Clean(pkg.Dir)
		expectedPath, ok := importPathForDir(moduleRoot, modulePath, dir)
		if !ok || expectedPath != pkg.PkgPath {
			return
		}
		files := make([]string, 0, len(pkg.CompiledGoFiles))
		for _, path := range pkg.CompiledGoFiles {
			path = filepath.Clean(path)
			if !sentinelFiles[path] {
				files = append(files, path)
			}
		}
		sort.Strings(files)
		metadataErrors := make([]packages.Error, 0, len(pkg.Errors))
		for _, loadErr := range pkg.Errors {
			if loadErr.Kind != packages.TypeError {
				metadataErrors = append(metadataErrors, loadErr)
			}
		}
		syntaxByFile := make(map[string]*goast.File, len(pkg.Syntax))
		for _, file := range pkg.Syntax {
			if file == nil || pkg.Fset == nil || pkg.Fset.File(file.Pos()) == nil {
				continue
			}
			path := filepath.Clean(pkg.Fset.File(file.Pos()).Name())
			if !sentinelFiles[path] {
				syntaxByFile[path] = file
			}
		}
		var invariantErrors []string
		if len(metadataErrors) == 0 {
			for _, path := range files {
				if syntaxByFile[path] == nil {
					invariantErrors = append(invariantErrors, "loaded syntax is missing for "+path)
				}
			}
		}
		byDir[dir] = projectSourcePackage{
			pkgPath:         pkg.PkgPath,
			name:            pkg.Name,
			compiledGoFiles: files,
			syntaxByFile:    syntaxByFile,
			metadataErrors:  metadataErrors,
			invariantErrors: invariantErrors,
		}
	})
	return byDir
}

func (m *Module) targetSourcePackage(dir string) (projectSourcePackage, bool, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	pkg, ok := m.sourcePackages[filepath.Clean(dir)]
	return pkg, ok, m.sourceInventoryReady
}

// exactTargetPackageDir resolves module-local ownership from the authoritative
// cold index. Bundle mode has no inventory, so it retains the explicitly
// bounded single-GSX-package filesystem path.
func (m *Module) exactTargetPackageDir(importPath string) (string, bool) {
	m.mu.Lock()
	dir, found := m.sourcePackageDirs[importPath]
	ready := m.sourceInventoryReady
	m.mu.Unlock()
	if ready {
		return dir, found
	}
	dir, found = dirForImportPath(m.opts.ModuleRoot, m.opts.ModulePath, importPath)
	return dir, found && m.isGsxPackage(dir)
}
