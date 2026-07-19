package sourceview

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

var ErrUncacheableCgo = errors.New("sourceview: mutable cgo package is not representable by persistent source projection")

// ModuleMetadata is the cmd/go module provenance attached to PackageMetadata.
// Its fields are exported because encoding/json fills the `go list -json`
// schema directly.
type ModuleMetadata struct {
	Path      string
	Version   string
	Dir       string
	GoMod     string
	GoVersion string
	Main      bool
	Replace   *ModuleMetadata
}

// PackageMetadata is the exact active package selection reported by cmd/go.
// Its fields are exported because encoding/json fills the `go list -json`
// schema directly.
type PackageMetadata struct {
	ImportPath      string
	Dir             string
	Imports         []string
	Deps            []string
	GoFiles         []string
	CgoFiles        []string
	CompiledGoFiles []string
	Standard        bool
	Module          *ModuleMetadata
}

// Graph is an import-path-indexed cmd/go package selection.
type Graph map[string]PackageMetadata

// DecodeGraph decodes the concatenated JSON objects emitted by `go list
// -deps -json -compiled`.
func DecodeGraph(reader io.Reader) (Graph, error) {
	graph := make(Graph)
	decoder := json.NewDecoder(reader)
	for {
		var metadata PackageMetadata
		err := decoder.Decode(&metadata)
		if err == io.EOF {
			return graph, nil
		}
		if err != nil {
			return nil, fmt.Errorf("sourceview: decode Go package graph: %w", err)
		}
		if metadata.ImportPath == "" {
			return nil, fmt.Errorf("sourceview: Go package graph contains an empty import path")
		}
		graph[metadata.ImportPath] = metadata
	}
}

type cacheInput struct {
	label       string
	logicalPath string
	digest      [sha256.Size]byte
	present     bool
}

type moduleProjection struct {
	inputs []cacheInput
	err    error
}

// CacheProjection is an immutable source-identity projection of one Manifest
// enriched by cmd/go's active Go-file and dependency selection.
type CacheProjection struct {
	manifest          *Manifest
	graph             Graph
	byDir             map[string]string
	packageInputs     map[string][]cacheInput
	packageInputErrs  map[string]error
	mutableModules    map[string]moduleProjection
	packageModuleKeys map[string]string
	mainInputs        []cacheInput
}

