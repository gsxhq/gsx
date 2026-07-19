// Package sourceview builds the one logical source manifest shared by normal
// code generation and persistent-cache metadata queries.
package sourceview

import (
	"bytes"
	"errors"
	"fmt"
	goast "go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"

	gsxast "github.com/gsxhq/gsx/ast"
	gsxparser "github.com/gsxhq/gsx/parser"
)

const pairedReplacement = "//go:build gsxpaired && !gsxpaired\n\npackage gsxpaired\n"
const absentGoReplacement = "//go:build gsxabsent && !gsxabsent\n\npackage gsxabsent\n"

// FileState is the complete saved-source state beneath an editor override.
// Absence and unreadability are intentionally distinct: absence participates in
// package membership, while unreadability is an operational failure that must
// remain visible when no override masks it.
type FileState uint8

const (
	FileAbsent FileState = iota
	FilePresent
	FileUnreadable
)

// FileSnapshot is one immutable saved-source observation. Its fields are kept
// private so callers cannot manufacture inconsistent states (for example,
// unreadable without an error) or mutate retained source bytes.
type FileSnapshot struct {
	state  FileState
	source []byte
	err    error
}

// PresentFile constructs a present saved-source snapshot.
func PresentFile(source []byte) FileSnapshot {
	cloned := make([]byte, len(source))
	copy(cloned, source)
	return FileSnapshot{state: FilePresent, source: cloned}
}

// AbsentFile constructs an absent saved-source snapshot.
func AbsentFile() FileSnapshot { return FileSnapshot{state: FileAbsent} }

// UnreadableFile constructs an unreadable saved-source snapshot.
func UnreadableFile(err error) FileSnapshot {
	if err == nil {
		err = fmt.Errorf("unknown saved-source read error")
	}
	return FileSnapshot{state: FileUnreadable, err: err}
}

// ReadFileSnapshot observes one saved path without collapsing an operational
// read failure into absence.
func ReadFileSnapshot(path string) FileSnapshot {
	source, err := os.ReadFile(path)
	switch {
	case err == nil:
		return PresentFile(source)
	case os.IsNotExist(err):
		return AbsentFile()
	default:
		return UnreadableFile(err)
	}
}

func (snapshot FileSnapshot) State() FileState { return snapshot.state }

// Source returns a defensive copy only for a present snapshot.
func (snapshot FileSnapshot) Source() ([]byte, bool) {
	if snapshot.state != FilePresent {
		return nil, false
	}
	return bytes.Clone(snapshot.source), true
}

func (snapshot FileSnapshot) Err() error {
	if snapshot.state != FileUnreadable {
		return nil
	}
	return snapshot.err
}

// BuildOptions identifies one module and any unsaved GSX or Go sources that
// replace its saved files. An override path is authoritative even when its
// bytes are empty; absence is represented by omitting the override and the disk
// file.
type BuildOptions struct {
	ModuleRoot string
	ModulePath string
	Overrides  map[string][]byte
}

// FileFact is the comparable dependency-surface projection of one GSX source.
// Body bytes deliberately do not participate: callers invalidate body analysis
// separately while using this fact only to decide whether the cold source
// selection must be rebuilt.
type FileFact struct {
	present     bool
	packageName string
	importsKey  string
}

// Present reports whether the source exists in the logical view.
func (fact FileFact) Present() bool { return fact.present }

// PackageName reports the parsed package clause, or an empty string when it
// could not be recovered.
func (fact FileFact) PackageName() string { return fact.packageName }

// Imports returns the sorted, unique authored import paths, excluding import C.
func (fact FileFact) Imports() []string {
	if fact.importsKey == "" {
		return nil
	}
	return strings.Split(fact.importsKey, "\x00")
}

// ReloadReason identifies why a current fact cannot reuse the last published
// cold source selection.
type ReloadReason uint8

const (
	ReloadNone ReloadReason = iota
	ReloadMembership
	ReloadPackage
	ReloadImports
)

