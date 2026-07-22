package gen

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/gsxhq/gsx/internal/attrclass"
	"github.com/gsxhq/gsx/internal/codegen"
	"github.com/gsxhq/gsx/internal/gsxfmt"
)

// configFileName is the project config filename gsx discovers (TOML).
const configFileName = "gsx.toml"

// tomlConfig is the on-disk gsx.toml schema (v1). It mirrors the declarative
// subset of the gen.With* options (named filters, filter packages, URL attr
// rules); func-valued options (custom minifier/field-matcher) stay code-only.
// Field tags pin the exact TOML keys so strict decoding (Undecoded) can reject
// typos.
//
// Filters is the named-filter table (template name → "<pkgPath>.<Func>"), the
// common case used as `{ value |> name(args) }`. FilterPackages is the bulk
// form: every exported func of each listed package is registered as a filter,
// named by its lower-cased func name (std.Upper → `upper`).
//
// Renderers is the [renderers] table: a fully-qualified Go named type
// ("pkgPath.TypeName", optionally *-prefixed for a pointer type) → a
// "<pkgPath>.<Func>" target, applied at render boundaries by codegen (see
// gen.WithRenderer and codegen.RendererAlias).
type tomlConfig struct {
	Filters        map[string]string `toml:"filters"`
	FilterPackages []string          `toml:"filter_packages"`
	Renderers      map[string]string `toml:"renderers"`
	URLAttrs       []tomlRule        `toml:"url_attrs"`
	URLPresets     []string          `toml:"url_presets"`
	Formatter      *tomlFormatter    `toml:"formatter"`
	Minify         *tomlMinify       `toml:"minify"`
	ClassMerger    string            `toml:"class_merger"`
	Dev            *tomlDev          `toml:"dev"`
}

// tomlFormatter is the [formatter] table: knobs for `gsx fmt` and LSP
// formatting. Like [dev], it never changes generated output and is NOT folded
// into computeKey. A nil pointer (table absent) leaves the defaults
// (print_width pretty.DefaultPrintWidth, imports "goimports").
type tomlFormatter struct {
	PrintWidth int    `toml:"print_width"`
	TabWidth   int    `toml:"tab_width"`
	Imports    string `toml:"imports"` // "goimports" (default) | "gofmt"
}

// tomlDev is the [dev] table read ONLY by `gsx dev` (runDev) — it is NOT part of
// the codegen config and is NOT folded into computeKey, because dev knobs never
// change generated output. It exists on tomlConfig so strict decoding accepts a
// [dev] table without breaking config-consuming commands (generate/info).
// Commands are argv arrays for exact quoting.
type tomlDev struct {
	Web   []string `toml:"web"`
	Build []string `toml:"build"`
	Run   []string `toml:"run"`
	Log   string   `toml:"log"`
	NoWeb bool     `toml:"no_web"`
	// Host is the hostname used to build VITE_DEV_URL (default "localhost"). Set
	// it when the dev server must be reachable under a specific hostname —
	// e.g. host = "mstudio" yields VITE_DEV_URL=http://mstudio:<port>.
	Host string `toml:"host"`
}

// tomlMinify is the [minify] table: per-asset level spellings. A nil pointer
// (table absent) leaves both levels at their default (safe). An empty string for
// a key (key absent) likewise leaves that asset's default.
type tomlMinify struct {
	CSS string `toml:"css"`
	JS  string `toml:"js"`
}

// tomlRule is one attribute-classification rule from an array-of-tables. Exactly
// one of Name/Prefix must be set (validated against attrclass.Rule.Valid).
type tomlRule struct {
	Name   string `toml:"name"`
	Prefix string `toml:"prefix"`
}

// discoverConfig walks UP from startDir and returns the full path of the FIRST
// directory containing a gsx.toml. The walk is bounded by the nearest ancestor
// containing .git (the git repo root); if none, it falls back to the module root
// (go.mod) directory. The bound dir is checked inclusively, then the walk stops —
// so a gsx.toml above the repo/module root is never used (no $HOME / filesystem
// root escape). Returns ("", false) when no config is found within the bound.
func discoverConfig(startDir string) (path string, ok bool) {
	d, err := filepath.Abs(startDir)
	if err != nil {
		return "", false
	}
	bound := configWalkBound(d)
	for {
		candidate := filepath.Join(d, configFileName)
		if fi, statErr := os.Stat(candidate); statErr == nil && !fi.IsDir() {
			return candidate, true
		}
		if d == bound {
			return "", false
		}
		parent := filepath.Dir(d)
		if parent == d {
			return "", false
		}
		d = parent
	}
}

