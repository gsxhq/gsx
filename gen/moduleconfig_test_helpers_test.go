package gen

import (
	"github.com/gsxhq/gsx/internal/attrclass"
	"github.com/gsxhq/gsx/internal/codegen"
)

// generateCachedReportFixed is generateCachedWithReport under ONE explicit
// configuration for every module (no gsx.toml discovery) — the cache tests'
// entry point, mirroring generateCached but returning the cache report.
func generateCachedReportFixed(paths, filterPkgs []string, aliases []codegen.FilterAlias, renderers []codegen.RendererAlias, cls *attrclass.Classifier, useCache bool, cssMin, jsMin, jsonMin func(string) (string, error), cssMinify, jsMinify, verbatimTags bool, classMerger *codegen.ClassMergerRef) (Result, cacheReport, error) {
	return generateCachedWithReport(paths, fixedModuleConfig(moduleGenerateConfig{
		filterPkgs:   filterPkgs,
		aliases:      aliases,
		renderers:    renderers,
		classifier:   cls,
		useCache:     useCache,
		cssMin:       cssMin,
		jsMin:        jsMin,
		jsonMin:      jsonMin,
		cssMinify:    cssMinify,
		jsMinify:     jsMinify,
		verbatimTags: verbatimTags,
		classMerger:  classMerger,
	}))
}
