package codegen

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/gsxhq/gsx/internal/attrclass"
	"github.com/gsxhq/gsx/internal/diag"
)

// GenOptions configures GenerateDirs.
type GenOptions struct {
	FilterPkgs   []string
	Aliases      []FilterAlias
	Classifier   *attrclass.Classifier
	FieldMatcher FieldMatcher
	CSSMin       func(string) (string, error)
	JSMin        func(string) (string, error)
	CSSMinify    bool
	JSMinify     bool
}

// DirResult is the per-directory outcome of GenerateDirs.
type DirResult struct {
	Files map[string][]byte // keyed by .gsx path (same as Module.Generate)
	Diags []diag.Diagnostic
}

// GenerateDirs opens a fresh Module rooted at moduleRoot, applies any override
// bytes, and calls Module.Generate on each dir. On a hard (non-diagnostic) error
// it returns immediately; otherwise each dir's result accumulates in the returned
// map. The map is keyed by the same dir strings passed in.
//
// override maps absolute .gsx paths to in-memory source bytes (e.g. unsaved
// editor buffers). Pass nil when no overrides are needed.
func GenerateDirs(moduleRoot string, dirs []string, opts GenOptions, override map[string][]byte) (map[string]DirResult, error) {
	modPath, err := readModulePath(moduleRoot)
	if err != nil {
		return nil, fmt.Errorf("codegen: GenerateDirs: %w", err)
	}
	m, err := Open(Options{
		ModuleRoot:   moduleRoot,
		ModulePath:   modPath,
		FilterPkgs:   opts.FilterPkgs,
		Aliases:      opts.Aliases,
		Classifier:   opts.Classifier,
		FieldMatcher: opts.FieldMatcher,
		CSSMin:       opts.CSSMin,
		JSMin:        opts.JSMin,
		CSSMinify:    opts.CSSMinify,
		JSMinify:     opts.JSMinify,
	})
	if err != nil {
		return nil, fmt.Errorf("codegen: GenerateDirs: open module: %w", err)
	}
	for path, src := range override {
		m.SetOverride(path, src)
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
