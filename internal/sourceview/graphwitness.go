package sourceview

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"maps"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
)

// GraphWitnessInput names what a stored go list graph read from the main
// module. Both lists hold module-relative slash paths ("." for the root).
// Dirs includes the absent "shadow" directories whose creation would make a
// graph import ambiguous; EmbedFiles are the graph's embedded files.
type GraphWitnessInput struct {
	Dirs       []string
	EmbedFiles []string
}

// ErrWitnessUnavailable reports that the manifest cannot prove what go list
// would read for the input, so the graph must not be reused or stored.
var ErrWitnessUnavailable = errors.New("sourceview: graph witness unavailable")

// GraphWitness digests everything go list reads from the input's main-module
// directories, from the manifest. Disk access is limited to an Lstat per
// EmbedFiles path and per directory the walk did not record, and to the
// listings (never file contents) of unimportable directories the walk skipped
// that an embed pattern reaches, such as a Vite static/.vite under all:.
func (manifest *Manifest) GraphWitness(input GraphWitnessInput) (string, error) {
	return manifest.witnessDigest(input, manifestWitnessSource{manifest: manifest})
}

// GraphWitnessFrom is GraphWitness with the listings of unwalked embedded
// directories taken from listings instead of the disk, so a graph store can
// witness what go list read rather than the tree after it ran. A directory
// listings did not capture is ErrListingNotCaptured.
func (manifest *Manifest) GraphWitnessFrom(input GraphWitnessInput, listings *UnwalkedListings) (string, error) {
	return manifest.witnessDigest(input, manifestWitnessSource{manifest: manifest, unwalked: listings.replay})
}

// DiskGraphWitness is GraphWitness over a fresh read of the input
// directories' listings and Go files. Overlaid paths still use their overlay
// bytes, so on an unchanged tree it equals GraphWitness.
func (manifest *Manifest) DiskGraphWitness(input GraphWitnessInput) (string, error) {
	return manifest.witnessDigest(input, diskWitnessSource{manifest})
}

// GraphStatPaths returns, sorted and as physical paths, what a graph store
// stats before and after go list to see edits a content re-read cannot (an
// edit reverted while go list ran): every directory the walk listed, every Go
// file in those listings, and the root go.mod and go.sum.
func (manifest *Manifest) GraphStatPaths() []string {
	abs := func(key string) string {
		return filepath.Join(manifest.physicalRoot, filepath.FromSlash(key))
	}
	paths := []string{abs("go.mod"), abs("go.sum")}
	for key, listing := range manifest.dirListings {
		paths = append(paths, abs(key))
		for _, name := range listing.files {
			if strings.HasSuffix(name, ".go") {
				paths = append(paths, abs(path.Join(key, name)))
			}
		}
	}
	slices.Sort(paths)
	return slices.Compact(paths)
}

// ErrListingNotCaptured reports that a witness reached an unwalked directory
// whose listing GraphWitnessFrom's capture does not hold.
var ErrListingNotCaptured = errors.New("sourceview: unwalked directory listing not captured")

// UnwalkedListings captures the listings of every directory the manifest
// walk did not enter that an embed pattern of a listed directory's Go files
// (test files included) reaches, read from disk as the witness reads them.
type UnwalkedListings struct {
	physicalRoot string
	nodes        map[string]capturedNode
}

// capturedNode is one unwalked directory as readDirNode returned it.
type capturedNode struct {
	node witnessNode
	err  error
}

// CaptureUnwalkedListings resolves the embed patterns of every listed
// directory's Go files, recording each unwalked directory listing the
// resolution reads. A directory whose witness fails is resolved up to the
// failure: its witness fails the same way when GraphWitnessFrom replays it.
func (manifest *Manifest) CaptureUnwalkedListings() *UnwalkedListings {
	listings := &UnwalkedListings{physicalRoot: manifest.physicalRoot, nodes: make(map[string]capturedNode)}
	overlayNames, err := manifest.witnessPrelude()
	if err != nil {
		// Every witness of this manifest fails before reaching a listing.
		return listings
	}
	src := manifestWitnessSource{manifest: manifest, unwalked: listings.record}
	keys := slices.Sorted(maps.Keys(manifest.dirListings))
	for _, key := range keys {
		node, err := src.node(key)
		if err != nil || node.kind != witnessListed {
			continue
		}
		w := witnessWriter{hash: sha256.New()}
		_ = manifest.writePresentDir(&w, key, node, overlayNames, src)
	}
	return listings
}

