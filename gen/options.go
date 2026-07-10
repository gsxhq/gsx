package gen

import (
	"fmt"
	"reflect"
	"runtime"
	"slices"
	"strings"

	"github.com/gsxhq/gsx/internal/attrclass"
	"github.com/gsxhq/gsx/internal/codegen"
	"github.com/gsxhq/gsx/internal/rawfmt"
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

// WithFilter registers a SINGLE function fn as a pipeline filter under the
// explicit short template name. It is the sibling of WithFilters (whole-package
// harvest): it lets a project own the template vocabulary (e.g. "url") while the
// library keeps its idiomatic Go name (e.g. structpages.URLFor):
//
//	gen.Main(
//	    gen.WithFilters(std.Pkg),
//	    gen.WithFilter("url",    structpages.URLFor),
//	    gen.WithFilter("id",     structpages.ID),
//	)
//
// fn is reflected via runtime.FuncForPC to recover its fully-qualified
// package/path.FuncName, which is split into the import path and the exported Go
// func name. fn MUST be a plain top-level exported function: a method value
// ((*T).M / T.M), a closure (a .funcN suffix), or an unexported target is
// rejected with a clear error recorded on the config so the run fails. The
// signature itself is classified later (at harvest, via go/types) against the
// seed-first contract, so a curried target surfaces the migration diagnostic
// there.
//
// Aliases are appended in option order AFTER WithFilters package harvests, so an
// alias can intentionally override a harvested same-named filter (last-wins).
func WithFilter(name string, fn any) Option {
	return func(cfg *config) {
		if fn == nil {
			cfg.errs = append(cfg.errs, fmt.Errorf("WithFilter %q: fn is nil; pass an exported top-level function (e.g. structpages.URLFor)", name))
			return
		}
		v := reflect.ValueOf(fn)
		if v.Kind() != reflect.Func {
			cfg.errs = append(cfg.errs, fmt.Errorf("WithFilter %q: fn (%T) is not a function", name, fn))
			return
		}
		pkgPath, funcName, err := resolveFilterFunc(v)
		if err != nil {
			cfg.errs = append(cfg.errs, fmt.Errorf("WithFilter %q: %w", name, err))
			return
		}
		cfg.aliases = append(cfg.aliases, codegen.FilterAlias{
			Name:     name,
			PkgPath:  pkgPath,
			FuncName: funcName,
		})
	}
}

// resolveFilterFunc reflects a function value to its declaring package import
// path and exported func name, rejecting anything that is not a plain top-level
// exported function. runtime.FuncForPC returns the fully-qualified name in the
// form "import/path.FuncName" for a top-level func; method values appear as
// "import/path.(*T).M" or "import/path.T.M" and closures carry a ".funcN" (or
// ".funcN.M") suffix — both are rejected.
func resolveFilterFunc(v reflect.Value) (pkgPath, funcName string, err error) {
	rf := runtime.FuncForPC(v.Pointer())
	if rf == nil {
		return "", "", fmt.Errorf("cannot resolve function value")
	}
	full := rf.Name() // e.g. "github.com/foo/bar.URLFor"
	return splitPkgFunc(full)
}

// splitPkgFunc splits a fully-qualified "import/path.FuncName" string into its
// package import path and exported func name, rejecting anything that is not a
// plain top-level exported function. It is the shared parser used by BOTH the
// reflection path (resolveFilterFunc, from runtime.FuncForPC) and the gsx.toml
// config loader (an alias value like "github.com/jackielii/structpages.URLFor"),
// so both produce an identical (PkgPath, FuncName) for the FilterAlias harvest.
func splitPkgFunc(full string) (pkgPath, funcName string, err error) {
	// Split off the func segment: everything after the LAST "." that is not part
	// of the import path. The import path may contain dots (foo.com/bar), but the
	// path component before the final func name never contains a "/"-free dotted
	// tail other than the func — so split at the last ".".
	dot := strings.LastIndex(full, ".")
	if dot < 0 {
		return "", "", fmt.Errorf("function %q has no package-qualified name", full)
	}
	pkgPath = full[:dot]
	funcName = full[dot+1:]

	// Reject method values: the package portion ends in a receiver group
	// "(*T)" or "T" preceded by a ".", i.e. the would-be pkgPath still contains a
	// ".(" or its final segment after the last "/" contains a "." (a type name).
	// A plain top-level func has a pkgPath whose final "/"-segment has NO ".".
	if strings.Contains(pkgPath, ".(") {
		return "", "", fmt.Errorf("function %q is a method value; requires a plain top-level function", full)
	}
	finalSeg := pkgPath
	if i := strings.LastIndex(pkgPath, "/"); i >= 0 {
		finalSeg = pkgPath[i+1:]
	}
	if strings.Contains(finalSeg, ".") {
		// e.g. "github.com/foo/bar.T" → method value T.M; or a closure parent.
		return "", "", fmt.Errorf("function %q is not a plain top-level function (method value or nested)", full)
	}
	// Reject closures: a "func1"/"funcN" suffix, or a non-exported / non-identifier
	// func name.
	if !isExportedIdent(funcName) {
		return "", "", fmt.Errorf("function %q is not an exported top-level function (got %q); requires e.g. structpages.URLFor", full, funcName)
	}
	return pkgPath, funcName, nil
}

// isExportedIdent reports whether s is a valid exported Go identifier: a leading
// uppercase letter followed by letters/digits/underscores. This rejects closure
// suffixes ("func1") and unexported names ("helper").
func isExportedIdent(s string) bool {
	if s == "" {
		return false
	}
	r := rune(s[0])
	if r < 'A' || r > 'Z' {
		return false
	}
	for _, c := range s[1:] {
		isIdentChar := c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
		if !isIdentChar {
			return false
		}
	}
	return true
}

// WithClassMerger installs fn as the project's class-merge strategy. fn MUST be
// an exported top-level function of type func([]string) string (e.g. a wrapper
// around a tailwind-merge-go merger). It is reflected via runtime.FuncForPC to
// recover its package/path.FuncName, exactly like WithFilter; a closure, method
// value, or unexported target is rejected with a clear error on the config. The
// signature is verified at generate time (go/types) against func([]string)string.
//
// Precedence: an option-set merger wins over a gsx.toml class_merger.
func WithClassMerger(fn any) Option {
	return func(cfg *config) {
		if fn == nil {
			cfg.errs = append(cfg.errs, fmt.Errorf("WithClassMerger: fn is nil; pass an exported func([]string) string"))
			return
		}
		v := reflect.ValueOf(fn)
		if v.Kind() != reflect.Func {
			cfg.errs = append(cfg.errs, fmt.Errorf("WithClassMerger: fn (%T) is not a function", fn))
			return
		}
		pkgPath, funcName, err := resolveFilterFunc(v)
		if err != nil {
			cfg.errs = append(cfg.errs, fmt.Errorf("WithClassMerger: %w", err))
			return
		}
		cfg.classMerger = &codegen.ClassMergerRef{PkgPath: pkgPath, FuncName: funcName}
	}
}

// WithCSSMinifier installs a custom CSS minifier for <style> blocks. It is used
// only when CSS minification is enabled (minify level "full"), where it REPLACES
// the built-in full minifier on FULLY-STATIC (holeless) blocks. A block that
// contains @{ } interpolation always uses gsx's built-in hole-aware minifier, so
// the custom minifier only ever receives complete, valid CSS. At level "none"
// (the default) no minification runs and the custom minifier is not called. Wrap
// any whole-buffer minifier (e.g. tdewolff) in this signature:
//
//	gen.Main(gen.WithCSSMinifier(func(css string) (string, error) { … }))
func WithCSSMinifier(min func(css string) (string, error)) Option {
	return func(cfg *config) { cfg.cssMin = min }
}

// WithJSMinifier installs a custom JS minifier for <script> blocks. It is used
// only when JS minification is enabled (minify level "full"), where it REPLACES
// the built-in full minifier; it receives complete JS (<script> is holeless). At
// level "none" (the default) no minification runs and it is not called.
func WithJSMinifier(min func(js string) (string, error)) Option {
	return func(cfg *config) { cfg.jsMin = min }
}

// WithCSSFormatter installs a custom CSS formatter for <style> bodies during
// `gsx fmt`, replacing the built-in minimal formatter. It receives complete,
// self-contained CSS (interpolation holes are substituted with sentinel tokens
// before the formatter sees them and restored afterward) and returns the
// formatted CSS, or an error to fall back to verbatim. Wrap any whole-buffer
// formatter (e.g. a prettier shell-out) in this signature:
//
//	gen.Main(gen.WithCSSFormatter(func(css []byte) ([]byte, error) { … }))
func WithCSSFormatter(f rawfmt.Formatter) Option {
	return func(cfg *config) { cfg.cssFmt = f }
}

// WithJSFormatter installs a custom JS formatter for executable <script> bodies
// during `gsx fmt`, replacing the built-in re-indenter. It receives complete,
// self-contained JS (interpolation holes are substituted with sentinel tokens
// before it runs and restored afterward) and returns the formatted JS, or an
// error to fall back to verbatim. Wrap any whole-buffer formatter (prettier,
// biome, esbuild) in this signature:
//
//	gen.Main(gen.WithJSFormatter(func(js []byte) ([]byte, error) { … }))
func WithJSFormatter(f rawfmt.Formatter) Option {
	return func(cfg *config) { cfg.jsFmt = f }
}

// appendFilterPkg appends path to the config's ordered filter-package list
// unless it is already present (first-seen order is preserved).
func (cfg *config) appendFilterPkg(path string) {
	if !slices.Contains(cfg.filterPkgs, path) {
		cfg.filterPkgs = append(cfg.filterPkgs, path)
	}
}

// WithURLAttrs registers additional URL-context attribute rules.
func WithURLAttrs(rules ...Rule) Option {
	return func(cfg *config) {
		cfg.urlRules = appendValidRules(cfg, "WithURLAttrs", cfg.urlRules, rules)
	}
}

// WithURLPreset enables one or more named URL-attribute presets, appending each
// preset's URL rules onto the config (additive over the built-in floor, exactly
// like WithURLAttrs). The only preset today is "htmx", which re-classifies the
// five htmx method attributes (hx-get/post/put/delete/patch) as URL sinks — they
// are OFF by default. An unknown preset name is recorded as a config error so the
// run fails with a clear message instead of silently doing nothing.
func WithURLPreset(names ...string) Option {
	return func(cfg *config) {
		for _, name := range names {
			rules, ok := attrclass.Preset(name)
			if !ok {
				cfg.errs = append(cfg.errs, fmt.Errorf("WithURLPreset: unknown preset %q (known: %s)", name, strings.Join(attrclass.PresetNames(), ", ")))
				continue
			}
			cfg.urlRules = append(cfg.urlRules, rules.URL...)
		}
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

// WithMinifyLevel pins the minification level for <style> CSS and <script> JS,
// overriding both the [minify] config table and the GSX_MINIFY env var (code is
// the most deliberate layer: option > env > config). The level GATES the pass;
// MinifyNone emits the asset verbatim (no minifier runs); MinifyFull enables
// aggressive AST-based minification (a custom WithCSSMinifier/WithJSMinifier
// takes precedence over the built-in full pass).
func WithMinifyLevel(css, js MinifyLevel) Option {
	return func(cfg *config) {
		cfg.cssMinLevel = css
		cfg.jsMinLevel = js
		cfg.minifyLevelSet = true
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
