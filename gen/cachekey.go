package gen

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/gsxhq/gsx/internal/codegen"
)

var (
	selfHashOnce sync.Once
	selfHashVal  string
)

// selfHash returns a content hash of the running gsx executable, memoized for
// the process. Folded into every cache key so ANY change to the gsx binary —
// including a codegen/emit change, even an uncommitted dev rebuild — invalidates
// cached output automatically. This is the robust backstop the manual
// codegen.Version constant alone cannot provide: a human who forgets to bump the
// constant after changing emit (e.g. 5aed1ba) would otherwise serve stale .x.go.
// Returns "" if the executable can't be located or read; the caller still has
// codegen.Version() as a coarse lever in that case.
func selfHash() string {
	selfHashOnce.Do(func() {
		exe, err := os.Executable()
		if err != nil {
			return
		}
		data, err := os.ReadFile(exe)
		if err != nil {
			return
		}
		sum := sha256.Sum256(data)
		selfHashVal = hex.EncodeToString(sum[:])
	})
	return selfHashVal
}

// codegenIdentity is the cache pin for "which generator produced this output".
// It composes the manual codegen.Version() (a coarse, explicit invalidation
// lever for project-wide busts) with selfHash() (automatic invalidation on any
// binary change). Folded into every cache key via computeKey.
func codegenIdentity() string {
	return "v=" + codegen.Version() + "\x00bin=" + selfHash()
}

// buildContext returns a stable string capturing the Go build context that can
// affect type resolution (and thus generated output). Folded into every cache
// key so a different GOOS/GOARCH/tags/CGO/etc. never collides on the same key.
func buildContext(moduleRoot string) string {
	cmd := exec.Command("go", "env", "GOVERSION", "GOOS", "GOARCH", "CGO_ENABLED", "GOFLAGS", "GOEXPERIMENT")
	cmd.Dir = moduleRoot
	out, err := cmd.Output()
	if err != nil {
		// Uncertain context → return a unique-ish marker so we don't share a key
		// with a real context (caller still caches, but conservatively).
		return "buildctx-unknown"
	}
	return strings.TrimSpace(string(out))
}

type pkgInfo struct {
	ImportPath string
	Dir        string
	Deps       []string
}

// loadGraph runs `go list -json ./...` (metadata only — NO -export, no compile)
// from moduleRoot and returns importPath -> info (Dir + transitive Deps).
func loadGraph(moduleRoot string) (map[string]pkgInfo, error) {
	cmd := exec.Command("go", "list", "-json", "./...")
	cmd.Dir = moduleRoot
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("gen: go list: %w", err)
	}
	graph := map[string]pkgInfo{}
	dec := json.NewDecoder(strings.NewReader(string(out)))
	for dec.More() {
		var p pkgInfo
		if err := dec.Decode(&p); err != nil {
			return nil, fmt.Errorf("gen: decode go list: %w", err)
		}
		graph[p.ImportPath] = p
	}
	return graph, nil
}

