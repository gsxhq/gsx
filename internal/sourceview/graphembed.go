package sourceview

import (
	"bytes"
	"go/ast"
	"go/scanner"
	"go/token"
	"io/fs"
	"path"
	"slices"
	"strings"

	"golang.org/x/mod/module"
)

// This file ports what go list reads for //go:embed: go/build's pattern
// extraction (go/build/read.go readGoInfo, parseGoEmbed) and cmd/go's pattern
// resolution (cmd/go/internal/load/pkg.go resolveEmbed, over
// cmd/go/internal/fsys.Glob), evaluated over witnessSource directory nodes
// instead of the file system. Every fact resolveEmbed acts on (each match,
// its kind, each walked file and directory, every module boundary) is
// written to the witness; whatever the nodes cannot answer is
// ErrWitnessUnavailable.

// embedPatterns returns the //go:embed patterns go/build reads from a file
// that imports "embed": the arguments of every line comment starting with
// //go:embed, anywhere in the file. Badly formed lines are ignored, as go/build
// does; the compiler reports them.
func embedPatterns(src []byte) []string {
	if !bytes.Contains(src, []byte("//go:embed")) {
		return nil
	}
	fset := token.NewFileSet()
	file := fset.AddFile("", -1, len(src))
	var sc scanner.Scanner
	sc.Init(file, src, nil, scanner.ScanComments)
	var patterns []string
	for {
		pos, tok, lit := sc.Scan()
		if tok == token.EOF {
			break
		}
		if tok != token.COMMENT || !strings.HasPrefix(lit, "//go:embed") {
			continue
		}
		dir, ok := ast.ParseDirective(pos, lit)
		if !ok || dir.Tool != "go" || dir.Name != "embed" {
			continue
		}
		args, err := dir.ParseArgs()
		if err != nil {
			continue
		}
		for _, arg := range args {
			patterns = append(patterns, arg.Arg)
		}
	}
	return patterns
}

// isBadEmbedName is cmd/go's: a name that cannot or will not be packaged
// into a module.
func isBadEmbedName(name string) bool {
	if err := module.CheckFilePath(name); err != nil {
		return true
	}
	switch name {
	case "", ".bzr", ".hg", ".git", ".svn":
		return true
	}
	return false
}

