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

// computeKey is the per-package cache key. dir is the absolute package dir;
// graph maps import paths to info; modPath is the module path; goModHash/
// goSumHash/buildCtx/filterPkgs are the version pins. buildCtx is the output
// of buildContext() and subsumes GOVERSION, GOOS, GOARCH, CGO_ENABLED, etc.
// clsFingerprint is the attrclass.Classifier.Fingerprint() for the current run;
// it ensures a changed attribute classification rule invalidates cached output.
// codegenID is codegenIdentity() — "which generator produced this" — so any
// change to the gsx binary (emit/lowering) invalidates cached output even when
// the manual codegen.Version constant is not bumped.
func computeKey(dir string, graph map[string]pkgInfo, modPath, goModHash, goSumHash, buildCtx, codegenID string, filterPkgs []string, aliases []codegen.FilterAlias, clsFingerprint string, hasFieldMatcher bool) (string, error) {
	dir = filepath.Clean(dir)
	own, err := dirSourceHash(dir)
	if err != nil {
		return "", err
	}
	// find this package's import path by matching Dir.
	var self pkgInfo
	for _, p := range graph {
		if p.Dir == dir {
			self = p
			break
		}
	}
	// transitive in-module deps: self.Deps filtered to the module prefix.
	var depHashes []string
	for _, dep := range self.Deps {
		if dep == self.ImportPath {
			continue
		}
		if dep != modPath && !strings.HasPrefix(dep, modPath+"/") {
			continue // external / stdlib — pinned by goMod/goSum/goVersion
		}
		dp, ok := graph[dep]
		if !ok || dp.Dir == "" {
			continue
		}
		dh, err := dirSourceHash(dp.Dir)
		if err != nil {
			return "", err
		}
		depHashes = append(depHashes, dep+":"+dh)
	}
	sort.Strings(depHashes)

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
	h := sha256.New()
	// codegenID (codegenIdentity) folds in BOTH the manual codegen.Version and the
	// gsx binary hash, so it supersedes a bare Version() pin: any emit/lowering
	// change auto-invalidates even when the constant is not bumped.
	fmt.Fprintf(h, "gsxcache-v1\x00%s\x00%s\x00%s\x00%s\x00", codegenID, buildCtx, goModHash, goSumHash)
	fmt.Fprintf(h, "filters=%s\x00aliases=%s\x00cls=%s\x00fm=%s\x00own=%s\x00", strings.Join(pins, "\x00"), strings.Join(aliasPins, "\x00"), clsFingerprint, fmStr, own)
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

// fileHashOrEmpty hashes a file's bytes, returning "" if absent (go.sum may not exist).
func fileHashOrEmpty(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
