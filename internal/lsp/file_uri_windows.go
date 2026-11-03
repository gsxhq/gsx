//go:build windows

package lsp

import "path/filepath"

func fileURIPathToPath(uriPath string) (string, bool) {
	path, ok := normalizeWindowsDriveURIPath(uriPath)
	if !ok {
		return "", false
	}
	path = filepath.FromSlash(path)
	if !filepath.IsAbs(path) {
		return "", false
	}
	return filepath.Clean(path), true
}

func filePathToURIPath(path string) (string, bool) {
	if path == "" || !filepath.IsAbs(path) {
		return "", false
	}
	return normalizeWindowsDrivePathForURI(filepath.ToSlash(filepath.Clean(path)))
}