// hasGlobMeta is fsys.hasMeta on a non-Windows host.
func hasGlobMeta(pattern string) bool {
	return strings.ContainsAny(pattern, `*?[\`)
}

type embedEntryKind int

const (
	embedEntryNone embedEntryKind = iota
	embedEntryFile
	embedEntryIrregular
	embedEntryDir
)

// embedResolver resolves one package's patterns. Paths are slash paths
// relative to the package directory pkg, "." for pkg itself.
type embedResolver struct {
	manifest     *Manifest
	w            *witnessWriter
	src          witnessSource
	overlayNames map[string][]string
	pkg          string
}

func (r *embedResolver) key(rel string) string { return path.Join(r.pkg, rel) }

// entries returns a listed directory's names as go list sees them: its
// listing plus the overlay paths in it.
func (r *embedResolver) entries(key string, node witnessNode) []string {
	names := slices.Concat(node.files, node.subdirs, r.overlayNames[key])
	slices.Sort(names)
	return slices.Compact(names)
}

// kind classifies name inside the listed directory key. Overlaid paths are
// regular files to go list.
func (r *embedResolver) kind(key string, node witnessNode, name string) embedEntryKind {
	switch {
	case slices.Contains(r.overlayNames[key], name):
		return embedEntryFile
	case contains(node.subdirs, name):
		return embedEntryDir
	case contains(node.irregular, name):
		return embedEntryIrregular
	case contains(node.files, name):
		return embedEntryFile
	}
	return embedEntryNone
}

func contains(sorted []string, name string) bool {
	_, found := slices.BinarySearch(sorted, name)
	return found
}

// dir resolves rel as fsys.Stat would for a directory: ok is false when rel
// does not exist or is not a directory. A step through a symlink, a module
// boundary or an unwalked directory cannot be witnessed.
func (r *embedResolver) dir(rel string) (witnessNode, bool, error) {
	key := r.pkg
	node, err := r.src.node(key)
	if err != nil {
		return witnessNode{}, false, err
	}
	if rel == "." {
		return node, true, nil
	}
	for part := range strings.SplitSeq(rel, "/") {
		switch r.kind(key, node, part) {
		case embedEntryNone, embedEntryFile:
			return witnessNode{}, false, nil
		case embedEntryIrregular:
			return witnessNode{}, false, witnessUnavailable("embed path %s crosses irregular file %s", path.Join(r.pkg, rel), path.Join(key, part))
		}
		key = path.Join(key, part)
		if node, err = r.manifest.witnessChild(r.src, node, key); err != nil {
			return witnessNode{}, false, err
		}
		if node.kind == witnessMissing {
			return witnessNode{}, false, nil
		}
		if node.kind != witnessListed || node.goMod {
			return witnessNode{}, false, witnessUnavailable("embed path %s enters unlisted directory %s", path.Join(r.pkg, rel), key)
		}
	}
	return node, true, nil
}

// glob is fsys.Glob relative to the package directory.
func (r *embedResolver) glob(pattern string) ([]string, error) {
	if !hasGlobMeta(pattern) {
		parent, name := path.Split(pattern)
		node, ok, err := r.dir(cleanGlobPath(parent))
		if err != nil || !ok {
			return nil, err
		}
		if r.kind(r.key(cleanGlobPath(parent)), node, name) == embedEntryNone {
			return nil, nil
		}
		return []string{pattern}, nil
	}
	parent, file := path.Split(pattern)
	parent = cleanGlobPath(parent)
	if !hasGlobMeta(parent) {
		return r.globIn(parent, file, nil)
	}
	if parent == pattern {
		return nil, witnessUnavailable("embed pattern %s recurses", pattern)
	}
	dirs, err := r.glob(parent)
	if err != nil {
		return nil, err
	}
	var matches []string
	for _, dir := range dirs {
		if matches, err = r.globIn(dir, file, matches); err != nil {
			return nil, err
		}
	}
	return matches, nil
}

// cleanGlobPath is fsys.cleanGlobPath for slash paths.
func cleanGlobPath(dir string) string {
	if dir == "" {
		return "."
	}
	return strings.TrimSuffix(dir, "/")
}

// globIn is fsys's glob: the names in dir matching pattern, in order.
func (r *embedResolver) globIn(dir, pattern string, matches []string) ([]string, error) {
	node, ok, err := r.dir(dir)
	if err != nil || !ok {
		return matches, err
	}
	for _, name := range r.entries(r.key(dir), node) {
		matched, err := path.Match(pattern, name)
		if err != nil {
			return nil, witnessUnavailable("embed pattern %s: %v", pattern, err)
		}
		if matched {
			matches = append(matches, path.Join(dir, name))
		}
	}
	return matches, nil
}

// writePattern writes how resolveEmbed resolves one pattern.
func (r *embedResolver) writePattern(pattern string) error {
	r.w.field(pattern)
	glob, all := strings.CutPrefix(pattern, "all:")
	if _, err := path.Match(glob, ""); err != nil || glob == "." || !fs.ValidPath(glob) {
		// go list rejects the pattern; the pattern text alone is the witness.
		r.w.field("invalid")
		return nil
	}
	matches, err := r.glob(glob)
	if err != nil {
		return err
	}
	r.w.count(len(matches))
	for _, rel := range matches {
		r.w.field(rel)
		if slices.ContainsFunc(strings.Split(rel, "/"), isBadEmbedName) {
			r.w.field("bad-name")
			continue
		}
		parent, name := path.Split(rel)
		node, _, err := r.dir(cleanGlobPath(parent))
		if err != nil {
			return err
		}
		switch r.kind(r.key(cleanGlobPath(parent)), node, name) {
		case embedEntryFile:
			r.w.field("file")
		case embedEntryDir:
			if err := r.writeMatchedDir(node, rel, all); err != nil {
				return err
			}
		default:
			// A symlink is an error or, under GODEBUG embedfollowsymlinks=1,
			// a read of its target.
			return witnessUnavailable("embed match %s is not a regular file or directory", r.key(rel))
		}
	}
	return nil
}

// writeMatchedDir writes a matched directory: a module boundary, or the walk
// resolveEmbed makes of it.
func (r *embedResolver) writeMatchedDir(parent witnessNode, rel string, all bool) error {
	node, err := r.manifest.witnessChild(r.src, parent, r.key(rel))
	if err != nil {
		return err
	}
	switch {
	case node.kind == witnessMissing:
		r.w.field("absent")
		return nil
	case node.kind == witnessNestedModule:
		r.w.field("boundary")
		return nil
	case node.kind == witnessListed && !node.goMod:
		r.w.field("dir")
		return r.walk(r.key(rel), node, all)
	}
	return witnessUnavailable("embedded directory %s is not listed", r.key(rel))
}

// walk is resolveEmbed's WalkDir over a matched directory: hidden names are
// skipped unless all is set, bad names skip directories and fail files,
// irregular files are ignored, and a nested module is not entered.
func (r *embedResolver) walk(key string, node witnessNode, all bool) error {
	skip := func(name string) bool {
		return isBadEmbedName(name) || ((name[0] == '.' || name[0] == '_') && !all)
	}
	files := slices.Concat(node.files, r.overlayNames[key])
	slices.Sort(files)
	files = slices.Compact(files)
	for _, name := range files {
		switch {
		case skip(name):
			if name[0] != '.' && name[0] != '_' {
				r.w.field("bad-name")
				r.w.field(name)
			}
		case r.kind(key, node, name) == embedEntryFile:
			r.w.field("file")
			r.w.field(name)
		}
	}
	for _, name := range node.subdirs {
		if skip(name) {
			continue
		}
		child := path.Join(key, name)
		childNode, err := r.manifest.witnessChild(r.src, node, child)
		if err != nil {
			return err
		}
		switch {
		case childNode.kind == witnessMissing:
			r.w.field("absent")
			r.w.field(name)
		case childNode.kind == witnessNestedModule:
			r.w.field("boundary")
			r.w.field(name)
		case childNode.kind == witnessListed && !childNode.goMod:
			r.w.field("dir")
			r.w.field(name)
			if err := r.walk(child, childNode, all); err != nil {
				return err
			}
		default:
			return witnessUnavailable("embedded directory %s is not listed", child)
		}
	}
	r.w.field("end")
	return nil
}
