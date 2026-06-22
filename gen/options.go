package gen

import (
	"fmt"
	"reflect"
)

// WithFilters registers one or more filter packages by their marker tokens.
// Each marker is a package's exported Pkg value (an unexported marker type, e.g.
// std.Pkg); WithFilters recovers the package's import path from the marker's
// type via reflection, so callers never spell an import-path string by hand:
//
//	gen.Main(gen.WithFilters(std.Pkg, myfilters.Pkg))
//
// The registered paths are appended to the config's ordered filter-package list
// and de-duplicated preserving first-seen order. ORDER MATTERS: filters are
// resolved LAST-WINS by name, so a package that should override an earlier
// package's same-named filter must be listed AFTER it (put overrides last).
//
// A nil marker, or a marker whose type has no import path (a builtin or unnamed
// type, e.g. an int literal), cannot name a package; rather than silently drop
// it, WithFilters records an error on the config so the run fails with a clear
// message.
func WithFilters(markers ...any) Option {
	return func(cfg *config) {
		for i, m := range markers {
			if m == nil {
				cfg.errs = append(cfg.errs, fmt.Errorf("WithFilters: marker %d is nil; pass a package's Pkg token (e.g. std.Pkg)", i))
				continue
			}
			path := reflect.TypeOf(m).PkgPath()
			if path == "" {
				cfg.errs = append(cfg.errs, fmt.Errorf("WithFilters: marker %d (%T) has no package path; pass a package's exported Pkg token (e.g. std.Pkg)", i, m))
				continue
			}
			cfg.appendFilterPkg(path)
		}
	}
}

// WithCSSMinifier installs a custom CSS minifier for <style> blocks, replacing
// the built-in safe minifier on FULLY-STATIC (holeless) blocks. A block that
// contains @{ } interpolation always uses gsx's built-in hole-aware minifier, so
// the custom minifier only ever receives complete, valid CSS. Wrap any
// whole-buffer minifier (e.g. tdewolff) in this signature:
//
//	gen.Main(gen.WithCSSMinifier(func(css string) (string, error) { … }))
func WithCSSMinifier(min func(css string) (string, error)) Option {
	return func(cfg *config) { cfg.cssMin = min }
}

// WithJSMinifier installs a custom JS minifier for <script> blocks, replacing
// the built-in safe minifier. It receives complete JS (<script> is holeless).
func WithJSMinifier(min func(js string) (string, error)) Option {
	return func(cfg *config) { cfg.jsMin = min }
}

// appendFilterPkg appends path to the config's ordered filter-package list
// unless it is already present (first-seen order is preserved).
func (cfg *config) appendFilterPkg(path string) {
	for _, p := range cfg.filterPkgs {
		if p == path {
			return
		}
	}
	cfg.filterPkgs = append(cfg.filterPkgs, path)
}