// ReloadReasonFor compares a current per-path fact to its immutable published
// baseline. An authored import addition needs no rebuild when the published
// external importer already contains that exact path.
func ReloadReasonFor(published, current FileFact, availableImports map[string]bool) ReloadReason {
	if published.present != current.present {
		return ReloadMembership
	}
	if published.packageName != current.packageName {
		return ReloadPackage
	}
	if published.importsKey == current.importsKey {
		return ReloadNone
	}
	publishedImports := make(map[string]bool)
	for _, importPath := range published.Imports() {
		publishedImports[importPath] = true
	}
	for _, importPath := range current.Imports() {
		if !publishedImports[importPath] && !availableImports[importPath] {
			return ReloadImports
		}
	}
	return ReloadNone
}

// Manifest is an immutable logical source view. All returned maps, slices, and
// byte slices are copies so the codegen and cache consumers cannot diverge by
// mutating shared state.
type Manifest struct {
	moduleRoot    string
	physicalRoot  string
	modulePath    string
	sources       map[string][]byte
	sourcePaths   []string
	facts         map[string]FileFact
	gsxDirs       map[string]bool
	packageDirs   map[string]string
	importsByDir  map[string][]string
	pairedOutputs []string
	pairedPresent map[string]bool
	overlay       map[string][]byte
	sentinelFiles []string
	sentinelByDir map[string]string
	loadRoots     []string
	trackedFiles  map[string]FileSnapshot
}

// Build constructs the complete owned GSX manifest once. It never selects Go
// files: packages.Load/cmd-go remain the sole build-tag and cgo authority.
func Build(options BuildOptions) (*Manifest, error) {
	if options.ModuleRoot == "" {
		return nil, fmt.Errorf("sourceview: module root is empty")
	}
	root, err := filepath.Abs(options.ModuleRoot)
	if err != nil {
		return nil, fmt.Errorf("sourceview: resolve module root: %w", err)
	}
	root = filepath.Clean(root)
	physicalRoot := root
	if resolved, resolveErr := filepath.EvalSymlinks(root); resolveErr == nil {
		physicalRoot = filepath.Clean(resolved)
	}

	overrides := make(map[string][]byte, len(options.Overrides))
	for path, source := range options.Overrides {
		absPath, absErr := filepath.Abs(path)
		if absErr != nil {
			return nil, fmt.Errorf("sourceview: resolve override %s: %w", path, absErr)
		}
		overrides[filepath.Clean(absPath)] = bytes.Clone(source)
	}

	paths := make(map[string]bool)
	err = filepath.WalkDir(physicalRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() && path != physicalRoot {
			if entry.Name() == "vendor" {
				return filepath.SkipDir
			}
			startsModule, moduleErr := directoryStartsModule(path)
			if moduleErr != nil {
				return moduleErr
			}
			if startsModule {
				return filepath.SkipDir
			}
		}
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".gsx") {
			rel, relErr := filepath.Rel(physicalRoot, path)
			if relErr != nil {
				return relErr
			}
			paths[filepath.Clean(filepath.Join(root, rel))] = true
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("sourceview: discover owned GSX sources: %w", err)
	}
	trackedFiles := make(map[string]FileSnapshot)
	for path, source := range overrides {
		if !strings.HasSuffix(path, ".gsx") {
			if strings.HasSuffix(path, ".go") {
				owned, ownershipErr := OwnsPath(root, path)
				if ownershipErr != nil {
					return nil, ownershipErr
				}
				if owned {
					trackedFiles[path] = PresentFile(source)
				}
			}
			continue
		}
		owned, ownershipErr := OwnsPath(root, path)
		if ownershipErr != nil {
			return nil, ownershipErr
		}
		if owned {
			paths[path] = true
		}
	}

	orderedPaths := make([]string, 0, len(paths))
	for path := range paths {
		orderedPaths = append(orderedPaths, path)
	}
	sort.Strings(orderedPaths)
	sources := make(map[string][]byte, len(orderedPaths))
	pairedPresent := make(map[string]bool, len(orderedPaths))
	for _, path := range orderedPaths {
		source, overridden := overrides[path]
		if !overridden {
			source, err = os.ReadFile(path)
			if err != nil {
				if os.IsNotExist(err) {
					continue
				}
				return nil, fmt.Errorf("sourceview: read GSX source %s: %w", path, err)
			}
		}
		sources[path] = bytes.Clone(source)
		paired := strings.TrimSuffix(path, ".gsx") + ".x.go"
		present, pairedErr := pairedOutputPresent(paired)
		if pairedErr != nil {
			return nil, pairedErr
		}
		pairedPresent[paired] = present
	}
	return newManifest(root, physicalRoot, options.ModulePath, sources, pairedPresent, nil, trackedFiles)
}

