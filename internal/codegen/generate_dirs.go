package codegen

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/gsxhq/gsx/internal/diag"
)

// DirResult is the per-directory outcome of GenerateDirs.
type DirResult struct {
	Files map[string][]byte // keyed by .gsx path (same as Module.Generate)
	Diags []diag.Diagnostic
}

// GenerateDirs opens a fresh Module rooted at moduleRoot, applies any override
// bytes, and calls Module.Generate on each dir. opts carries the codegen knobs;
// GenerateDirs fills opts.ModuleRoot from moduleRoot and derives opts.ModulePath
// from go.mod only when the caller left it empty (callers that already know the
// module path pass it to skip the re-read). On a hard (non-diagnostic) error it
// returns immediately; otherwise each dir's result accumulates in the returned
// map, keyed by the same dir strings passed in. override maps absolute .gsx paths
// to in-memory source bytes; pass nil when no overrides are needed.
func GenerateDirs(moduleRoot string, dirs []string, opts Options, override map[string][]byte) (map[string]DirResult, error) {
	opts.ModuleRoot = moduleRoot
	if opts.ModulePath == "" {
		modPath, err := readModulePath(moduleRoot)
		if err != nil {
			return nil, fmt.Errorf("codegen: GenerateDirs: %w", err)
		}
		opts.ModulePath = modPath
	}
	m, err := Open(opts)
	if err != nil {
		return nil, fmt.Errorf("codegen: GenerateDirs: open module: %w", err)
	}
	for path, src := range override {
		m.SetOverride(path, src)
	}
	// Validate the complete global + per-directory merger set only after every
	// in-memory source override is installed, using the Module's one authoritative
	// configured-declaration graph.
	if err := m.validateConfiguredMergers(); err != nil {
		return nil, fmt.Errorf("codegen: GenerateDirs: %w", err)
	}
	result := make(map[string]DirResult, len(dirs))
	for _, dir := range dirs {
		out, diags, err := m.Generate(dir)
		if err != nil {
			return nil, fmt.Errorf("codegen: GenerateDirs: generate %s: %w", dir, err)
		}
		result[dir] = DirResult{Files: out, Diags: diags}
	}
	return result, nil
}

// readModulePath reads the module path from the go.mod file at moduleRoot.
func readModulePath(moduleRoot string) (string, error) {
	data, err := os.ReadFile(filepath.Join(moduleRoot, "go.mod"))
	if err != nil {
		return "", fmt.Errorf("read go.mod: %w", err)
	}
	return ModulePathFromGoMod(data), nil
}
