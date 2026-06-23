package gen

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/gsxhq/gsx/internal/attrclass"
)

// manifestSchemaVersion is bumped on incompatible manifest layout changes so a
// reader can reject a manifest it does not understand.
const manifestSchemaVersion = 1

// manifest is the resolved, build-independent projection of a project's gsx
// configuration. It is the same data `gsx info` prints; persisted as JSON into
// the build cache so external tools can ground on the last successful build.
// It is a derived cache, never a hand-edited config file.
type manifest struct {
	SchemaVersion  int              `json:"schemaVersion"`
	Module         string           `json:"module"`
	UserRules      attrclass.Rules  `json:"userRules"`
	HasPredicate   bool             `json:"hasPredicate"`
	PredicateLabel string           `json:"predicateLabel,omitempty"`
	Filters        []manifestFilter `json:"filters,omitempty"`
}

type manifestFilter struct {
	Name string `json:"name"`
	Pkg  string `json:"pkg"`
	Func string `json:"func"`
}

// manifestPath returns the stable cache path for modPath's manifest. The key is
// derived from the module path alone so a tool that knows the module can find it
// without any content hash.
func manifestPath(cacheDir, modPath string) string {
	sum := sha256.Sum256([]byte(modPath))
	return filepath.Join(cacheDir, "manifest", fmt.Sprintf("%x.json", sum[:]))
}

func saveManifest(cacheDir, modPath string, m manifest) error {
	p := manifestPath(cacheDir, modPath)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	writeSentinel(cacheDir) // tag the cache root (idempotent, best-effort)
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(p), "tmp-")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), p) // atomic
}

func loadManifest(cacheDir, modPath string) (manifest, bool) {
	data, err := os.ReadFile(manifestPath(cacheDir, modPath))
	if err != nil {
		return manifest{}, false
	}
	var m manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return manifest{}, false
	}
	if m.SchemaVersion != manifestSchemaVersion {
		return manifest{}, false
	}
	return m, true
}

// buildManifest assembles a manifest from the resolved classifier and filters.
func buildManifest(modPath string, cls *attrclass.Classifier, predLabel string, filters []manifestFilter) manifest {
	return manifest{
		SchemaVersion:  manifestSchemaVersion,
		Module:         modPath,
		UserRules:      cls.Rules(),
		HasPredicate:   cls.HasPredicate(),
		PredicateLabel: predLabel,
		Filters:        filters,
	}
}
