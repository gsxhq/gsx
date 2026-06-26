package gen

import (
	"fmt"
	"io"
	"path/filepath"

	"github.com/gsxhq/gsx/internal/codegen"
	"github.com/gsxhq/gsx/internal/gsxfmt"
	"github.com/gsxhq/gsx/internal/lsp"
)

// lspAnalyzer is the concrete code analysis behind the language server: it
// resolves the module root for a directory and runs the stock (std-filter)
// codegen pipeline over that one package, returning the retained Package — the
// diagnostics plus the read-only type info (Fset, TypesInfo, expr-map, gsx AST)
// the read-intelligence features need. It never writes .x.go to disk.
//
// Slice-1 limitation: codegen discovers .gsx files by on-disk glob, so a buffer
// opened in the editor but never saved to disk is not analyzed (its override is
// never consulted). Editing existing .gsx files works; unsaved-new files are a
// slice-2 follow-up.
type lspAnalyzer struct {
	optCfg config    // programmatic opts (empty for the stock binary); layered OVER gsx.toml (opts win on conflict)
	warnw  io.Writer // best-effort sink for a malformed gsx.toml; nil → discard, never fatal
}

func (a lspAnalyzer) Analyze(dir string, override map[string][]byte) (*lsp.Package, error) {
	root, _, err := moduleRoot(dir)
	if err != nil {
		return nil, err
	}
	merged := resolveConfigBestEffort(dir, a.optCfg, a.warnw)
	out, err := codegen.GeneratePackagesWithFilters(root, []string{dir},
		merged.filterPkgs, merged.aliases, merged.classifier(), merged.fieldMatcher,
		nil, nil, true, true, override)
	if err != nil {
		return nil, err
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	pr := out[abs]
	if pr == nil {
		return &lsp.Package{}, nil
	}
	cross := make(map[string]lsp.CrossRef, len(pr.CrossIndex))
	for k, v := range pr.CrossIndex {
		cross[k] = lsp.CrossRef{Name: v.Name, Decl: v.Decl, Refs: v.Refs}
	}
	nav := make([]lsp.NavRef, len(pr.NavIndex))
	for i, nr := range pr.NavIndex {
		nav[i] = lsp.NavRef{From: nr.From, Name: nr.Name, To: nr.To}
	}
	unused := make(map[string][]gsxfmt.ImportRef, len(pr.UnusedImports))
	for path, imps := range pr.UnusedImports {
		refs := make([]gsxfmt.ImportRef, len(imps))
		for i, u := range imps {
			refs[i] = gsxfmt.ImportRef{Name: u.Name, Path: u.Path}
		}
		unused[path] = refs
	}
	return &lsp.Package{
		Diags:         pr.Diags,
		GSXFset:       pr.GSXFset,
		Fset:          pr.Fset,
		Info:          pr.Info,
		Types:         pr.Types,
		ExprMap:       pr.ExprMap,
		Files:         pr.GSXFiles,
		CrossIndex:    cross,
		NavIndex:      nav,
		UnusedImports: unused,
	}, nil
}

// AnalyzeModule analyzes every gsx package in the module containing dir and
// returns a flat cross-reference list. It runs ONE whole-module codegen batch
// (so cross-package component references route into the declaring component's
// CrossRef — see the cross-package find-references design) and flattens every
// package's CrossIndex. override supplies unsaved buffers (abs path -> bytes).
func (a lspAnalyzer) AnalyzeModule(dir string, override map[string][]byte) ([]lsp.CrossRef, error) {
	root, _, err := moduleRoot(dir)
	if err != nil {
		return nil, err
	}
	dirs, err := discoverDirs([]string{root})
	if err != nil {
		return nil, err
	}
	merged := resolveConfigBestEffort(dir, a.optCfg, a.warnw)
	out, err := codegen.GeneratePackagesWithFilters(root, dirs,
		merged.filterPkgs, merged.aliases, merged.classifier(), merged.fieldMatcher,
		nil, nil, true, true, override)
	if err != nil {
		return nil, err
	}
	var refs []lsp.CrossRef
	for _, pr := range out {
		if pr == nil {
			continue
		}
		for _, v := range pr.CrossIndex {
			refs = append(refs, lsp.CrossRef{Name: v.Name, Decl: v.Decl, Refs: v.Refs})
		}
	}
	return refs, nil
}

// PrintWidth resolves the effective gsx.toml print width for dir, layering the
// programmatic optCfg over the file config exactly like Analyze. Best-effort:
// returns 80 on any failure.
func (a lspAnalyzer) PrintWidth(dir string) int {
	merged := resolveConfigBestEffort(dir, a.optCfg, a.warnw)
	return merged.effectivePrintWidth()
}

// resolveConfigBestEffort resolves the LSP's effective config: it discovers a
// gsx.toml from dir (walking up, bounded by .git/module root) and merges it under
// optCfg — exactly as resolveConfig does for generate/info — but for the LSP it
// must NEVER break analysis. A malformed/typo'd gsx.toml is logged to warnw (when
// non-nil) and the optCfg baseline is used; with no gsx.toml, optCfg is returned
// unchanged. It loads no packages (TOML + file walk only), so it is cheap enough
// to run per Analyze, which also picks up gsx.toml edits live.
func resolveConfigBestEffort(dir string, optCfg config, warnw io.Writer) config {
	path, ok := discoverConfig(dir)
	if !ok {
		return optCfg
	}
	fileCfg, err := loadConfig(path)
	if err != nil {
		if warnw != nil {
			fmt.Fprintf(warnw, "gsx: lsp: ignoring %s: %v\n", path, err)
		}
		return optCfg
	}
	return mergeConfig(fileCfg, optCfg)
}

// runLSP runs the gsx language server over stdin/stdout (JSON-RPC), logging
// operational failures to stderr. cfg carries the binary's compiled-in opts
// (empty for the stock binary), layered OVER the project's gsx.toml (opts win) per Analyze.
// It returns a process exit code.
func runLSP(stdin io.Reader, stdout, stderr io.Writer, cfg config, _ []string) int {
	srv := lsp.NewServer(stdin, stdout, lspAnalyzer{optCfg: cfg, warnw: stderr})
	if err := srv.Run(); err != nil {
		fmt.Fprintf(stderr, "gsx: lsp: %v\n", err)
		return 1
	}
	return 0
}
