package gen

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/gsxhq/gsx/internal/sourceview"
)

// graphFormat versions the graph key and the entry encoding.
const (
	graphKeyVersion   = "gsx-graph-v1"
	graphEntryVersion = 1
	graphMaxVariants  = 4
	graphEntrySubdir  = "graph"
)

type graphCacheReason string

const (
	graphHit            graphCacheReason = "graph-hit"
	graphEntryMissing   graphCacheReason = "graph-entry-missing"
	graphEntryCorrupt   graphCacheReason = "graph-entry-corrupt"
	graphWitnessChanged graphCacheReason = "graph-witness-changed"
	graphUncacheable    graphCacheReason = "graph-uncacheable"
	// graphStoreSkipped: files changed while go list ran, so the graph does
	// not describe the manifest's witness.
	graphStoreSkipped graphCacheReason = "graph-store-skipped"
	graphStoreFailed  graphCacheReason = "graph-store-failed"
)

// graphCacheOutcome is what one graph cache attempt did, for -v reporting.
type graphCacheOutcome struct {
	Reason graphCacheReason
	Detail string
}

func (o graphCacheOutcome) Hit() bool { return o.Reason == graphHit }

// graphVariant is one stored go list graph with the witness it was computed
// under. Graph is module-relative (see relativizeGraph).
type graphVariant struct {
	Witness sourceview.GraphWitnessInput `json:"witness"`
	Digest  string                       `json:"digest"`
	Graph   sourceview.Graph             `json:"graph"`
}

// graphEntry is the on-disk form: variants, most recently stored first.
type graphEntry struct {
	Version  int            `json:"version"`
	Variants []graphVariant `json:"variants"`
}

