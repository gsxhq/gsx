package gen

import (
	"errors"
	"fmt"
	"path/filepath"

	"github.com/gsxhq/gsx/internal/codegen"
)

// moduleConfigFunc yields the codegen configuration for the module rooted at
// root. Discovery walks DOWN across go.mod boundaries (see groupByModule), so
// one generate or watch can span several modules, and each must be configured
// by ITS gsx.toml: config is a property of a module, not of the invocation.
type moduleConfigFunc func(root string) (moduleGenerateConfig, error)

// fixedModuleConfig configures every module identically — the programmatic
// Generate API and tests, where there is no gsx.toml to discover.
func fixedModuleConfig(c moduleGenerateConfig) moduleConfigFunc {
	return func(string) (moduleGenerateConfig, error) { return c, nil }
}

// discoveredModuleConfig is the CLI's source: for each module root it
// discovers gsx.toml walking up from that root (resolveConfig), layers env and
// the compiled-in optCfg on top, and derives the codegen knobs. noCache forces
// a full regeneration; a custom minifier does the same (not hashable).
func discoveredModuleConfig(optCfg config, noCache bool) moduleConfigFunc {
	return func(root string) (moduleGenerateConfig, error) {
		merged, configPath, err := resolveConfig(optCfg, root)
		if err != nil {
			return moduleGenerateConfig{}, err
		}
		c := merged.moduleGenerateConfig(!noCache && !merged.hasCustomMinifier())
		c.configPath = configPath
		return c, nil
	}
}

// moduleGenerateConfig derives the codegen knobs from a resolved config.
func (c config) moduleGenerateConfig(useCache bool) moduleGenerateConfig {
	return moduleGenerateConfig{
		filterPkgs:   c.filterPkgs,
		aliases:      c.aliases,
		renderers:    c.renderers,
		classifier:   c.classifier(),
		useCache:     useCache,
		cssMin:       c.effectiveCSSMin(),
		jsMin:        c.effectiveJSMin(),
		jsonMin:      c.effectiveJSONMin(),
		cssMinify:    c.cssMinLevel.enabled(),
		jsMinify:     c.jsMinLevel.enabled(),
		verbatimTags: c.serialization == SerializationVerbatim,
		classMerger:  c.classMerger,
	}
}

// codegenOptions is the codegen.Options projection of the module's knobs; the
// caller adds the module identity (root, path) and any load context.
func (c moduleGenerateConfig) codegenOptions() codegen.Options {
	return codegen.Options{
		FilterPkgs:   c.filterPkgs,
		Aliases:      c.aliases,
		Renderers:    c.renderers,
		Classifier:   c.classifier,
		CSSMin:       c.cssMin,
		JSMin:        c.jsMin,
		JSONMin:      c.jsonMin,
		CSSMinify:    c.cssMinify,
		JSMinify:     c.jsMinify,
		VerbatimTags: c.verbatimTags,
		ClassMerger:  c.classMerger,
	}
}

// fmtOptionsFunc yields the codegen options `gsx fmt` analyzes the module
// rooted at root under (unused-import detection): the fmt-side counterpart of
// moduleConfigFunc, best-effort and load-free.
type fmtOptionsFunc func(root string) codegen.Options

// fixedFmtOptions analyzes every module under the same options (tests).
func fixedFmtOptions(o codegen.Options) fmtOptionsFunc {
	return func(string) codegen.Options { return o }
}

// inheritedFrom reports whether the module rooted at root took its config from
// a gsx.toml OUTSIDE the module — an ancestor file found by walking up past the
// module's own go.mod (a repo-root config shared by example sub-modules, say).
func (c moduleGenerateConfig) inheritedFrom(root string) bool {
	if c.configPath == "" {
		return false
	}
	return !pathWithinTree(root, filepath.Dir(c.configPath))
}

// annotateConfigError attributes a configured-package failure to the gsx.toml
// that named the package when that file was inherited from outside the module.
// Cross-module inheritance works only while every named package is importable
// from the inheriting module; when it is not, the bare codegen error blames a
// package the user never mentioned for THIS module, so name the file, the
// module, and the per-module config that opts out. Any other error, and any
// failure under the module's own config, is returned unchanged.
func annotateConfigError(err error, root, modPath string, c moduleGenerateConfig) error {
	var cpe *codegen.ConfiguredPackageError
	if err == nil || !errors.As(err, &cpe) || !c.inheritedFrom(root) {
		return err
	}
	// The cause goes last: a go/packages load error can span several lines.
	return fmt.Errorf("%s applies to module %s (rooted at %s) and fails there; add %s so that module has its own config (an empty file opts out of inheritance): %w",
		c.configPath, modPath, root, filepath.Join(root, configFileName), err)
}
