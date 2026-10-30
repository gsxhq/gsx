package sourceview

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
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
// directories, from the manifest alone. The only disk access is an Lstat per
// EmbedFiles path and per directory the walk did not record.
func (manifest *Manifest) GraphWitness(input GraphWitnessInput) (string, error) {
	return manifest.witnessDigest(input, manifestWitnessSource{manifest})
}

// DiskGraphWitness is GraphWitness over a fresh read of the input
// directories' listings and Go files. Overlaid paths still use their overlay
// bytes, so on an unchanged tree it equals GraphWitness.
func (manifest *Manifest) DiskGraphWitness(input GraphWitnessInput) (string, error) {
	return manifest.witnessDigest(input, diskWitnessSource{manifest})
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
)

type witnessNode struct {
	kind  witnessNodeKind
	files []string
	goMod bool
}

// witnessSource is where witnessDigest reads directory state and non-overlaid
// Go file bytes from. Keys are validated module-relative slash paths.
type witnessSource interface {
	node(key string) (witnessNode, error)
	goFile(key, name string) ([]byte, error)
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
	if !manifest.witnessable {
		return "", witnessUnavailable("manifest listings do not describe the disk")
	}
	// A listing outside the root, or a directory both listed and skipped, means
	// the listings no longer describe one walk of this root.
	for key := range manifest.dirListings {
		if !validWitnessKey(key) {
			return "", witnessUnavailable("listing %q is outside the module root", key)
		}
		if _, skipped := manifest.skippedDirs[key]; skipped {
			return "", witnessUnavailable("directory %q is both listed and skipped", key)
		}
	}
	for key := range manifest.skippedDirs {
		if !validWitnessKey(key) {
			return "", witnessUnavailable("skipped directory %q is outside the module root", key)
		}
	}
	dirs, err := sortedWitnessKeys(input.Dirs)
	if err != nil {
		return "", err
	}
	embeds, err := sortedWitnessKeys(input.EmbedFiles)
	if err != nil {
		return "", err
	}
	overlayNames, err := manifest.overlayNamesByDir()
	if err != nil {
		return "", err
	}

	w := witnessWriter{hash: sha256.New()}
	w.field("gsx-graph-witness-v1")
	w.count(len(dirs))
	for _, dir := range dirs {
		w.field(dir)
		if err := manifest.writeDirWitness(&w, dir, src, overlayNames[dir]); err != nil {
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

// writeDirWitness writes the state of every directory from the root down to
// dir: each recorded ancestor's go.mod flag, then how dir itself resolves.
func (manifest *Manifest) writeDirWitness(w *witnessWriter, dir string, src witnessSource, overlayNames []string) error {
	prefixes := []string{"."}
	if dir != "." {
		parts := strings.Split(dir, "/")
		for i := range parts {
			prefixes = append(prefixes, strings.Join(parts[:i+1], "/"))
		}
	}
	for _, prefix := range prefixes {
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
				return manifest.writePresentDir(w, dir, node.files, overlayNames, src)
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

// writePresentDir writes a directory's file names (its listing plus its
// overlaid paths) and each Go file's header and //go:embed lines. _test.go
// files are included: go list parses their headers too, and a third package
// name, a broken import block or import "C" there fails it.
func (manifest *Manifest) writePresentDir(w *witnessWriter, dir string, files, overlayNames []string, src witnessSource) error {
	names := slices.Concat(files, overlayNames)
	slices.Sort(names)
	names = slices.Compact(names)
	w.count(len(names))
	for _, name := range names {
		w.field(name)
	}
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
		w.field(name)
		w.bytes(goFileHeader(source))
		embeds := goEmbedLines(source)
		w.count(len(embeds))
		for _, line := range embeds {
			w.bytes(line)
		}
	}
	return nil
}

// manifestWitnessSource reads the walk's recorded state.
type manifestWitnessSource struct{ manifest *Manifest }

func (src manifestWitnessSource) node(key string) (witnessNode, error) {
	if listing, ok := src.manifest.dirListings[key]; ok {
		return witnessNode{kind: witnessListed, files: listing.files, goMod: listing.goMod}, nil
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
	physical := filepath.Join(src.manifest.physicalRoot, filepath.FromSlash(key))
	if key != "." {
		info, err := os.Lstat(physical)
		if err != nil {
			if pathAbsent(err) {
				return witnessNode{kind: witnessUnrecorded}, nil
			}
			return witnessNode{}, witnessUnavailable("inspect directory %s: %v", key, err)
		}
		if !info.IsDir() {
			// The walk never enters a symlink or a file.
			return witnessNode{kind: witnessUnrecorded}, nil
		}
		if !importableDirName(path.Base(key)) {
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
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			names = append(names, entry.Name())
		}
	}
	listing := freezeListing(key, names)
	return witnessNode{kind: witnessListed, files: listing.files, goMod: listing.goMod}, nil
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
