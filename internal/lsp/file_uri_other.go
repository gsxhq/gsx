//go:build !windows

package lsp

import "path/filepath"

func fileURIPathToPath(uriPath string) (string, bool) {
	path := filepath.FromSlash(uriPath)
	if path == "" || !filepath.IsAbs(path) {
		return "", false
	}
	return filepath.Clean(path), true
}

func filePathToURIPath(path string) (string, bool) {
	if path == "" || !filepath.IsAbs(path) {
		return "", false
	}
	return filepath.ToSlash(filepath.Clean(path)), true
}