// configWalkBound returns the inclusive upper bound for the discovery walk: the
// nearest ancestor of dir containing a .git entry (file OR dir — git worktrees
// and submodules use a .git file), else the module root (go.mod) directory, else
// dir itself (never escape to the filesystem root).
func configWalkBound(dir string) string {
	d := dir
	for {
		if _, err := os.Stat(filepath.Join(d, ".git")); err == nil {
			return d
		}
		parent := filepath.Dir(d)
		if parent == d {
			break
		}
		d = parent
	}
	if root, _, err := moduleRoot(dir); err == nil {
		return root
	}
	return dir
}

// loadConfig reads and decodes a gsx.toml at path into a config. Decoding is
// strict: an unknown key (a typo like "filteres") is an error naming the path.
// Each alias "name = pkg.Func" is parsed via splitPkgFunc (identical to the
// reflection path) into a codegen.FilterAlias; aliases are emitted sorted by
// name so the resulting slice — and thus the cache key — is deterministic
// regardless of TOML map ordering. Each attr rule is validated (exactly one of
// name/prefix). Returns a populated config (errors name the path + key).
func loadConfig(path string) (config, error) {
	var tc tomlConfig
	md, err := toml.DecodeFile(path, &tc)
	if err != nil {
		return config{}, fmt.Errorf("%s: %w", path, err)
	}
	if und := md.Undecoded(); len(und) > 0 {
		keys := make([]string, 0, len(und))
		for _, k := range und {
			keys = append(keys, k.String())
		}
		sort.Strings(keys)
		return config{}, fmt.Errorf("%s: unknown key(s): %s", path, strings.Join(keys, ", "))
	}

	var cfg config
	for _, p := range tc.FilterPackages {
		cfg.appendFilterPkg(p)
	}

	// Named filters: sort by name for a deterministic slice (TOML maps are
	// unordered) so the resolved order — and thus the cache key — is stable.
	names := make([]string, 0, len(tc.Filters))
	for n := range tc.Filters {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		pkgPath, funcName, err := splitPkgFunc(tc.Filters[n])
		if err != nil {
			return config{}, fmt.Errorf("%s: filter %q: %w", path, n, err)
		}
		cfg.aliases = append(cfg.aliases, codegen.FilterAlias{Name: n, PkgPath: pkgPath, FuncName: funcName})
	}

	// Renderers: sort by TypeKey for a deterministic slice (TOML maps are
	// unordered), same shape as the named-filters loop above. Unlike aliases,
	// registration ORDER here is not itself meaning — computeKey's renderers=
	// pin resolves last-wins-per-TypeKey first and then re-sorts by TypeKey —
	// but the config-level slice is still emitted sorted for a stable,
	// diffable cfg.renderers regardless of TOML map iteration order.
	rendererKeys := make([]string, 0, len(tc.Renderers))
	for k := range tc.Renderers {
		rendererKeys = append(rendererKeys, k)
	}
	sort.Strings(rendererKeys)
	for _, k := range rendererKeys {
		key, err := splitPkgType(k)
		if err != nil {
			return config{}, fmt.Errorf("%s: %w", path, err)
		}
		pkgPath, funcName, err := splitPkgFunc(tc.Renderers[k])
		if err != nil {
			return config{}, fmt.Errorf("%s: renderer for %q: %w", path, k, err)
		}
		cfg.renderers = append(cfg.renderers, codegen.RendererAlias{TypeKey: key, PkgPath: pkgPath, FuncName: funcName})
	}

	if tc.ClassMerger != "" {
		pkgPath, funcName, err := splitPkgFunc(tc.ClassMerger)
		if err != nil {
			return config{}, fmt.Errorf("%s: class_merger %q: %w", path, tc.ClassMerger, err)
		}
		cfg.classMerger = &codegen.ClassMergerRef{PkgPath: pkgPath, FuncName: funcName}
	}

	if cfg.urlRules, err = appendTomlRules(path, "url_attrs", cfg.urlRules, tc.URLAttrs); err != nil {
		return config{}, err
	}
	for _, name := range tc.URLPresets {
		rules, ok := attrclass.Preset(name)
		if !ok {
			return config{}, fmt.Errorf("%s: url_presets: unknown preset %q (known: %s)", path, name, strings.Join(attrclass.PresetNames(), ", "))
		}
		cfg.urlRules = append(cfg.urlRules, rules.URL...)
		cfg.urlPresets = append(cfg.urlPresets, name)
	}
	if tc.Minify != nil {
		if tc.Minify.CSS != "" {
			lvl, err := parseMinifyLevel(tc.Minify.CSS)
			if err != nil {
				return config{}, fmt.Errorf("%s: minify.css: %w", path, err)
			}
			cfg.cssMinLevel = lvl
		}
		if tc.Minify.JS != "" {
			lvl, err := parseMinifyLevel(tc.Minify.JS)
			if err != nil {
				return config{}, fmt.Errorf("%s: minify.js: %w", path, err)
			}
			cfg.jsMinLevel = lvl
		}
	}
	if tc.Formatter != nil {
		cfg.printWidth = tc.Formatter.PrintWidth
		cfg.tabWidth = tc.Formatter.TabWidth
		if s := tc.Formatter.Imports; s != "" {
			m, err := gsxfmt.ParseImportsMode(s)
			if err != nil {
				return config{}, fmt.Errorf("%s: formatter.imports: %w", path, err)
			}
			cfg.importsMode = m
		}
	}
	return cfg, nil
}