func (listings *UnwalkedListings) record(key string) (witnessNode, error) {
	if captured, ok := listings.nodes[key]; ok {
		return captured.node, captured.err
	}
	node, err := readDirNode(listings.physicalRoot, key, false)
	listings.nodes[key] = capturedNode{node: node, err: err}
	return node, err
}

func (listings *UnwalkedListings) replay(key string) (witnessNode, error) {
	captured, ok := listings.nodes[key]
	if !ok {
		return witnessNode{}, fmt.Errorf("%w: %s", ErrListingNotCaptured, key)
	}
	return captured.node, captured.err
}

// Paths returns the captured directories, sorted, as physical paths.
func (listings *UnwalkedListings) Paths() []string {
	paths := make([]string, 0, len(listings.nodes))
	for key := range listings.nodes {
		paths = append(paths, filepath.Join(listings.physicalRoot, filepath.FromSlash(key)))
	}
	slices.Sort(paths)
	return paths
}

// FirstDifference returns the first directory, in key order, that one
// capture holds and the other does not or whose listings differ.
func (listings *UnwalkedListings) FirstDifference(other *UnwalkedListings) (string, bool) {
	keys := slices.Concat(slices.Collect(maps.Keys(listings.nodes)), slices.Collect(maps.Keys(other.nodes)))
	slices.Sort(keys)
	for _, key := range slices.Compact(keys) {
		a, aok := listings.nodes[key]
		b, bok := other.nodes[key]
		if aok != bok || !a.equal(b) {
			return key, true
		}
	}
	return "", false
}

func (a capturedNode) equal(b capturedNode) bool {
	if (a.err == nil) != (b.err == nil) || (a.err != nil && a.err.Error() != b.err.Error()) {
		return false
	}
	return a.node.kind == b.node.kind && a.node.goMod == b.node.goMod &&
		slices.Equal(a.node.files, b.node.files) && slices.Equal(a.node.subdirs, b.node.subdirs) && slices.Equal(a.node.irregular, b.node.irregular)
}

// witnessNodeKind is how the walk would classify one directory.
type witnessNodeKind int

const (
	// witnessUnrecorded: the walk holds nothing for the directory; whether it
	// exists must be checked on disk.
	witnessUnrecorded witnessNodeKind = iota
	witnessListed
	witnessNestedModule
	witnessUnimportable
	// witnessMissing: an unwalked directory read from disk does not exist.
	witnessMissing
)

type witnessNode struct {
	kind      witnessNodeKind
	files     []string
	subdirs   []string
	irregular []string
	goMod     bool
	// unwalked marks a listing read from disk below a directory the manifest
	// walk skipped; its children are read from disk too.
	unwalked bool
}

// witnessSource is where witnessDigest reads directory state and non-overlaid
// Go file bytes from. Keys are validated module-relative slash paths.
type witnessSource interface {
	node(key string) (witnessNode, error)
	goFile(key, name string) ([]byte, error)
	// unwalkedNode reads the listing of a directory the manifest walk did
	// not enter, as readDirNode without the name rule.
	unwalkedNode(key string) (witnessNode, error)
}

func witnessUnavailable(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrWitnessUnavailable, fmt.Sprintf(format, args...))
}

