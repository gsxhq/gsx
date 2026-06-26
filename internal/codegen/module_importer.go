package codegen

import (
	"path/filepath"
	"strings"
)

// importPathForDir maps an absolute package dir under moduleRoot to its Go
// import path. ok is false when dir is not within moduleRoot.
func importPathForDir(moduleRoot, modulePath, dir string) (string, bool) {
	rel, err := filepath.Rel(moduleRoot, dir)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	if rel == "." {
		return modulePath, true
	}
	return modulePath + "/" + filepath.ToSlash(rel), true
}

// dirForImportPath is the inverse of importPathForDir. ok is false when
// importPath is not under modulePath (e.g. stdlib or third-party).
func dirForImportPath(moduleRoot, modulePath, importPath string) (string, bool) {
	if importPath == modulePath {
		return moduleRoot, true
	}
	prefix := modulePath + "/"
	if !strings.HasPrefix(importPath, prefix) {
		return "", false
	}
	rel := strings.TrimPrefix(importPath, prefix)
	return filepath.Join(moduleRoot, filepath.FromSlash(rel)), true
}