// appendTomlRules converts and validates a slice of tomlRule into attrclass.Rule,
// returning an error (naming the path + table + index) on the first invalid rule.
func appendTomlRules(path, who string, dst []attrclass.Rule, add []tomlRule) ([]attrclass.Rule, error) {
	for i, r := range add {
		rule := attrclass.Rule{Name: r.Name, Prefix: r.Prefix}
		if err := rule.Valid(); err != nil {
			return nil, fmt.Errorf("%s: %s rule %d: %w", path, who, i, err)
		}
		dst = append(dst, rule)
	}
	return dst, nil
}

// mergeConfig merges a programmatic opts config ON TOP of a file-loaded base
// config. The file base comes first; opts are appended after so they win under
// the existing last-wins resolution: filterPkgs, aliases, and renderers are
// base++opts (with filterPkgs deduped), URL attr rules are concatenated
// base++opts, and func-valued fields (cssMin/jsMin) are taken
// from opts when set, else base. errs are concatenated. Slices are freshly
// allocated so neither input is mutated.
func mergeConfig(base, opts config) config {
	var merged config

	merged.filterPkgs = append(merged.filterPkgs, base.filterPkgs...)
	for _, p := range opts.filterPkgs {
		merged.appendFilterPkg(p)
	}

	merged.aliases = append(merged.aliases, base.aliases...)
	merged.aliases = append(merged.aliases, opts.aliases...)

	// renderers: file layer first, option layer appended after — last-wins
	// per TypeKey resolves at harvest, matching aliases' convention.
	merged.renderers = append(merged.renderers, base.renderers...)
	merged.renderers = append(merged.renderers, opts.renderers...)

	merged.urlRules = append(append(merged.urlRules, base.urlRules...), opts.urlRules...)
	merged.urlPresets = append(append(merged.urlPresets, base.urlPresets...), opts.urlPresets...)

	merged.cssMin = base.cssMin
	if opts.cssMin != nil {
		merged.cssMin = opts.cssMin
	}
	merged.jsMin = base.jsMin
	if opts.jsMin != nil {
		merged.jsMin = opts.jsMin
	}
	merged.cssFmt = base.cssFmt
	if opts.cssFmt != nil {
		merged.cssFmt = opts.cssFmt
	}
	merged.jsFmt = base.jsFmt
	if opts.jsFmt != nil {
		merged.jsFmt = opts.jsFmt
	}
	merged.classMerger = base.classMerger
	if opts.classMerger != nil {
		merged.classMerger = opts.classMerger
	}

	merged.errs = append(append(merged.errs, base.errs...), opts.errs...)

	merged.printWidth = base.printWidth
	if opts.printWidth > 0 {
		merged.printWidth = opts.printWidth
	}

	merged.tabWidth = base.tabWidth
	if opts.tabWidth > 0 {
		merged.tabWidth = opts.tabWidth
	}

	merged.importsMode = base.importsMode
	if opts.importsMode != gsxfmt.ImportsUnset {
		merged.importsMode = opts.importsMode
	}

	// MinifyLevel fields use minifyLevelSet as the sentinel so opts.MinifyNone
	// (zero) can be distinguished from "not set by caller". When opts explicitly
	// sets the level it wins; otherwise the base (env/file) value is preserved.
	merged.cssMinLevel = base.cssMinLevel
	merged.jsMinLevel = base.jsMinLevel
	if opts.minifyLevelSet {
		merged.cssMinLevel = opts.cssMinLevel
		merged.jsMinLevel = opts.jsMinLevel
		merged.minifyLevelSet = true
	}

	return merged
}
