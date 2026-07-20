package sourceview

import (
	"path/filepath"
	"strings"
)

// PairedGSXPath returns the authoritative GSX sibling whose generated output
// path is exactly path. An .x.go suffix alone does not imply generated
// ownership: callers must also prove that the returned GSX path exists in
// their authoritative source view.
func PairedGSXPath(path string) (string, bool) {
	path = filepath.Clean(path)
	if !strings.HasSuffix(path, ".x.go") {
		return "", false
	}
	return strings.TrimSuffix(path, ".x.go") + ".gsx", true
}

// PairedGeneratedOutputPath returns the exact generated sibling owned by a GSX
// source path.
func PairedGeneratedOutputPath(path string) (string, bool) {
	path = filepath.Clean(path)
	if !strings.HasSuffix(path, ".gsx") {
		return "", false
	}
	return strings.TrimSuffix(path, ".gsx") + ".x.go", true
}
