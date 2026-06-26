//go:build !js

package codegen

import "os"

// diskExists reports whether p exists as a real file on disk. freeOverlayPath
// uses it to avoid colliding an overlay-only key with real package source.
func diskExists(p string) (bool, error) {
	_, err := os.Stat(p)
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}