func newManifest(moduleRoot, physicalRoot, modulePath string, sources map[string][]byte, pairedPresent map[string]bool, preferredSentinels map[string]string, trackedFiles map[string]FileSnapshot) (*Manifest, error) {
	manifest := &Manifest{
		moduleRoot:    moduleRoot,
		physicalRoot:  physicalRoot,
		modulePath:    modulePath,
		sources:       make(map[string][]byte, len(sources)),
		facts:         make(map[string]FileFact, len(sources)),
		gsxDirs:       make(map[string]bool),
		packageDirs:   make(map[string]string),
		importsByDir:  make(map[string][]string),
		pairedPresent: make(map[string]bool, len(sources)),
		overlay:       make(map[string][]byte),
		sentinelByDir: make(map[string]string),
		trackedFiles:  cloneFileSnapshots(trackedFiles),
	}
	for path, snapshot := range manifest.trackedFiles {
		if strings.HasSuffix(path, ".go") {
			switch snapshot.state {
			case FilePresent:
				manifest.overlay[path] = bytes.Clone(snapshot.source)
			case FileAbsent:
				manifest.overlay[path] = []byte(absentGoReplacement)
			}
		}
		if strings.HasSuffix(path, ".gsx") && snapshot.state == FileUnreadable {
			paired := strings.TrimSuffix(path, ".gsx") + ".x.go"
			manifest.pairedPresent[paired] = pairedPresent[paired]
			manifest.pairedOutputs = append(manifest.pairedOutputs, paired)
			manifest.overlay[paired] = []byte(pairedReplacement)
		}
	}
	for path, source := range sources {
		manifest.sources[path] = bytes.Clone(source)
		manifest.sourcePaths = append(manifest.sourcePaths, path)
	}
	sort.Strings(manifest.sourcePaths)

	packageNames := make(map[string]string)
	loadRoots := make(map[string]bool)
	for _, path := range manifest.sourcePaths {
		source := manifest.sources[path]
		fact := Inspect(path, source, true)
		manifest.facts[path] = fact
		dir := filepath.Dir(path)
		manifest.gsxDirs[dir] = true
		manifest.importsByDir[dir] = mergeSortedUnique(manifest.importsByDir[dir], fact.Imports())

		paired := strings.TrimSuffix(path, ".gsx") + ".x.go"
		present := pairedPresent[paired]
		manifest.pairedPresent[paired] = present
		manifest.pairedOutputs = append(manifest.pairedOutputs, paired)
		// Replace the exact paired path even when it was absent at snapshot time.
		// That keeps a concurrently-created stale output from entering either the
		// cmd/go graph or packages.Load after this manifest was frozen.
		manifest.overlay[paired] = []byte(pairedReplacement)

		if fact.packageName == "" {
			continue
		}
		if packageNames[dir] == "" {
			packageNames[dir] = fact.packageName
		}
		if packagePath, ok := importPathForDir(moduleRoot, modulePath, dir); ok {
			loadRoots[packagePath] = true
			manifest.packageDirs[packagePath] = dir
		}
		for _, importPath := range fact.Imports() {
			loadRoots[importPath] = true
		}
	}
	sort.Strings(manifest.pairedOutputs)
	dirs := make([]string, 0, len(packageNames))
	for dir := range packageNames {
		dirs = append(dirs, dir)
	}
	sort.Strings(dirs)
	for _, dir := range dirs {
		sentinel := preferredSentinels[dir]
		if sentinel == "" {
			var sentinelErr error
			sentinel, sentinelErr = sentinelPath(dir, manifest.overlay)
			if sentinelErr != nil {
				return nil, sentinelErr
			}
		} else if filepath.Dir(sentinel) != dir {
			return nil, fmt.Errorf("sourceview: preferred sentinel %s is outside package directory %s", sentinel, dir)
		} else if _, occupied := manifest.overlay[sentinel]; occupied {
			return nil, fmt.Errorf("sourceview: preferred sentinel %s conflicts with a paired generated output", sentinel)
		}
		manifest.overlay[sentinel] = []byte("package " + packageNames[dir] + "\n")
		manifest.sentinelFiles = append(manifest.sentinelFiles, sentinel)
		manifest.sentinelByDir[dir] = sentinel
	}
	for root := range loadRoots {
		manifest.loadRoots = append(manifest.loadRoots, root)
	}
	sort.Strings(manifest.loadRoots)
	return manifest, nil
}

