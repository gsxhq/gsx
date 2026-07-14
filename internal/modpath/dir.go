// Package modpath maps canonical Go import paths to module-local directories.
package modpath

import (
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/mod/module"
)

// DirForImportPath maps a canonical module-local import path to its lexical
// directory under moduleRoot. The directory may not exist yet, but both its
// lexical path and the path obtained by resolving its deepest existing ancestor
// must remain contained by the module root.
func DirForImportPath(moduleRoot, modulePath, importPath string) (string, bool) {
	if moduleRoot == "" || module.CheckImportPath(modulePath) != nil || module.CheckImportPath(importPath) != nil {
		return "", false
	}

	var rel string
	switch {
	case importPath == modulePath:
	case strings.HasPrefix(importPath, modulePath+"/"):
		rel = strings.TrimPrefix(importPath, modulePath+"/")
	default:
		return "", false
	}

	root, err := filepath.Abs(moduleRoot)
	if err != nil {
		return "", false
	}
	root = filepath.Clean(root)
	candidate := root
	if rel != "" {
		candidate = filepath.Join(root, filepath.FromSlash(rel))
	}
	if !containedBy(root, candidate) {
		return "", false
	}

	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", false
	}
	resolvedCandidate, ok := resolveExistingAncestor(candidate)
	if !ok || !containedBy(resolvedRoot, resolvedCandidate) {
		return "", false
	}
	return candidate, true
}

// resolveExistingAncestor resolves candidate through its deepest existing
// ancestor, then reapplies the missing suffix. Using Lstat ensures a broken
// symlink is rejected rather than treated as a missing path component.
func resolveExistingAncestor(candidate string) (string, bool) {
	ancestor := candidate
	for {
		_, err := os.Lstat(ancestor)
		switch {
		case err == nil:
			resolved, err := filepath.EvalSymlinks(ancestor)
			if err != nil {
				return "", false
			}
			info, err := os.Stat(resolved)
			if err != nil || !info.IsDir() {
				return "", false
			}
			suffix, err := filepath.Rel(ancestor, candidate)
			if err != nil || filepath.IsAbs(suffix) {
				return "", false
			}
			if suffix == "." {
				return filepath.Clean(resolved), true
			}
			return filepath.Clean(filepath.Join(resolved, suffix)), true
		case os.IsNotExist(err):
			parent := filepath.Dir(ancestor)
			if parent == ancestor {
				return "", false
			}
			ancestor = parent
		default:
			return "", false
		}
	}
}

func containedBy(root, candidate string) bool {
	root, err := filepath.Abs(root)
	if err != nil {
		return false
	}
	candidate, err = filepath.Abs(candidate)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(candidate))
	if err != nil || filepath.IsAbs(rel) {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