// dirSourceHash hashes a package dir's .gsx + .go source (excluding generated
// .x.go), name-sorted, content-addressed.
func dirSourceHash(dir string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		n := e.Name()
		if strings.HasSuffix(n, ".x.go") {
			continue // generated output is not an input
		}
		if strings.HasSuffix(n, ".gsx") || strings.HasSuffix(n, ".go") {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	h := sha256.New()
	for _, n := range names {
		data, err := os.ReadFile(filepath.Join(dir, n))
		if err != nil {
			return "", err
		}
		fmt.Fprintf(h, "%s\x00%d\x00", n, len(data))
		h.Write(data)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// gsxDepDirs returns every in-module package dir reachable from dir through
// the union of .gsx-hoisted import edges and go-list dep edges, excluding dir
// itself. go list alone misses a dep whose only edge is a .gsx import with no
// .x.go on disk, so the walk follows both edge kinds transitively.
func gsxDepDirs(dir string, graph map[string]pkgInfo, moduleRoot, modPath string) []string {
	byDir := map[string]pkgInfo{}
	for _, p := range graph {
		if p.Dir != "" {
			byDir[filepath.Clean(p.Dir)] = p
		}
	}
	dirFor := func(importPath string) (string, bool) {
		if importPath == modPath {
			return filepath.Clean(moduleRoot), true
		}
		if !strings.HasPrefix(importPath, modPath+"/") {
			return "", false
		}
		rel := strings.TrimPrefix(importPath, modPath+"/")
		return filepath.Join(moduleRoot, filepath.FromSlash(rel)), true
	}
	dir = filepath.Clean(dir)
	seen := map[string]bool{dir: true}
	queue := []string{dir}
	var out []string
	for len(queue) > 0 {
		d := queue[0]
		queue = queue[1:]
		var neighborPaths []string
		neighborPaths = append(neighborPaths, codegen.GsxHoistedImportPaths(d)...)
		if p, ok := byDir[d]; ok {
			neighborPaths = append(neighborPaths, p.Deps...)
		}
		for _, ip := range neighborPaths {
			dd, ok := dirFor(ip)
			if !ok || seen[dd] {
				continue
			}
			if _, err := os.Stat(dd); err != nil {
				continue // import of a not-yet-existing package: nothing to hash
			}
			seen[dd] = true
			out = append(out, dd)
			queue = append(queue, dd)
		}
	}
	sort.Strings(out)
	return out
}

// rendererDepDirs returns the existing module-owned package directories named
// by the completed renderer registry. Registrations resolve last-wins per
// TypeKey before package ownership is considered, so a shadowed local renderer
// contributes no source dependency. Import paths outside modPath contribute
// nothing: ownership is exact module-path identity, never a sibling-directory
// or package-name guess.
func rendererDepDirs(renderers []codegen.RendererAlias, moduleRoot, modPath string) []string {
	final := make(map[string]codegen.RendererAlias, len(renderers))
	for _, r := range renderers {
		final[r.TypeKey] = r
	}
	dirs := make(map[string]bool, len(final))
	for _, r := range final {
		dir, ok := moduleDirForImportPath(moduleRoot, modPath, r.PkgPath)
		if !ok {
			continue
		}
		dirs[dir] = true
	}
	out := make([]string, 0, len(dirs))
	for dir := range dirs {
		out = append(out, dir)
	}
	sort.Strings(out)
	return out
}

// computeKey is the per-package cache key. dir is the absolute package dir;
// graph maps import paths to info; modPath is the module path; goModHash/
// goSumHash/buildCtx/filterPkgs are the version pins. buildCtx is the output
// of buildContext() and subsumes GOVERSION, GOOS, GOARCH, CGO_ENABLED, etc.
// clsFingerprint is the attrclass.Classifier.Fingerprint() for the current run;
// it ensures a changed attribute classification rule invalidates cached output.
// codegenID is codegenIdentity() — "which generator produced this" — so any
// change to the gsx binary (emit/lowering) invalidates cached output even when
// the manual codegen.Version constant is not bumped. renderers is the merged
// [renderers]/WithRenderer registration list (see the renderers= pin below).
func computeKey(dir string, graph map[string]pkgInfo, modPath, goModHash, goSumHash, buildCtx, codegenID string, filterPkgs []string, aliases []codegen.FilterAlias, renderers []codegen.RendererAlias, clsFingerprint string, hasFieldMatcher bool, cssMinify, jsMinify bool, classMerger *codegen.ClassMergerRef, moduleRoot string) (string, error) {
	dir = filepath.Clean(dir)
	own, err := dirSourceHash(dir)
	if err != nil {
		return "", err
	}
	// Dep hashes: every in-module package reachable through go-list edges OR
	// .gsx-hoisted import edges. The .gsx walk (gsxDepDirs) is what keeps the
	// key honest when a dep's .x.go is not on disk (fresh checkout, cleaned
	// outputs): the dep's component prop fields drive this package's attr
	// splitting, so its .gsx content must be an input to the key.
	depDirs := make(map[string]bool)
	for _, depDir := range gsxDepDirs(dir, graph, moduleRoot, modPath) {
		depDirs[filepath.Clean(depDir)] = true
	}
	for _, depDir := range rendererDepDirs(renderers, moduleRoot, modPath) {
		depDir = filepath.Clean(depDir)
		if depDir != dir {
			depDirs[depDir] = true
		}
	}
	sortedDepDirs := make([]string, 0, len(depDirs))
	for depDir := range depDirs {
		sortedDepDirs = append(sortedDepDirs, depDir)
	}
	sort.Strings(sortedDepDirs)
	var depHashes []string
	for _, depDir := range sortedDepDirs {
		dh, err := dirSourceHash(depDir)
		if err != nil {
			return "", err
		}
		rel, rerr := filepath.Rel(moduleRoot, depDir)
		if rerr != nil {
			rel = depDir
		}
		depHashes = append(depHashes, filepath.ToSlash(rel)+":"+dh)
	}
	pins := dedupSorted(filterPkgs)
	fmStr := "0"
	if hasFieldMatcher {
		fmStr = "1"
	}
	// Fold the explicit WithFilter aliases (name+pkgPath+funcName, in registration
	// order) into the key so a changed alias invalidates cached output. Order is
	// significant (last-wins), so this is NOT sorted — mirror the registration
	// order the resolver sees.
	var aliasPins []string
	for _, a := range aliases {
		aliasPins = append(aliasPins, a.Name+"="+a.PkgPath+"."+a.FuncName)
	}
	// renderers= pin: last-wins per TypeKey resolved FIRST, then sorted by
	// TypeKey — UNLIKE aliases= above (registration order is meaning there),
	// the renderer table is a per-key map, so two configs with the same final
	// table (regardless of file/option split or registration order) must hash
	// identically.
	finalRenderers := map[string]codegen.RendererAlias{}
	for _, r := range renderers {
		finalRenderers[r.TypeKey] = r
	}
	rendererTypeKeys := make([]string, 0, len(finalRenderers))
	for k := range finalRenderers {
		rendererTypeKeys = append(rendererTypeKeys, k)
	}
	sort.Strings(rendererTypeKeys)
	var rendererPins []string
	for _, k := range rendererTypeKeys {
		r := finalRenderers[k]
		rendererPins = append(rendererPins, k+"="+r.PkgPath+"."+r.FuncName)
	}
	cm := "cm="
	if classMerger != nil {
		cm += classMerger.PkgPath + "." + classMerger.FuncName
	}
	h := sha256.New()
	// codegenID (codegenIdentity) folds in BOTH the manual codegen.Version and the
	// gsx binary hash, so it supersedes a bare Version() pin: any emit/lowering
	// change auto-invalidates even when the constant is not bumped.
	fmt.Fprintf(h, "gsxcache-v1\x00%s\x00%s\x00%s\x00%s\x00", codegenID, buildCtx, goModHash, goSumHash)
	fmt.Fprintf(h, "filters=%s\x00aliases=%s\x00renderers=%s\x00cls=%s\x00fm=%s\x00minify=css:%d,js:%d\x00%s\x00own=%s\x00", strings.Join(pins, "\x00"), strings.Join(aliasPins, "\x00"), strings.Join(rendererPins, "\x00"), clsFingerprint, fmStr, b2i(cssMinify), b2i(jsMinify), cm, own)
	for _, d := range depHashes {
		fmt.Fprintf(h, "dep=%s\x00", d)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func dedupSorted(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}

// b2i maps a bool to 1/0 for stable inclusion in the cache-key digest.
func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

// fileHashOrEmpty hashes a file's bytes, returning "" if absent (go.sum may not exist).
func fileHashOrEmpty(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