func pairedOutputPresent(path string) (bool, error) {
	info, err := os.Stat(path)
	switch {
	case err == nil && info.Mode().IsRegular():
		return true, nil
	case err == nil:
		return false, fmt.Errorf("sourceview: paired generated output %s is not a regular file", path)
	case os.IsNotExist(err):
		return false, nil
	default:
		return false, fmt.Errorf("sourceview: inspect paired generated output %s: %w", path, err)
	}
}

// WithOverrides derives one effective source view from this saved snapshot.
// Overrides replace or add GSX or Go bytes without consulting live disk for
// existing saved membership, so an editor update cannot accidentally observe
// an unrelated concurrent filesystem change.
func (manifest *Manifest) WithOverrides(overrides map[string][]byte) (*Manifest, error) {
	snapshots := make(map[string]FileSnapshot, len(overrides))
	for path, source := range overrides {
		snapshots[path] = PresentFile(source)
	}
	return manifest.WithFileSnapshots(snapshots)
}

// WithFileSnapshots derives a view with exact per-path present, absent, or
// unreadable saved states. It is the layer primitive beneath WithOverrides and
// Module buffer-close transitions.
func (manifest *Manifest) WithFileSnapshots(snapshots map[string]FileSnapshot) (*Manifest, error) {
	if manifest == nil {
		return nil, fmt.Errorf("sourceview: nil saved manifest")
	}
	sources := cloneSources(manifest.sources)
	pairedPresent := maps.Clone(manifest.pairedPresent)
	preferredSentinels := maps.Clone(manifest.sentinelByDir)
	trackedFiles := cloneFileSnapshots(manifest.trackedFiles)
	for path, snapshot := range snapshots {
		absPath, err := filepath.Abs(path)
		if err != nil {
			return nil, fmt.Errorf("sourceview: resolve saved file %s: %w", path, err)
		}
		absPath = filepath.Clean(absPath)
		if !strings.HasSuffix(absPath, ".gsx") && !strings.HasSuffix(absPath, ".go") {
			continue
		}
		owned, err := OwnsPath(manifest.moduleRoot, absPath)
		if err != nil {
			return nil, err
		}
		if !owned {
			continue
		}
		if strings.HasSuffix(absPath, ".go") {
			trackedFiles[absPath] = cloneFileSnapshot(snapshot)
			continue
		}

		delete(sources, absPath)
		delete(trackedFiles, absPath)
		paired := strings.TrimSuffix(absPath, ".gsx") + ".x.go"
		switch snapshot.state {
		case FilePresent:
			sources[absPath] = bytes.Clone(snapshot.source)
			if _, exists := pairedPresent[paired]; !exists {
				present, err := pairedOutputPresent(paired)
				if err != nil {
					return nil, err
				}
				pairedPresent[paired] = present
			}
		case FileAbsent:
			delete(pairedPresent, paired)
		case FileUnreadable:
			trackedFiles[absPath] = cloneFileSnapshot(snapshot)
			if _, exists := pairedPresent[paired]; !exists {
				present, err := pairedOutputPresent(paired)
				if err != nil {
					return nil, err
				}
				pairedPresent[paired] = present
			}
		default:
			return nil, fmt.Errorf("sourceview: invalid saved file state %d for %s", snapshot.state, absPath)
		}
	}
	return newManifest(manifest.moduleRoot, manifest.physicalRoot, manifest.modulePath, sources, pairedPresent, preferredSentinels, trackedFiles)
}