// graphKey hashes everything that selects the go list query and is not
// covered by the witness: the context fingerprint, module path, patterns, the
// overlay (module-relative paths and bytes) and go.mod/go.sum. identity is
// codegenIdentity, so a gsx binary never reuses entries another one stored. It
// contains no checkout path, so checkouts of one module share a key.
func graphKey(fingerprint, identity string, manifest *sourceview.Manifest, patterns []string) (string, error) {
	hash := sha256.New()
	field := func(data []byte) {
		var size [8]byte
		binary.BigEndian.PutUint64(size[:], uint64(len(data)))
		hash.Write(size[:])
		hash.Write(data)
	}
	field([]byte(graphKeyVersion))
	field([]byte(fingerprint))
	field([]byte(identity))
	field([]byte(manifest.ModulePath()))
	field([]byte(fmt.Sprint(len(patterns))))
	for _, pattern := range patterns {
		field([]byte(pattern))
	}
	overlay, err := overlayByRel(manifest.Overlay(), manifest.PhysicalRoot(), manifest.ModuleRoot())
	if err != nil {
		return "", fmt.Errorf("graph key: %w", err)
	}
	paths := make([]string, 0, len(overlay))
	for rel := range overlay {
		paths = append(paths, rel)
	}
	sort.Strings(paths)
	field([]byte(fmt.Sprint(len(paths))))
	for _, rel := range paths {
		source := overlay[rel]
		field([]byte(rel))
		if source == nil {
			field([]byte{0}) // deletion entry, distinct from an empty file
		} else {
			field(append([]byte{1}, source...))
		}
	}
	goMod, err := os.ReadFile(filepath.Join(manifest.ModuleRoot(), "go.mod"))
	if err != nil {
		return "", fmt.Errorf("graph key: %w", err)
	}
	field(goMod)
	goSum, err := os.ReadFile(filepath.Join(manifest.ModuleRoot(), "go.sum"))
	switch {
	case err == nil:
		field(append([]byte{1}, goSum...))
	case os.IsNotExist(err):
		field([]byte{0})
	default:
		return "", fmt.Errorf("graph key: %w", err)
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

// overlayByRel re-keys overlay by module-relative slash path. Two paths that
// map to one relative path (spelled under different roots) are an error: the
// key could not tell which bytes go list saw.
func overlayByRel(overlay map[string][]byte, roots ...string) (map[string][]byte, error) {
	out := make(map[string][]byte, len(overlay))
	for p, source := range overlay {
		var rel string
		found := false
		for _, root := range roots {
			if rel, found = relSlash(root, p); found {
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("overlay path %s is outside module root %s", p, roots[len(roots)-1])
		}
		if _, dup := out[rel]; dup {
			return nil, fmt.Errorf("two overlay paths map to module-relative path %s", rel)
		}
		out[rel] = source
	}
	return out, nil
}

// relUnderRoots returns p relative (slash form) to the manifest's logical or
// physical root, whichever contains it.
func relUnderRoots(manifest *sourceview.Manifest, p string) (string, bool) {
	for _, root := range []string{manifest.PhysicalRoot(), manifest.ModuleRoot()} {
		if rel, ok := relSlash(root, p); ok {
			return rel, true
		}
	}
	return "", false
}

// relSlash returns p relative to root as a slash path ("." for root itself),
// or false when p is not inside root.
func relSlash(root, p string) (string, bool) {
	if !filepath.IsAbs(p) || !sourceview.PathWithin(root, p) {
		return "", false
	}
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(p))
	if err != nil {
		return "", false
	}
	return filepath.ToSlash(rel), true
}

func isMainModulePackage(pkg sourceview.PackageMetadata) bool {
	return pkg.Module != nil && pkg.Module.Main
}

// directoryReplaced reports a module whose source is a local directory: its
// files are not in the manifest, so they cannot be witnessed.
func directoryReplaced(module *sourceview.ModuleMetadata) bool {
	if module == nil {
		return false
	}
	if module.Replace != nil && module.Replace.Version == "" {
		return true
	}
	return module.Version == "" && !module.Main
}

// witnessInputForGraph names the main-module directories and embedded files
// go list read to produce graph, plus the absent "shadow" directories whose
// creation would make an import of a non-main package ambiguous. It wraps
// ErrWitnessUnavailable when the graph cannot be witnessed.
func witnessInputForGraph(graph sourceview.Graph, manifest *sourceview.Manifest) (sourceview.GraphWitnessInput, error) {
	prefix := manifest.ModulePath() + "/"
	dirs := make(map[string]bool)
	embeds := make(map[string]bool)
	for importPath, pkg := range graph {
		if directoryReplaced(pkg.Module) {
			return sourceview.GraphWitnessInput{}, fmt.Errorf("%w: package %q is in directory-replaced module %q", sourceview.ErrWitnessUnavailable, importPath, pkg.Module.Path)
		}
		if !isMainModulePackage(pkg) {
			if rest, shadow := strings.CutPrefix(pkg.ImportPath, prefix); shadow && rest != "" {
				dirs[rest] = true
			}
			continue
		}
		rel, ok := relUnderRoots(manifest, pkg.Dir)
		if !ok {
			return sourceview.GraphWitnessInput{}, fmt.Errorf("%w: main-module package %q dir %q is outside module root %s", sourceview.ErrWitnessUnavailable, importPath, pkg.Dir, manifest.ModuleRoot())
		}
		dirs[rel] = true
		for _, embed := range pkg.EmbedFiles {
			embeds[path.Join(rel, filepath.ToSlash(embed))] = true
		}
	}
	return sourceview.GraphWitnessInput{Dirs: sortedKeys(dirs), EmbedFiles: sortedKeys(embeds)}, nil
}

func sortedKeys(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for key := range set {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}

// copyPackage clones pkg's slices and module, dropping Deps. Only gsx-read
// fields are kept, and Deps is not among them.
func copyPackage(pkg sourceview.PackageMetadata) sourceview.PackageMetadata {
	pkg.Deps = nil
	pkg.Imports = slices.Clone(pkg.Imports)
	pkg.GoFiles = slices.Clone(pkg.GoFiles)
	pkg.CgoFiles = slices.Clone(pkg.CgoFiles)
	pkg.CompiledGoFiles = slices.Clone(pkg.CompiledGoFiles)
	pkg.EmbedFiles = slices.Clone(pkg.EmbedFiles)
	pkg.Module = copyModule(pkg.Module)
	return pkg
}

func copyModule(module *sourceview.ModuleMetadata) *sourceview.ModuleMetadata {
	if module == nil {
		return nil
	}
	clone := *module
	clone.Replace = copyModule(module.Replace)
	return &clone
}

// relativizeGraph returns the stored form of graph: main-module Dir,
// Module.Dir and Module.GoMod become slash paths relative to goModDir, and
// Deps (which gsx does not read) are dropped. It fails when a main-module
// path is not under goModDir, or when a main-module file list holds an
// absolute path that rebasing could not tell from a relative one.
func relativizeGraph(graph sourceview.Graph, goModDir string) (sourceview.Graph, error) {
	out := make(sourceview.Graph, len(graph))
	for importPath, pkg := range graph {
		pkg = copyPackage(pkg)
		if isMainModulePackage(pkg) {
			var err error
			if pkg.Dir, err = relativeMainPath(goModDir, pkg.Dir, importPath, "Dir", false); err != nil {
				return nil, err
			}
			if pkg.Module.Dir, err = relativeMainPath(goModDir, pkg.Module.Dir, importPath, "Module.Dir", false); err != nil {
				return nil, err
			}
			if pkg.Module.GoMod, err = relativeMainPath(goModDir, pkg.Module.GoMod, importPath, "Module.GoMod", true); err != nil {
				return nil, err
			}
			for _, files := range [][]string{pkg.GoFiles, pkg.CgoFiles, pkg.CompiledGoFiles, pkg.EmbedFiles} {
				for _, file := range files {
					if filepath.IsAbs(file) {
						return nil, fmt.Errorf("graph store: main-module package %q lists absolute file %s", importPath, file)
					}
				}
			}
		}
		out[importPath] = pkg
	}
	return out, nil
}

func relativeMainPath(goModDir, p, importPath, field string, optional bool) (string, error) {
	if p == "" && optional {
		return "", nil
	}
	rel, ok := relSlash(goModDir, p)
	if !ok {
		return "", fmt.Errorf("graph store: main-module package %q %s %q is not under %s", importPath, field, p, goModDir)
	}
	return rel, nil
}

// rebaseGraph is the inverse of relativizeGraph onto the current run's
// goModDir. The result shares nothing with graph and has no Deps.
func rebaseGraph(graph sourceview.Graph, goModDir string) sourceview.Graph {
	abs := func(rel string) string {
		if rel == "" {
			return ""
		}
		return filepath.Join(goModDir, filepath.FromSlash(rel))
	}
	out := make(sourceview.Graph, len(graph))
	for importPath, pkg := range graph {
		pkg = copyPackage(pkg)
		if isMainModulePackage(pkg) {
			pkg.Dir = abs(pkg.Dir)
			pkg.Module.Dir = abs(pkg.Module.Dir)
			pkg.Module.GoMod = abs(pkg.Module.GoMod)
		}
		out[importPath] = pkg
	}
	return out
}

// storedGraphValid rejects a decoded graph whose main-module paths could
// escape the module root when rebased.
func storedGraphValid(graph sourceview.Graph) bool {
	ok := func(rel string) bool {
		return rel == "" || (!filepath.IsAbs(rel) && !strings.Contains(rel, "\\") && path.Clean(rel) == rel && rel != ".." && !strings.HasPrefix(rel, "../"))
	}
	for importPath, pkg := range graph {
		if pkg.ImportPath != importPath {
			return false
		}
		if !isMainModulePackage(pkg) {
			continue
		}
		if !ok(pkg.Dir) || !ok(pkg.Module.Dir) || !ok(pkg.Module.GoMod) {
			return false
		}
	}
	return true
}

func decodeGraphEntry(data []byte) (graphEntry, bool) {
	var entry graphEntry
	if err := json.Unmarshal(data, &entry); err != nil {
		return graphEntry{}, false
	}
	if entry.Version != graphEntryVersion || len(entry.Variants) == 0 {
		return graphEntry{}, false
	}
	for _, variant := range entry.Variants {
		if variant.Digest == "" || variant.Graph == nil || !storedGraphValid(variant.Graph) {
			return graphEntry{}, false
		}
	}
	return entry, true
}

// lookupGraph returns the first stored variant whose witness digest, recomputed
// from manifest, equals the stored one, rebased onto goModDir. It reads only
// the entry file; a variant whose witness is unavailable simply does not match.
func lookupGraph(dir, key string, manifest *sourceview.Manifest, goModDir string) (sourceview.Graph, graphCacheOutcome) {
	data, err := os.ReadFile(entryPath(filepath.Join(dir, graphEntrySubdir), key))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, graphCacheOutcome{Reason: graphEntryMissing}
		}
		return nil, graphCacheOutcome{Reason: graphEntryCorrupt, Detail: err.Error()}
	}
	entry, ok := decodeGraphEntry(data)
	if !ok {
		return nil, graphCacheOutcome{Reason: graphEntryCorrupt}
	}
	for _, variant := range entry.Variants {
		digest, err := manifest.GraphWitness(variant.Witness)
		if err != nil || digest != variant.Digest {
			continue
		}
		return rebaseGraph(variant.Graph, goModDir), graphCacheOutcome{Reason: graphHit}
	}
	return nil, graphCacheOutcome{Reason: graphWitnessChanged}
}

// storeGraph writes variant at the front of the key's entry, dropping any
// stored variant with the same digest and keeping at most graphMaxVariants.
// An unreadable or corrupt existing entry is replaced.
func storeGraph(dir, key string, variant graphVariant) error {
	root := filepath.Join(dir, graphEntrySubdir)
	p := entryPath(root, key)
	entry := graphEntry{Version: graphEntryVersion, Variants: []graphVariant{variant}}
	if data, err := os.ReadFile(p); err == nil {
		if old, ok := decodeGraphEntry(data); ok {
			for _, v := range old.Variants {
				if v.Digest != variant.Digest && len(entry.Variants) < graphMaxVariants {
					entry.Variants = append(entry.Variants, v)
				}
			}
		}
	}
	encoded, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	return writeAtomic(dir, p, encoded)
}
