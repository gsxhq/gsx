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

// WithJSAttrs registers additional JS-context attribute rules (e.g. Vue v-on:,
// Livewire wire:). Rules are additive over the built-ins; an invalid rule (both
// or neither of Name/Prefix set) fails the run with a clear message.
func WithJSAttrs(rules ...Rule) Option {
	return func(cfg *config) {
		cfg.jsRules = appendValidRules(cfg, "WithJSAttrs", cfg.jsRules, rules)
	}
}

// WithURLAttrs registers additional URL-context attribute rules.
func WithURLAttrs(rules ...Rule) Option {
	return func(cfg *config) {
		cfg.urlRules = appendValidRules(cfg, "WithURLAttrs", cfg.urlRules, rules)
	}
}

// WithCSSAttrs registers additional CSS-context attribute rules.
func WithCSSAttrs(rules ...Rule) Option {
	return func(cfg *config) {
		cfg.cssRules = appendValidRules(cfg, "WithCSSAttrs", cfg.cssRules, rules)
	}
}

// WithAttrClassifier installs a predicate escape hatch for matching logic the
// declarative rules cannot express. It is additive (consulted only for names no
// rule matched) and cannot downgrade a built-in. label is recorded in the
// manifest so offline tools can name the predicate they cannot evaluate.
// NOTE: predicate-classified attributes do not survive a broken build — prefer
// declarative rules where possible.
func WithAttrClassifier(label string, fn func(name string) (Context, bool)) Option {
	return func(cfg *config) {
		cfg.attrPred = fn
		cfg.predLabel = label
	}
}

// WithFieldMatcher installs a custom FieldMatcher for the byo (bring-your-own
// Props) attr→field resolution. The matcher is called for every attribute on a
// byo child component; it replaces the default identifier-capitalize + kebab→Camel
// logic. When nil the default matcher is used (no-op option).
//
// The FieldMatcher receives the raw attribute name (e.g. "aria-label") and the
// child struct's exported field names, and returns the matched field name +
// true, or ("", false) to send the attr to the Attrs bag.
//
// A custom matcher bypasses the incremental cache (funcs are not hashable) and
// is recorded as hasFieldMatcher:true in gsx info --json so external tools know
// the default matching was overridden.
func WithFieldMatcher(fn FieldMatcher) Option {
	return func(cfg *config) {
		cfg.fieldMatcher = fn
	}
}

// appendValidRules validates each rule in add, recording errors for invalid
// rules onto cfg.errs, and appends the valid ones to dst.
func appendValidRules(cfg *config, who string, dst, add []Rule) []Rule {
	for i, r := range add {
		if err := r.Valid(); err != nil {
			cfg.errs = append(cfg.errs, fmt.Errorf("%s: rule %d: %w", who, i, err))
			continue
		}
		dst = append(dst, r)
	}
	return dst
}
