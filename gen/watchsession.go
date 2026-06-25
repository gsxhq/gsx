package gen

import (
	"github.com/gsxhq/gsx/internal/codegen"
)

// newModuleResolver builds a warm CachedResolver whose importer covers the whole
// module: all in-module packages (so cross-package gsx component refs resolve)
// plus their transitive dependencies. filterPkgs/aliases thread the user's
// pipeline filters, exactly as the cold path does. The one-time packages.Load
// happens here; resolver.Generate calls afterwards run fully in-process.
func newModuleResolver(moduleDir string, filterPkgs []string, aliases []codegen.FilterAlias) (*CachedResolver, error) {
	// "./..." expands to every package in the module; packages.Load (NeedDeps)
	// pulls their transitive deps into the importer map. This is what lets a
	// later resolver.Generate of one package see sibling packages' types.
	allow := []string{"./..."}
	inner, err := codegen.NewCachedResolver(moduleDir, append([]string{codegen.StdImportPath}, filterPkgs...), aliases, allow)
	if err != nil {
		return nil, err
	}
	return &CachedResolver{inner: inner}, nil
}
