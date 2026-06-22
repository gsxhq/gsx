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

	"github.com/gsxhq/gsx/internal/codegen"
)

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
// goSumHash/goVersion/filterPkgs are the version pins.
func computeKey(dir string, graph map[string]pkgInfo, modPath, goModHash, goSumHash, goVersion string, filterPkgs []string) (string, error) {
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
	h := sha256.New()
	fmt.Fprintf(h, "gsxcache-v1\x00%s\x00%s\x00%s\x00%s\x00", codegen.Version(), goVersion, goModHash, goSumHash)
	fmt.Fprintf(h, "filters=%s\x00own=%s\x00", strings.Join(pins, ","), own)
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