// validWitnessKey reports whether key is a clean module-relative slash path
// that stays inside the root.
func validWitnessKey(key string) bool {
	if key == "." {
		return true
	}
	return key != "" && path.Clean(key) == key && !path.IsAbs(key) && key != ".." && !strings.HasPrefix(key, "../") && !strings.Contains(key, `\`)
}

func (manifest *Manifest) witnessDigest(input GraphWitnessInput, src witnessSource) (string, error) {
	overlayNames, err := manifest.witnessPrelude()
	if err != nil {
		return "", err
	}
	dirs, err := sortedWitnessKeys(input.Dirs)
	if err != nil {
		return "", err
	}
	embeds, err := sortedWitnessKeys(input.EmbedFiles)
	if err != nil {
		return "", err
	}

	w := witnessWriter{hash: sha256.New()}
	w.field("gsx-graph-witness-v2")
	w.count(len(dirs))
	for _, dir := range dirs {
		w.field(dir)
		if err := manifest.writeDirWitness(&w, dir, src, overlayNames); err != nil {
			return "", err
		}
	}
	// An embed pattern may name a directory, so a file added to or removed
	// from an embedded file's directory changes EmbedFiles and can fail a
	// live go list: witness each such directory's listing.
	embedDirs := make([]string, 0, len(embeds))
	for _, embed := range embeds {
		embedDirs = append(embedDirs, path.Dir(embed))
	}
	slices.Sort(embedDirs)
	embedDirs = slices.Compact(embedDirs)
	w.count(len(embedDirs))
	for _, dir := range embedDirs {
		w.field(dir)
		if err := manifest.writeEmbedDirWitness(&w, dir, src, overlayNames[dir]); err != nil {
			return "", err
		}
	}
	w.count(len(embeds))
	for _, embed := range embeds {
		w.field(embed)
		info, err := os.Lstat(filepath.Join(manifest.physicalRoot, filepath.FromSlash(embed)))
		switch {
		case err == nil:
			w.field("present")
			w.count(int(info.Mode().Type()))
		case pathAbsent(err):
			w.field("absent")
		default:
			return "", witnessUnavailable("inspect embedded file %s: %v", embed, err)
		}
	}
	return hex.EncodeToString(w.hash.Sum(nil)), nil
}

// witnessPrelude checks that the manifest's listings describe one walk of
// this root and indexes its overlay names by directory.
func (manifest *Manifest) witnessPrelude() (map[string][]string, error) {
	if !manifest.witnessable {
		return nil, witnessUnavailable("manifest listings do not describe the disk")
	}
	// A listing outside the root, or a directory both listed and skipped, means
	// the listings no longer describe one walk of this root.
	for key := range manifest.dirListings {
		if !validWitnessKey(key) {
			return nil, witnessUnavailable("listing %q is outside the module root", key)
		}
		if _, skipped := manifest.skippedDirs[key]; skipped {
			return nil, witnessUnavailable("directory %q is both listed and skipped", key)
		}
	}
	for key := range manifest.skippedDirs {
		if !validWitnessKey(key) {
			return nil, witnessUnavailable("skipped directory %q is outside the module root", key)
		}
	}
	return manifest.overlayNamesByDir()
}

// sortedWitnessKeys validates keys and returns them sorted and deduplicated.
func sortedWitnessKeys(keys []string) ([]string, error) {
	for _, key := range keys {
		if !validWitnessKey(key) {
			return nil, witnessUnavailable("path %q is not a clean module-relative path", key)
		}
	}
	sorted := slices.Clone(keys)
	slices.Sort(sorted)
	return slices.Compact(sorted), nil
}

// overlayNamesByDir indexes the overlay's base names by module-relative
// directory key. go list sees an overlaid path whether or not it is on disk.
func (manifest *Manifest) overlayNamesByDir() (map[string][]string, error) {
	names := make(map[string][]string)
	for logical := range manifest.overlay {
		key, err := relDirKey(manifest.moduleRoot, filepath.Dir(logical))
		if err != nil || !validWitnessKey(key) {
			return nil, witnessUnavailable("overlay path %s is outside the module root", logical)
		}
		names[key] = append(names[key], filepath.Base(logical))
	}
	return names, nil
}

// witnessPrefixes returns the keys from the root down to dir, inclusive.
func witnessPrefixes(dir string) []string {
	prefixes := []string{"."}
	if dir != "." {
		parts := strings.Split(dir, "/")
		for i := range parts {
			prefixes = append(prefixes, strings.Join(parts[:i+1], "/"))
		}
	}
	return prefixes
}

// writeDirWitness writes the state of every directory from the root down to
// dir: each recorded ancestor's go.mod flag, then how dir itself resolves.
func (manifest *Manifest) writeDirWitness(w *witnessWriter, dir string, src witnessSource, overlayNames map[string][]string) error {
	for _, prefix := range witnessPrefixes(dir) {
		node, err := src.node(prefix)
		if err != nil {
			return err
		}
		switch node.kind {
		case witnessListed:
			if node.goMod && prefix != "." {
				return witnessUnavailable("listed directory %s starts a module", prefix)
			}
			w.field("listed")
			w.flag(node.goMod)
			if prefix == dir {
				return manifest.writePresentDir(w, dir, node, overlayNames, src)
			}
		case witnessNestedModule:
			w.field("nested-module")
			return nil
		case witnessUnimportable:
			return witnessUnavailable("directory %s is not importable", prefix)
		case witnessUnrecorded:
			if prefix == "." {
				return witnessUnavailable("module root is not listed")
			}
			// The parent is listed, but RefreshDirs never records a new
			// subdirectory, so only the disk can prove absence.
			_, err := os.Lstat(filepath.Join(manifest.physicalRoot, filepath.FromSlash(prefix)))
			if err == nil {
				return witnessUnavailable("directory %s exists but was not walked", prefix)
			}
			if !pathAbsent(err) {
				return witnessUnavailable("inspect directory %s: %v", prefix, err)
			}
			w.field("absent")
			return nil
		}
	}
	return witnessUnavailable("directory %s did not resolve", dir)
}

// writeEmbedDirWitness writes the listing of a directory holding an embedded
// file. Every directory from the root down must be listed: an embedded file
// outside the walked tree cannot be witnessed.
func (manifest *Manifest) writeEmbedDirWitness(w *witnessWriter, dir string, src witnessSource, overlayNames []string) error {
	var node witnessNode
	for i, key := range witnessPrefixes(dir) {
		var err error
		if i == 0 {
			node, err = src.node(key)
		} else {
			node, err = manifest.witnessChild(src, node, key)
		}
		if err != nil {
			return err
		}
		if node.kind == witnessMissing {
			w.field("absent")
			return nil
		}
		if node.kind != witnessListed || (node.goMod && key != ".") {
			return witnessUnavailable("embedded directory %s is not listed", key)
		}
		if key == dir {
			names := slices.Concat(node.files, overlayNames)
			slices.Sort(names)
			names = slices.Compact(names)
			w.count(len(names))
			for _, name := range names {
				w.field(name)
			}
			return nil
		}
	}
	return witnessUnavailable("embedded directory %s did not resolve", dir)
}

// writePresentDir writes a directory's file names (its listing plus its
// overlaid paths) and each Go file's header, //go:embed lines and resolved
// embed patterns. _test.go files are included: go list parses their headers
// and resolves their embed patterns too, and a third package name, a broken
// import block or import "C" there fails it.
func (manifest *Manifest) writePresentDir(w *witnessWriter, dir string, node witnessNode, overlayNames map[string][]string, src witnessSource) error {
	names := slices.Concat(node.files, overlayNames[dir])
	slices.Sort(names)
	names = slices.Compact(names)
	w.count(len(names))
	for _, name := range names {
		// go/build reads build constraints from these files, and their mere
		// presence can fail a non-cgo package; witnessing them would mean
		// reading them on every hit.
		if constrainedSourceExts[filepath.Ext(name)] {
			return witnessUnavailable("directory %s holds non-Go source %s", dir, name)
		}
		w.field(name)
		w.flag(contains(node.irregular, name) && !slices.Contains(overlayNames[dir], name))
	}
	embeds := embedResolver{manifest: manifest, w: w, src: src, overlayNames: overlayNames, pkg: dir}
	for _, name := range names {
		if !strings.HasSuffix(name, ".go") {
			continue
		}
		source, overlaid := manifest.overlay[filepath.Join(manifest.moduleRoot, filepath.FromSlash(dir), name)]
		if !overlaid {
			var err error
			if source, err = src.goFile(dir, name); err != nil {
				return err
			}
		}
		header := parseGoHeader(source)
		if header.importsC && !strings.HasSuffix(name, "_test.go") {
			// cgo reads the headers its preamble includes, from anywhere.
			return witnessUnavailable("directory %s uses cgo in %s", dir, name)
		}
		w.field(name)
		w.bytes(header.header)
		lines := goEmbedLines(source)
		w.count(len(lines))
		for _, line := range lines {
			w.bytes(line)
		}
		var patterns []string
		if !header.parsed || header.importsEmbed {
			patterns = embedPatterns(source)
		}
		w.count(len(patterns))
		for _, pattern := range patterns {
			if err := embeds.writePattern(pattern); err != nil {
				return err
			}
		}
	}
	return nil
}

// constrainedSourceExts are the non-Go extensions go/build lists for a
// package and reads build constraints from (.syso is listed by name only).
var constrainedSourceExts = map[string]bool{
	".c": true, ".h": true, ".s": true, ".S": true,
	".cc": true, ".cpp": true, ".cxx": true, ".hh": true, ".hpp": true, ".hxx": true,
	".m": true, ".f": true, ".F": true, ".for": true, ".f90": true,
	".swig": true, ".swigcxx": true,
}

// manifestWitnessSource reads the walk's recorded state. unwalked, when set,
// replaces the disk read of unwalked directory listings.
type manifestWitnessSource struct {
	manifest *Manifest
	unwalked func(key string) (witnessNode, error)
}

func (src manifestWitnessSource) unwalkedNode(key string) (witnessNode, error) {
	if src.unwalked != nil {
		return src.unwalked(key)
	}
	return readDirNode(src.manifest.physicalRoot, key, false)
}

func (src manifestWitnessSource) node(key string) (witnessNode, error) {
	if listing, ok := src.manifest.dirListings[key]; ok {
		return witnessNode{kind: witnessListed, files: listing.files, subdirs: listing.subdirs, irregular: listing.irregular, goMod: listing.goMod}, nil
	}
	switch src.manifest.skippedDirs[key] {
	case skipNestedModule:
		return witnessNode{kind: witnessNestedModule}, nil
	case skipUnimportable:
		return witnessNode{kind: witnessUnimportable}, nil
	}
	return witnessNode{kind: witnessUnrecorded}, nil
}

func (src manifestWitnessSource) goFile(key, name string) ([]byte, error) {
	logical := filepath.Join(src.manifest.moduleRoot, filepath.FromSlash(key), name)
	snapshot, ok := src.manifest.helperGoFiles[logical]
	if !ok || snapshot.state != FilePresent {
		return nil, witnessUnavailable("Go file %s has no readable snapshot", logical)
	}
	return snapshot.source, nil
}

// diskWitnessSource classifies and lists directories as the Build walk would,
// from a fresh read of the physical tree.
type diskWitnessSource struct{ manifest *Manifest }

func (src diskWitnessSource) node(key string) (witnessNode, error) {
	node, err := readDirNode(src.manifest.physicalRoot, key, true)
	if node.kind == witnessMissing {
		// The walk never enters a symlink or a file either.
		node.kind = witnessUnrecorded
	}
	return node, err
}

// witnessChild resolves key, a subdirectory entry of the listed parent. The
// manifest walk does not enter unimportable directories, so a witness that
// reaches one (only embed resolution does) reads its listing, and every
// listing below it, through src.unwalkedNode: from disk unless a capture is
// replayed. Both witness sources go through here, so their digests agree by
// construction.
func (manifest *Manifest) witnessChild(src witnessSource, parent witnessNode, key string) (witnessNode, error) {
	if !parent.unwalked {
		node, err := src.node(key)
		if err != nil || node.kind != witnessUnimportable {
			return node, err
		}
	}
	node, err := src.unwalkedNode(key)
	if err != nil {
		return witnessNode{}, err
	}
	if node.kind == witnessUnrecorded {
		return witnessNode{}, witnessUnavailable("unwalked directory %s is not a directory", key)
	}
	node.unwalked = node.kind == witnessListed
	return node, nil
}

// readDirNode classifies the physical directory key as the Build walk would,
// reading only its listing. checkName applies the walk's importable-name
// rule; a missing directory is witnessMissing and a non-directory (the walk
// never follows a symlink) is witnessUnrecorded.
func readDirNode(physicalRoot, key string, checkName bool) (witnessNode, error) {
	physical := filepath.Join(physicalRoot, filepath.FromSlash(key))
	if key != "." {
		info, err := os.Lstat(physical)
		if err != nil {
			if pathAbsent(err) {
				return witnessNode{kind: witnessMissing}, nil
			}
			return witnessNode{}, witnessUnavailable("inspect directory %s: %v", key, err)
		}
		if !info.IsDir() {
			return witnessNode{kind: witnessUnrecorded}, nil
		}
		if checkName && !importableDirName(path.Base(key)) {
			return witnessNode{kind: witnessUnimportable}, nil
		}
		startsModule, err := directoryStartsModule(physical)
		if err != nil {
			return witnessNode{}, witnessUnavailable("%v", err)
		}
		if startsModule {
			return witnessNode{kind: witnessNestedModule}, nil
		}
	}
	entries, err := os.ReadDir(physical)
	if err != nil {
		return witnessNode{}, witnessUnavailable("read directory %s: %v", key, err)
	}
	files, subdirs, irregular := listEntries(entries)
	listing := freezeListing(key, files, subdirs, irregular)
	return witnessNode{kind: witnessListed, files: listing.files, subdirs: listing.subdirs, irregular: listing.irregular, goMod: listing.goMod}, nil
}

func (src diskWitnessSource) unwalkedNode(key string) (witnessNode, error) {
	return readDirNode(src.manifest.physicalRoot, key, false)
}

func (src diskWitnessSource) goFile(key, name string) ([]byte, error) {
	physical := filepath.Join(src.manifest.physicalRoot, filepath.FromSlash(key), name)
	source, err := os.ReadFile(physical)
	if err != nil {
		return nil, witnessUnavailable("read Go file %s: %v", physical, err)
	}
	return source, nil
}

// witnessWriter length-prefixes every field so concatenations are unambiguous.
type witnessWriter struct{ hash hash.Hash }

func (w *witnessWriter) count(n int) {
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], uint64(n))
	w.hash.Write(buf[:])
}

func (w *witnessWriter) bytes(b []byte) {
	w.count(len(b))
	w.hash.Write(b)
}

func (w *witnessWriter) field(s string) { w.bytes([]byte(s)) }

func (w *witnessWriter) flag(b bool) {
	if b {
		w.field("1")
	} else {
		w.field("0")
	}
}