// RefreshDirs replaces the saved GSX membership, bytes, paired-output state,
// and sentinel choice for the listed direct package directories. Sources in
// every other directory remain byte-for-byte identical to this snapshot.
func (manifest *Manifest) RefreshDirs(dirs []string) (*Manifest, error) {
	if manifest == nil {
		return nil, fmt.Errorf("sourceview: nil saved manifest")
	}
	dirSet := make(map[string]bool, len(dirs))
	for _, dir := range dirs {
		absDir, err := filepath.Abs(dir)
		if err != nil {
			return nil, fmt.Errorf("sourceview: resolve refresh directory %s: %w", dir, err)
		}
		absDir = filepath.Clean(absDir)
		owned, err := OwnsDir(manifest.moduleRoot, absDir)
		if err != nil {
			return nil, err
		}
		if !owned {
			return nil, fmt.Errorf("sourceview: refresh directory %s is not owned by module root %s", absDir, manifest.moduleRoot)
		}
		dirSet[absDir] = true
	}
	sources := cloneSources(manifest.sources)
	pairedPresent := maps.Clone(manifest.pairedPresent)
	preferredSentinels := maps.Clone(manifest.sentinelByDir)
	trackedFiles := cloneFileSnapshots(manifest.trackedFiles)
	for path := range sources {
		if !dirSet[filepath.Dir(path)] {
			continue
		}
		delete(sources, path)
		delete(pairedPresent, strings.TrimSuffix(path, ".gsx")+".x.go")
	}
	for path := range trackedFiles {
		if !dirSet[filepath.Dir(path)] {
			continue
		}
		if strings.HasSuffix(path, ".gsx") {
			delete(trackedFiles, path)
			delete(pairedPresent, strings.TrimSuffix(path, ".gsx")+".x.go")
			continue
		}
		trackedFiles[path] = ReadFileSnapshot(path)
	}
	for dir := range dirSet {
		delete(preferredSentinels, dir)
	}
	orderedDirs := make([]string, 0, len(dirSet))
	for dir := range dirSet {
		orderedDirs = append(orderedDirs, dir)
	}
	sort.Strings(orderedDirs)
	for _, dir := range orderedDirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("sourceview: read refresh directory %s: %w", dir, err)
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".gsx") {
				continue
			}
			path := filepath.Join(dir, entry.Name())
			owned, err := OwnsPath(manifest.moduleRoot, path)
			if err != nil {
				return nil, err
			}
			if !owned {
				continue
			}
			source, err := os.ReadFile(path)
			if err != nil {
				trackedFiles[path] = UnreadableFile(err)
				paired := strings.TrimSuffix(path, ".gsx") + ".x.go"
				present, pairedErr := pairedOutputPresent(paired)
				if pairedErr != nil {
					return nil, pairedErr
				}
				pairedPresent[paired] = present
				continue
			}
			sources[path] = source
			paired := strings.TrimSuffix(path, ".gsx") + ".x.go"
			present, err := pairedOutputPresent(paired)
			if err != nil {
				return nil, err
			}
			pairedPresent[paired] = present
		}
	}
	return newManifest(manifest.moduleRoot, manifest.physicalRoot, manifest.modulePath, sources, pairedPresent, preferredSentinels, trackedFiles)
}

func cloneSources(sources map[string][]byte) map[string][]byte {
	out := make(map[string][]byte, len(sources))
	for path, source := range sources {
		out[path] = bytes.Clone(source)
	}
	return out
}

func cloneFileSnapshot(snapshot FileSnapshot) FileSnapshot {
	snapshot.source = bytes.Clone(snapshot.source)
	return snapshot
}

func cloneFileSnapshots(snapshots map[string]FileSnapshot) map[string]FileSnapshot {
	out := make(map[string]FileSnapshot, len(snapshots))
	for path, snapshot := range snapshots {
		out[path] = cloneFileSnapshot(snapshot)
	}
	return out
}

// Inspect extracts the dependency-surface fact for source. A malformed file
// retains any recoverable package clause but publishes no guessed import edge.
func Inspect(path string, source []byte, present bool) FileFact {
	fact := FileFact{present: present}
	if !present {
		return fact
	}
	fset := token.NewFileSet()
	file, parseErr := gsxparser.ParseFile(fset, path, source, 0)
	if file == nil || file.Package == "" {
		return fact
	}
	fact.packageName = file.Package
	if parseErr != nil {
		return fact
	}
	imports, err := fileImports(file)
	if err != nil {
		return fact
	}
	fact.importsKey = strings.Join(imports, "\x00")
	return fact
}