// NewCacheProjection joins the shared GSX manifest to cmd/go's selected graph.
// It validates every selected graph package claiming manifest ownership before
// any cache key can be computed, preventing cache metadata from describing a
// different package view than packages.Load.
func NewCacheProjection(manifest *Manifest, graph Graph) (*CacheProjection, error) {
	if manifest == nil {
		return nil, fmt.Errorf("sourceview: nil manifest")
	}
	projection := &CacheProjection{
		manifest:          manifest,
		graph:             cloneGraph(graph),
		byDir:             make(map[string]string),
		packageInputs:     make(map[string][]cacheInput),
		packageInputErrs:  make(map[string]error),
		mutableModules:    make(map[string]moduleProjection),
		packageModuleKeys: make(map[string]string),
	}
	for importPath, metadata := range projection.graph {
		if metadata.ImportPath == "" {
			metadata.ImportPath = importPath
			projection.graph[importPath] = metadata
		}
		if metadata.Dir != "" {
			projection.byDir[canonicalPath(metadata.Dir)] = importPath
		}
	}
	for _, metadata := range projection.graph {
		dir, owned := manifest.packageDirs[metadata.ImportPath]
		if !owned {
			continue
		}
		importPath := metadata.ImportPath
		if canonicalPath(metadata.Dir) != canonicalPath(dir) {
			return nil, fmt.Errorf("sourceview: selected package %q has dir %q, want manifest dir %q", importPath, metadata.Dir, dir)
		}
		if !isMainModule(metadata.Module, manifest) {
			return nil, fmt.Errorf("sourceview: selected package %q is not owned by main module %q", importPath, manifest.modulePath)
		}
	}

	var err error
	projection.mainInputs, err = moduleProvenanceInputs("main:"+manifest.modulePath, manifest.moduleRoot, filepath.Join(manifest.moduleRoot, "go.mod"), true)
	if err != nil {
		return nil, err
	}
	for importPath, metadata := range projection.graph {
		inputs, inputErr := projection.inputsForPackage(metadata)
		if inputErr != nil {
			projection.packageInputErrs[importPath] = inputErr
		} else {
			projection.packageInputs[importPath] = inputs
		}
		module, mutable := mutableModule(metadata.Module)
		if !mutable {
			continue
		}
		moduleKey := moduleIdentityKey(metadata.Module, module)
		projection.packageModuleKeys[importPath] = moduleKey
		if _, done := projection.mutableModules[moduleKey]; done {
			continue
		}
		goMod := module.GoMod
		if goMod == "" {
			goMod = filepath.Join(module.Dir, "go.mod")
		}
		moduleInputs, moduleErr := moduleProvenanceInputs("replace:"+moduleKey, module.Dir, goMod, true)
		if moduleErr == nil {
			descriptor := strings.Join([]string{
				"path=" + metadata.Module.Path,
				"version=" + metadata.Module.Version,
				"replace-path=" + module.Path,
				"replace-version=" + module.Version,
			}, "\x00")
			moduleInputs = append(moduleInputs, inputFromBytes("replace:"+moduleKey+":descriptor", "", []byte(descriptor)))
		}
		projection.mutableModules[moduleKey] = moduleProjection{inputs: moduleInputs, err: moduleErr}
	}
	return projection, nil
}

