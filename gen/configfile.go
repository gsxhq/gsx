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
	URLAttrs       *tomlURLAttrs     `toml:"url_attrs"`
	URLPresets     []string          `toml:"url_presets"`
	Formatter      *tomlFormatter    `toml:"formatter"`
	Minify         *tomlMinify       `toml:"minify"`
	ClassMerger    string            `toml:"class_merger"`
	Dev            *tomlDev          `toml:"dev"`
	// Serialization selects the tag-shape serialization mode: "canonical"
	// (default, key absent) or "verbatim". See gen.Serialization.
	Serialization string `toml:"serialization"`
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
	// Upstream is the dev backend's origin, ${VAR}-expanded against the merged
	// shell+.env (e.g. "http://localhost${ADDR}"). It is observational only —
	// gsx dev never sets the app's listen address — and feeds both the health
	// probe and the GSX_DEV_UPSTREAM env injected into the vite child. Empty
	// defaults to http://localhost:<port>, the port resolveGoPort resolved (an
	// explicit GO_PORT, else the first free port from 7777) — GO_PORT has
	// exactly one reader, and it is not the upstream resolver.
	Upstream string `toml:"upstream"`
	// Health is the path probed on Upstream (default "/healthz").
	Health string `toml:"health"`
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
// tomlURLAttrs is the [url_attrs] table: three name-matching arrays that apply
// to every element, plus [url_attrs.tags.<element>] sub-tables scoping rules to
// one element the way the built-in floor scopes `content` to <meta>.
type tomlURLAttrs struct {
	tomlRuleSet
	Tags map[string]tomlRuleSet `toml:"tags"`
}

type tomlRuleSet struct {
	Names    []string `toml:"names"`
	Prefixes []string `toml:"prefixes"`
	Suffixes []string `toml:"suffixes"`
}

// UnmarshalTOML decodes [url_attrs] by hand so a shape mistake produces OUR
// message rather than the decoder's, which would name the internal Go type. The
// common mistake is the retired array-of-tables form, so that one gets a
// migration hint instead of a type error.
func (u *tomlURLAttrs) UnmarshalTOML(v any) error {
	table, ok := v.(map[string]any)
	if !ok {
		if _, wasArray := v.([]map[string]any); wasArray {
			return fmt.Errorf("url_attrs is a table, not a list of tables; replace each [[url_attrs]] name/prefix entry with one [url_attrs] table:\n\n[url_attrs]\nnames    = [\"data-href\"]\nprefixes = [\"data-url-\"]\nsuffixes = [\"-url\"]")
		}
		return fmt.Errorf("url_attrs must be a table, got %T", v)
	}
	for key, raw := range table {
		switch key {
		case "names", "prefixes", "suffixes":
			vals, err := tomlStringSlice(key, raw)
			if err != nil {
				return err
			}
			switch key {
			case "names":
				u.Names = vals
			case "prefixes":
				u.Prefixes = vals
			case "suffixes":
				u.Suffixes = vals
			}
		case "tags":
			tags, ok := raw.(map[string]any)
			if !ok {
				return fmt.Errorf("url_attrs.tags must be a table of elements, got %T", raw)
			}
			u.Tags = map[string]tomlRuleSet{}
			for tag, rawSet := range tags {
				set, ok := rawSet.(map[string]any)
				if !ok {
					return fmt.Errorf("url_attrs.tags.%s must be a table, got %T", tag, rawSet)
				}
				var rs tomlRuleSet
				for k, rv := range set {
					vals, err := tomlStringSlice("url_attrs.tags."+tag+"."+k, rv)
					if err != nil {
						return err
					}
					switch k {
					case "names":
						rs.Names = vals
					case "prefixes":
						rs.Prefixes = vals
					case "suffixes":
						rs.Suffixes = vals
					default:
						return fmt.Errorf("url_attrs.tags.%s: unknown key %q (want names, prefixes or suffixes)", tag, k)
					}
				}
				u.Tags[tag] = rs
			}
		default:
			return fmt.Errorf("url_attrs: unknown key %q (want names, prefixes, suffixes or tags)", key)
		}
	}
	return nil
}

// tomlStringSlice converts a decoded TOML value to []string, naming who on a
// type mismatch.
func tomlStringSlice(who string, raw any) ([]string, error) {
	list, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("%s must be a list of strings, got %T", who, raw)
	}
	out := make([]string, 0, len(list))
	for _, item := range list {
		str, ok := item.(string)
		if !ok {
			return nil, fmt.Errorf("%s must contain only strings, got %T", who, item)
		}
		out = append(out, str)
	}
	return out, nil
}

func (r tomlRuleSet) toRuleSet() attrclass.RuleSet {
	return attrclass.RuleSet{Names: r.Names, Prefixes: r.Prefixes, Suffixes: r.Suffixes}
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

	if tc.URLAttrs != nil {
		rules := attrclass.Rules{URL: tc.URLAttrs.toRuleSet()}
		for tag, set := range tc.URLAttrs.Tags {
			if rules.URLTags == nil {
				rules.URLTags = map[string]attrclass.RuleSet{}
			}
			rules.URLTags[strings.ToLower(tag)] = set.toRuleSet()
		}
		if err := rules.Valid(); err != nil {
			return config{}, fmt.Errorf("%s: %w", path, err)
		}
		cfg.urlRules = cfg.urlRules.Merge(rules.URL)
		for tag, set := range rules.URLTags {
			if cfg.urlTagRules == nil {
				cfg.urlTagRules = map[string]attrclass.RuleSet{}
			}
			cfg.urlTagRules[tag] = cfg.urlTagRules[tag].Merge(set)
		}
	}
	for _, name := range tc.URLPresets {
		rules, ok := attrclass.Preset(name)
		if !ok {
			return config{}, fmt.Errorf("%s: url_presets: unknown preset %q (known: %s)", path, name, strings.Join(attrclass.PresetNames(), ", "))
		}
		cfg.urlRules = cfg.urlRules.Merge(rules.URL)
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
	cfg.serialization, err = parseSerialization(tc.Serialization)
	if err != nil {
		return config{}, fmt.Errorf("%s: %w", path, err)
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

	merged.urlRules = base.urlRules.Merge(opts.urlRules)
	for _, src := range []map[string]attrclass.RuleSet{base.urlTagRules, opts.urlTagRules} {
		for tag, set := range src {
			if merged.urlTagRules == nil {
				merged.urlTagRules = map[string]attrclass.RuleSet{}
			}
			merged.urlTagRules[tag] = merged.urlTagRules[tag].Merge(set)
		}
	}
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

	// Serialization uses serializationSet as the sentinel so opts.SerializationCanonical
	// (zero) can be distinguished from "not set by caller" — mirrors minifyLevelSet
	// above. When opts explicitly sets it (WithSerialization) it wins; otherwise the
	// base (file) value is preserved.
	merged.serialization = base.serialization
	merged.serializationSet = base.serializationSet
	if opts.serializationSet {
		merged.serialization = opts.serialization
		merged.serializationSet = true
	}

	return merged
}