func fileImports(file *gsxast.File) ([]string, error) {
	unique := make(map[string]bool)
	for _, declaration := range file.Decls {
		chunk, ok := declaration.(*gsxast.GoChunk)
		if !ok {
			continue
		}
		parsed, err := parser.ParseFile(token.NewFileSet(), "", "package _gsxsourceview\n"+chunk.Src, parser.SkipObjectResolution)
		if err != nil {
			return nil, err
		}
		for _, declaration := range parsed.Decls {
			general, ok := declaration.(*goast.GenDecl)
			if !ok || general.Tok != token.IMPORT {
				continue
			}
			for _, spec := range general.Specs {
				importSpec := spec.(*goast.ImportSpec)
				path, unquoteErr := strconv.Unquote(importSpec.Path.Value)
				if unquoteErr == nil && path != "C" {
					unique[path] = true
				}
			}
		}
	}
	imports := make([]string, 0, len(unique))
	for path := range unique {
		imports = append(imports, path)
	}
	sort.Strings(imports)
	return imports, nil
}

// ModuleRoot returns the logical absolute root used for source paths.
func (manifest *Manifest) ModuleRoot() string { return manifest.moduleRoot }

// PhysicalRoot returns ModuleRoot with symlinks resolved when possible.
func (manifest *Manifest) PhysicalRoot() string { return manifest.physicalRoot }

// ModulePath returns the declared main-module import path.
func (manifest *Manifest) ModulePath() string { return manifest.modulePath }

func (manifest *Manifest) SourcePaths() []string {
	return append([]string(nil), manifest.sourcePaths...)
}

func (manifest *Manifest) Source(path string) ([]byte, bool) {
	source, ok := manifest.sources[filepath.Clean(path)]
	return bytes.Clone(source), ok
}

// FileSnapshot returns an explicitly retained saved-source state. GSX
// membership is complete, so a missing owned .gsx path is known absent. Go
// membership remains cmd/go-owned; a Go path is known only after an editor
// transition explicitly snapshots it.
func (manifest *Manifest) FileSnapshot(path string) (FileSnapshot, bool) {
	path = filepath.Clean(path)
	if source, ok := manifest.sources[path]; ok {
		return PresentFile(source), true
	}
	if snapshot, ok := manifest.trackedFiles[path]; ok {
		return cloneFileSnapshot(snapshot), true
	}
	if strings.HasSuffix(path, ".gsx") && PathWithin(manifest.moduleRoot, path) {
		return AbsentFile(), true
	}
	return FileSnapshot{}, false
}

// CheckReadable fails deterministically when any unmasked saved source is
// unreadable. WithOverrides removes the unreadable state only for an actively
// authoritative buffer, so closing that buffer exposes this error again.
func (manifest *Manifest) CheckReadable() error {
	paths := make([]string, 0)
	for path, snapshot := range manifest.trackedFiles {
		if snapshot.state == FileUnreadable {
			paths = append(paths, path)
		}
	}
	if len(paths) == 0 {
		return nil
	}
	sort.Strings(paths)
	path := paths[0]
	return fmt.Errorf("sourceview: read saved source %s: %w", path, manifest.trackedFiles[path].err)
}

func (manifest *Manifest) Facts() map[string]FileFact {
	out := make(map[string]FileFact, len(manifest.facts))
	maps.Copy(out, manifest.facts)
	return out
}

func (manifest *Manifest) Fact(path string) (FileFact, bool) {
	fact, ok := manifest.facts[filepath.Clean(path)]
	return fact, ok
}

func (manifest *Manifest) GSXDirs() map[string]bool {
	out := make(map[string]bool, len(manifest.gsxDirs))
	for dir := range manifest.gsxDirs {
		out[dir] = true
	}
	return out
}

func (manifest *Manifest) PackageDirs() map[string]string {
	out := make(map[string]string, len(manifest.packageDirs))
	maps.Copy(out, manifest.packageDirs)
	return out
}

func (manifest *Manifest) PackageDir(importPath string) (string, bool) {
	dir, ok := manifest.packageDirs[importPath]
	return dir, ok
}

