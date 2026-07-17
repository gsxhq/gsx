package sourceview

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// GoOverlay is a temporary cmd/go overlay projection of a Manifest. Backing
// paths are transport-only and never participate in source/cache identity.
type GoOverlay struct {
	dir  string
	path string
}

func (overlay *GoOverlay) Path() string {
	if overlay == nil {
		return ""
	}
	return overlay.path
}

func (overlay *GoOverlay) Close() error {
	if overlay == nil || overlay.dir == "" {
		return nil
	}
	err := os.RemoveAll(overlay.dir)
	overlay.dir = ""
	overlay.path = ""
	return err
}

// MaterializeGoOverlay writes an overlay JSON file and private backing files
// for cmd/go. packages.Load consumes Manifest.Overlay directly instead.
func (manifest *Manifest) MaterializeGoOverlay() (*GoOverlay, error) {
	if err := manifest.CheckReadable(); err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp("", "gsx-sourceview-overlay-")
	if err != nil {
		return nil, fmt.Errorf("sourceview: create Go overlay directory: %w", err)
	}
	fail := func(err error) (*GoOverlay, error) {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	replacements := make(map[string]string, len(manifest.overlay))
	for index, logical := range manifest.OverlayPaths() {
		rel, err := filepath.Rel(manifest.moduleRoot, logical)
		if err != nil || rel == ".." || filepath.IsAbs(rel) || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return fail(fmt.Errorf("sourceview: Go overlay target %s is outside logical module root %s", logical, manifest.moduleRoot))
		}
		transport := filepath.Join(manifest.physicalRoot, rel)
		backing := filepath.Join(dir, fmt.Sprintf("%06d.go", index))
		if err := os.WriteFile(backing, manifest.overlay[logical], 0o600); err != nil {
			return fail(fmt.Errorf("sourceview: write Go overlay backing for %s: %w", logical, err))
		}
		replacements[transport] = backing
	}
	data, err := json.Marshal(struct {
		Replace map[string]string
	}{Replace: replacements})
	if err != nil {
		return fail(fmt.Errorf("sourceview: encode Go overlay: %w", err))
	}
	path := filepath.Join(dir, "overlay.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fail(fmt.Errorf("sourceview: write Go overlay: %w", err))
	}
	return &GoOverlay{dir: dir, path: path}, nil
}