// Digest returns one deterministic source identity for dir plus every package
// reachable from its selected Go imports, authored GSX imports, and extraRoots.
func (projection *CacheProjection) Digest(dir string, extraRoots []string) (string, error) {
	inputs, err := projection.inputs(dir, extraRoots)
	if err != nil {
		return "", err
	}
	hash := sha256.New()
	hash.Write([]byte("gsx-sourceview-cache-v1\x00"))
	for _, input := range inputs {
		fmt.Fprintf(hash, "%d:%s\x00", len(input.label), input.label)
		if input.present {
			hash.Write([]byte{1})
			hash.Write(input.digest[:])
		} else {
			hash.Write([]byte{0})
		}
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

// LogicalFiles returns the sorted logical source/provenance files represented
// by Digest. Transport-only cmd/go overlay backing paths are never returned.
func (projection *CacheProjection) LogicalFiles(dir string, extraRoots []string) ([]string, error) {
	inputs, err := projection.inputs(dir, extraRoots)
	if err != nil {
		return nil, err
	}
	set := make(map[string]bool)
	for _, input := range inputs {
		if input.present && input.logicalPath != "" {
			set[input.logicalPath] = true
		}
	}
	paths := make([]string, 0, len(set))
	for path := range set {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths, nil
}

func (projection *CacheProjection) inputs(dir string, extraRoots []string) ([]cacheInput, error) {
	dir = canonicalPath(dir)
	rootPath, ok := projection.byDir[dir]
	if !ok {
		return nil, fmt.Errorf("sourceview: target directory %s is absent from selected package graph", dir)
	}
	roots := append([]string{rootPath}, extraRoots...)
	queue := dedupSortedStrings(roots)
	seen := make(map[string]bool)
	var packagePaths []string
	for len(queue) != 0 {
		path := queue[0]
		queue = queue[1:]
		if path == "" || path == "C" || seen[path] {
			continue
		}
		metadata, inGraph := projection.graph[path]
		if !inGraph {
			return nil, fmt.Errorf("sourceview: reachable package %q is absent from selected Go graph", path)
		}
		seen[path] = true
		packagePaths = append(packagePaths, path)
		queue = append(queue, metadata.Imports...)
		if sourceDir, exists := projection.manifest.packageDirs[path]; exists {
			queue = append(queue, projection.manifest.importsByDir[sourceDir]...)
		}
	}
	sort.Strings(packagePaths)
	inputsByLabel := make(map[string]cacheInput)
	add := func(input cacheInput) error {
		if old, exists := inputsByLabel[input.label]; exists && old != input {
			return fmt.Errorf("sourceview: conflicting cache inputs for %q", input.label)
		}
		inputsByLabel[input.label] = input
		return nil
	}
	for _, input := range projection.mainInputs {
		if err := add(input); err != nil {
			return nil, err
		}
	}
	seenModules := make(map[string]bool)
	for _, path := range packagePaths {
		if err := projection.packageInputErrs[path]; err != nil {
			return nil, err
		}
		for _, input := range projection.packageInputs[path] {
			if err := add(input); err != nil {
				return nil, err
			}
		}
		moduleKey := projection.packageModuleKeys[path]
		if moduleKey == "" || seenModules[moduleKey] {
			continue
		}
		seenModules[moduleKey] = true
		module := projection.mutableModules[moduleKey]
		if module.err != nil {
			return nil, module.err
		}
		for _, input := range module.inputs {
			if err := add(input); err != nil {
				return nil, err
			}
		}
	}
	labels := make([]string, 0, len(inputsByLabel))
	for label := range inputsByLabel {
		labels = append(labels, label)
	}
	sort.Strings(labels)
	inputs := make([]cacheInput, 0, len(labels))
	for _, label := range labels {
		inputs = append(inputs, inputsByLabel[label])
	}
	return inputs, nil
}

func (projection *CacheProjection) inputsForPackage(metadata PackageMetadata) ([]cacheInput, error) {
	mainOwned := isMainModule(metadata.Module, projection.manifest)
	_, mutable := mutableModule(metadata.Module)
	if metadata.Standard || !mainOwned && !mutable {
		return nil, nil
	}
	if len(metadata.CgoFiles) != 0 {
		return nil, fmt.Errorf("%w: %q", ErrUncacheableCgo, metadata.ImportPath)
	}
	selected := metadata.CompiledGoFiles
	if len(selected) == 0 {
		selected = metadata.GoFiles
	}
	paired := make(map[string]bool, len(projection.manifest.pairedOutputs))
	for _, path := range projection.manifest.pairedOutputs {
		paired[canonicalPath(path)] = true
	}
	sentinels := make(map[string]bool, len(projection.manifest.sentinelFiles))
	for _, path := range projection.manifest.sentinelFiles {
		sentinels[canonicalPath(path)] = true
	}
	paths := make(map[string]bool)
	for _, path := range selected {
		if !filepath.IsAbs(path) {
			path = filepath.Join(metadata.Dir, path)
		}
		path = filepath.Clean(path)
		canonical := canonicalPath(path)
		if mainOwned && (paired[canonical] || sentinels[canonical]) {
			continue
		}
		if !PathWithin(canonicalPath(metadata.Dir), canonical) {
			return nil, fmt.Errorf("sourceview: selected Go file %s for %q is outside package dir %s", path, metadata.ImportPath, metadata.Dir)
		}
		paths[path] = true
	}
	var inputs []cacheInput
	ordered := make([]string, 0, len(paths))
	for path := range paths {
		ordered = append(ordered, path)
	}
	sort.Strings(ordered)
	for _, path := range ordered {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("sourceview: read selected Go source %s: %w", path, err)
		}
		rel, err := filepath.Rel(metadata.Dir, path)
		if err != nil {
			return nil, err
		}
		logicalPath := path
		if mainOwned {
			physical := canonicalPath(path)
			moduleRel, relErr := filepath.Rel(projection.manifest.physicalRoot, physical)
			if relErr != nil || moduleRel == ".." || strings.HasPrefix(moduleRel, ".."+string(filepath.Separator)) {
				return nil, fmt.Errorf("sourceview: selected main-module Go file %s is outside physical module root %s", path, projection.manifest.physicalRoot)
			}
			logicalPath = filepath.Join(projection.manifest.moduleRoot, moduleRel)
		}
		inputs = append(inputs, inputFromBytes("package:"+metadata.ImportPath+":go:"+filepath.ToSlash(rel), logicalPath, data))
	}
	if mainOwned {
		for _, path := range projection.manifest.sourcePaths {
			if canonicalPath(filepath.Dir(path)) != canonicalPath(metadata.Dir) {
				continue
			}
			source := projection.manifest.sources[path]
			rel, err := filepath.Rel(projection.manifest.moduleRoot, path)
			if err != nil {
				return nil, err
			}
			inputs = append(inputs, inputFromBytes("package:"+metadata.ImportPath+":gsx:"+filepath.ToSlash(rel), path, source))
		}
	}
	return inputs, nil
}

func moduleProvenanceInputs(labelPrefix, moduleDir, goMod string, requireGoMod bool) ([]cacheInput, error) {
	goModInput, err := fileInput(labelPrefix+":go.mod", goMod, requireGoMod)
	if err != nil {
		return nil, err
	}
	goSumInput, err := fileInput(labelPrefix+":go.sum", filepath.Join(moduleDir, "go.sum"), false)
	if err != nil {
		return nil, err
	}
	return []cacheInput{goModInput, goSumInput}, nil
}

func fileInput(label, path string, required bool) (cacheInput, error) {
	data, err := os.ReadFile(path)
	if err == nil {
		return inputFromBytes(label, filepath.Clean(path), data), nil
	}
	if os.IsNotExist(err) && !required {
		return cacheInput{label: label}, nil
	}
	return cacheInput{}, fmt.Errorf("sourceview: read cache provenance %s: %w", path, err)
}

func inputFromBytes(label, logicalPath string, data []byte) cacheInput {
	return cacheInput{label: label, logicalPath: logicalPath, digest: sha256.Sum256(data), present: true}
}

func mutableModule(module *ModuleMetadata) (*ModuleMetadata, bool) {
	if module == nil || module.Main {
		return nil, false
	}
	effective := module
	if module.Replace != nil {
		effective = module.Replace
	}
	return effective, effective.Version == "" && effective.Dir != ""
}

func moduleIdentityKey(original, effective *ModuleMetadata) string {
	return strings.Join([]string{original.Path, original.Version, effective.Path, effective.Version}, "\x00")
}

func isMainModule(module *ModuleMetadata, manifest *Manifest) bool {
	return module != nil && module.Main && module.Path == manifest.modulePath && canonicalPath(module.Dir) == canonicalPath(manifest.moduleRoot)
}

func canonicalPath(path string) string {
	path = filepath.Clean(path)
	if resolved, err := resolvePathAllowMissing(path); err == nil {
		return filepath.Clean(resolved)
	}
	return path
}

func cloneGraph(graph Graph) Graph {
	out := make(Graph, len(graph))
	for path, metadata := range graph {
		metadata.Imports = append([]string(nil), metadata.Imports...)
		metadata.Deps = append([]string(nil), metadata.Deps...)
		metadata.GoFiles = append([]string(nil), metadata.GoFiles...)
		metadata.CgoFiles = append([]string(nil), metadata.CgoFiles...)
		metadata.CompiledGoFiles = append([]string(nil), metadata.CompiledGoFiles...)
		metadata.Module = cloneModule(metadata.Module)
		out[path] = metadata
	}
	return out
}

func cloneModule(module *ModuleMetadata) *ModuleMetadata {
	if module == nil {
		return nil
	}
	clone := *module
	clone.Replace = cloneModule(module.Replace)
	return &clone
}

func dedupSortedStrings(values []string) []string {
	set := make(map[string]bool, len(values))
	for _, value := range values {
		if value != "" {
			set[value] = true
		}
	}
	out := make([]string, 0, len(set))
	for value := range set {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}