// PackagePathForDir returns the manifest-owned import path for dir.
func (manifest *Manifest) PackagePathForDir(dir string) (string, bool) {
	dir = canonicalPath(dir)
	for importPath, packageDir := range manifest.packageDirs {
		if canonicalPath(packageDir) == dir {
			return importPath, true
		}
	}
	return "", false
}

// SelectedLoadRoots returns the deterministic authored GSX import closure of
// dirs. Main-module packages are traversed through manifest edges; external
// imports are roots but are not traversed here because cmd/go owns that graph.
func (manifest *Manifest) SelectedLoadRoots(dirs []string) ([]string, error) {
	var queue []string
	for _, dir := range dirs {
		path, ok := manifest.PackagePathForDir(dir)
		if !ok {
			return nil, fmt.Errorf("sourceview: no manifest package for selected directory %s", dir)
		}
		queue = append(queue, path)
	}

	seen := map[string]bool{"": true, "C": true}
	var roots []string
	for len(queue) > 0 {
		path := queue[0]
		queue = queue[1:]
		if seen[path] {
			continue
		}
		seen[path] = true
		roots = append(roots, path)
		if dir, owned := manifest.PackageDir(path); owned {
			queue = append(queue, manifest.ImportsForDir(dir)...)
		}
	}
	sort.Strings(roots)
	return roots, nil
}

func (manifest *Manifest) ImportsForDir(dir string) []string {
	return append([]string(nil), manifest.importsByDir[filepath.Clean(dir)]...)
}

func (manifest *Manifest) PairedOutputs() []string {
	return append([]string(nil), manifest.pairedOutputs...)
}

func (manifest *Manifest) Overlay() map[string][]byte {
	out := make(map[string][]byte, len(manifest.overlay))
	for path, source := range manifest.overlay {
		if source == nil {
			out[path] = nil
			continue
		}
		out[path] = bytes.Clone(source)
	}
	return out
}

// PackagesOverlay projects logical manifest targets onto the physical module
// root used by cmd/go. The values remain immutable copies. This is the exact
// map passed to packages.Config.Overlay so package selection and syntax loading
// consume the same bytes even when ModuleRoot traverses a symlink.
func (manifest *Manifest) PackagesOverlay() (map[string][]byte, error) {
	out := make(map[string][]byte, len(manifest.overlay)*2)
	for logical, source := range manifest.overlay {
		rel, err := filepath.Rel(manifest.moduleRoot, logical)
		if err != nil || rel == ".." || filepath.IsAbs(rel) || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return nil, fmt.Errorf("sourceview: packages overlay target %s is outside logical module root %s", logical, manifest.moduleRoot)
		}
		physical := filepath.Join(manifest.physicalRoot, rel)
		out[physical] = bytes.Clone(source)
		// packages.Load invokes cmd/go from the logical module root, while cmd/go
		// may canonicalize that root before matching overlay targets. Publish both
		// spellings to the same immutable bytes so either identity observes one
		// source view; downstream retained paths are projected back to logical.
		if physical != logical {
			out[logical] = bytes.Clone(source)
		}
	}
	return out, nil
}

func (manifest *Manifest) OverlayPaths() []string {
	paths := make([]string, 0, len(manifest.overlay))
	for path := range manifest.overlay {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths
}

func (manifest *Manifest) SentinelFiles() []string {
	return append([]string(nil), manifest.sentinelFiles...)
}

func (manifest *Manifest) LoadRoots() []string { return append([]string(nil), manifest.loadRoots...) }

func mergeSortedUnique(left, right []string) []string {
	set := make(map[string]bool, len(left)+len(right))
	for _, value := range left {
		set[value] = true
	}
	for _, value := range right {
		set[value] = true
	}
	out := make([]string, 0, len(set))
	for value := range set {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func sentinelPath(dir string, overlay map[string][]byte) (string, error) {
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
			return "", fmt.Errorf("sourceview: inspect source sentinel %s: %w", path, err)
		}
	}
}

func directoryStartsModule(dir string) (bool, error) {
	path := filepath.Join(dir, "go.mod")
	info, err := os.Lstat(path)
	if err != nil {
		if pathAbsent(err) {
			return false, nil
		}
		return false, fmt.Errorf("sourceview: inspect module boundary in %s: %w", dir, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		info, err = os.Stat(path)
		if err != nil {
			return false, fmt.Errorf("sourceview: resolve module boundary %s: %w", path, err)
		}
	}
	if !info.Mode().IsRegular() {
		return false, fmt.Errorf("sourceview: module boundary %s is not a regular file", path)
	}
	return true, nil
}

// OwnsPath reports exact parent-module ownership. Nested modules and any
// vendor path segment are ownership boundaries.
func OwnsPath(root, path string) (bool, error) {
	root = filepath.Clean(root)
	path = filepath.Clean(path)
	if !PathWithin(root, path) {
		return false, nil
	}
	physicalRoot, err := resolvePathAllowMissing(root)
	if err != nil {
		return false, fmt.Errorf("sourceview: resolve physical module root %s: %w", root, err)
	}
	physicalPath, err := resolvePathAllowMissing(path)
	if err != nil {
		return false, fmt.Errorf("sourceview: resolve physical source path %s: %w", path, err)
	}
	if !PathWithin(physicalRoot, physicalPath) {
		return false, nil
	}
	for _, pair := range [][2]string{{root, path}, {physicalRoot, physicalPath}} {
		if pathContainsVendor(pair[0], pair[1]) {
			return false, nil
		}
		if pair[1] == pair[0] {
			continue
		}
		for dir := filepath.Dir(pair[1]); dir != pair[0]; dir = filepath.Dir(dir) {
			startsModule, moduleErr := directoryStartsModule(dir)
			if moduleErr != nil {
				return false, moduleErr
			}
			if startsModule {
				return false, nil
			}
			parent := filepath.Dir(dir)
			if parent == dir {
				return false, fmt.Errorf("sourceview: path %s escaped module root %s while checking ownership", pair[1], pair[0])
			}
		}
	}
	return true, nil
}

// OwnsDir reports exact module ownership for a directory, including a go.mod
// located in the directory itself. It is the directory-target counterpart to
// OwnsPath, whose path argument normally names a source file.
func OwnsDir(root, dir string) (bool, error) {
	return OwnsPath(root, filepath.Join(filepath.Clean(dir), ".gsx-sourceview-directory"))
}

func pathContainsVendor(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return true
	}
	for part := range strings.SplitSeq(rel, string(filepath.Separator)) {
		if part == "vendor" {
			return true
		}
	}
	return false
}

// pathAbsent reports whether a filesystem-probe error means the path is simply
// not present to inspect: either genuinely missing (ENOENT) or unreachable
// because the environment has no filesystem at all (ENOSYS). The latter is the
// browser js/wasm playground, which serves a purely in-memory virtual module
// ("/__gsxmem__") that never exists on disk; there, every lstat/EvalSymlinks
// returns ENOSYS ("not implemented on js") for the same paths a native run
// resolves as ENOENT. Both cases mean "there is nothing on disk here — resolve
// the path lexically." A broken symlink or any other error is NOT absence and
// stays fail-closed.
func pathAbsent(err error) bool {
	return os.IsNotExist(err) || errors.Is(err, syscall.ENOSYS)
}

// resolvePathAllowMissing resolves every existing symlink prefix and then
// appends a missing suffix lexically. A broken symlink is an error, not a
// missing path, which keeps ownership checks fail-closed.
func resolvePathAllowMissing(path string) (string, error) {
	path = filepath.Clean(path)
	current := path
	var missing []string
	for {
		resolved, err := filepath.EvalSymlinks(current)
		if err == nil {
			parts := append([]string{filepath.Clean(resolved)}, missing...)
			return filepath.Join(parts...), nil
		}
		if !pathAbsent(err) {
			return "", err
		}
		if _, lstatErr := os.Lstat(current); lstatErr == nil {
			return "", err
		} else if !pathAbsent(lstatErr) {
			return "", lstatErr
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", err
		}
		missing = append([]string{filepath.Base(current)}, missing...)
		current = parent
	}
}

func PathWithin(root, path string) bool {
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(path))
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func importPathForDir(root, modulePath, dir string) (string, bool) {
	if modulePath == "" {
		return "", false
	}
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(dir))
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	if rel == "." {
		return modulePath, true
	}
	return strings.TrimSuffix(modulePath, "/") + "/" + filepath.ToSlash(rel), true
}
