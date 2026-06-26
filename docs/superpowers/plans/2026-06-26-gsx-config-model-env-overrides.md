# gsx Config Model + Env Overrides + Declarative Minify — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a curated environment-override layer and declarative per-asset minification to gsx's config, name the three-layer config model, and document it for users and contributors.

**Architecture:** A new `MinifyLevel` enum and `[minify]` TOML table extend the existing declarative config; an env-override registry (`applyEnvOverrides`) slots between `loadConfig` and `mergeConfig` to give `option > env > config` precedence; the resolved minify level is reduced to two booleans threaded through the existing `cssMin`/`jsMin` parameter chain to gate the minify passes in `generateFile`, and folded into the cache key. `gsx info` and the docs surface the new layer.

**Tech Stack:** Go 1.26.1, `github.com/BurntSushi/toml`, the gsx `gen` package and `internal/codegen`/`internal/cssmin`/`internal/jsmin` packages.

## Global Constraints

- Runtime (root `gsx` package) stays standard-library only; this work lives in `gen` and `internal/*` (tooling), which may use `golang.org/x/tools`. No new root-package deps.
- Go pinned to `GO_VERSION` 1.26.1 (gofmt drift otherwise).
- Before merge run `make ci` (build/vet/test both modules, examples drift, `gofmt` + `gsx fmt`).
- New user-facing env vars use the `GSX_<THING>` convention (underscore). The existing internal `GSXCACHE` / `GSX_PERF` are NOT user config and are not added to the registry.
- Default minify level is `safe` (the enum zero value), so an absent `[minify]` table and no env/option produce byte-identical output to today.
- Field/type visibility: keep identifiers unexported unless they are part of public API. `MinifyLevel` and its constants ARE exported (consumed by the public `gen.WithMinifyLevel` option); everything else (`envOverride`, `applyEnvOverrides`, config fields) stays unexported.
- `gsx` binary name collides with Ghostscript on PATH — invoke via `go run ./cmd/gsx …` in any manual check.

---

### Task 1: `MinifyLevel` type, config fields, and `[minify]` TOML schema

Adds the declarative layer for minification: the enum, the resolved config fields, the TOML schema, and strict parsing in `loadConfig`.

**Files:**
- Create: `gen/minify.go`
- Modify: `gen/main.go:34-48` (the `config` struct)
- Modify: `gen/configfile.go:28-35` (`tomlConfig`), `gen/configfile.go:101-147` (`loadConfig`)
- Test: `gen/minify_test.go`

**Interfaces:**
- Produces: `MinifyLevel` (exported `int` enum) with constants `MinifySafe` (zero value) and `MinifyNone`; methods `(MinifyLevel).String() string` (`"safe"`/`"none"`) and `(MinifyLevel).enabled() bool` (`false` only for `MinifyNone`); `parseMinifyLevel(s string) (MinifyLevel, error)`.
- Produces: `config.cssMinLevel`, `config.jsMinLevel` (`MinifyLevel`) and `config.minifyLevelSet bool`.

- [ ] **Step 1: Write the failing test**

Create `gen/minify_test.go`:

```go
package gen

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMinifyLevel_Basics(t *testing.T) {
	if MinifySafe != 0 {
		t.Fatalf("MinifySafe must be the zero value, got %d", MinifySafe)
	}
	if !MinifySafe.enabled() {
		t.Fatal("MinifySafe must be enabled")
	}
	if MinifyNone.enabled() {
		t.Fatal("MinifyNone must be disabled")
	}
	if MinifySafe.String() != "safe" || MinifyNone.String() != "none" {
		t.Fatalf("String(): safe=%q none=%q", MinifySafe.String(), MinifyNone.String())
	}
}

func TestParseMinifyLevel(t *testing.T) {
	for in, want := range map[string]MinifyLevel{"safe": MinifySafe, "none": MinifyNone} {
		got, err := parseMinifyLevel(in)
		if err != nil || got != want {
			t.Fatalf("parseMinifyLevel(%q) = %v, %v", in, got, err)
		}
	}
	if _, err := parseMinifyLevel("aggressive"); err == nil {
		t.Fatal("parseMinifyLevel(aggressive) must error")
	}
}

// writeTOML writes a gsx.toml into a temp dir and returns its path.
func writeTOML(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "gsx.toml")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadConfig_Minify(t *testing.T) {
	// Absent [minify] → both default to safe.
	cfg, err := loadConfig(writeTOML(t, "[filters]\nupper = \"example.com/x.Up\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.cssMinLevel != MinifySafe || cfg.jsMinLevel != MinifySafe {
		t.Fatalf("absent [minify] should be safe/safe, got %v/%v", cfg.cssMinLevel, cfg.jsMinLevel)
	}

	// Explicit levels.
	cfg, err = loadConfig(writeTOML(t, "[minify]\ncss = \"none\"\njs = \"safe\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.cssMinLevel != MinifyNone || cfg.jsMinLevel != MinifySafe {
		t.Fatalf("got css=%v js=%v", cfg.cssMinLevel, cfg.jsMinLevel)
	}

	// Invalid level → error naming the key.
	if _, err := loadConfig(writeTOML(t, "[minify]\ncss = \"agressive\"\n")); err == nil {
		t.Fatal("invalid minify.css should error")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./gen/ -run 'TestMinifyLevel|TestParseMinifyLevel|TestLoadConfig_Minify' -v`
Expected: FAIL — `undefined: MinifySafe` / `cfg.cssMinLevel undefined`.

- [ ] **Step 3: Create `gen/minify.go`**

```go
package gen

import "fmt"

// MinifyLevel is the minification level for one asset kind (<style> CSS or
// <script> JS). The zero value is MinifySafe, so an absent [minify] table — and
// the unset config — means today's always-on safe minification.
type MinifyLevel int

const (
	// MinifySafe runs gsx's built-in safe minifier (or a custom one installed via
	// gen.WithCSSMinifier / gen.WithJSMinifier). It is the default.
	MinifySafe MinifyLevel = iota
	// MinifyNone disables minification: the asset is emitted verbatim.
	MinifyNone
)

// String returns the TOML/CLI spelling of the level.
func (l MinifyLevel) String() string {
	if l == MinifyNone {
		return "none"
	}
	return "safe"
}

// enabled reports whether the minify pass should run for this level.
func (l MinifyLevel) enabled() bool { return l != MinifyNone }

// parseMinifyLevel parses a TOML/CLI level spelling, rejecting anything else.
func parseMinifyLevel(s string) (MinifyLevel, error) {
	switch s {
	case "safe":
		return MinifySafe, nil
	case "none":
		return MinifyNone, nil
	default:
		return 0, fmt.Errorf("invalid minify level %q (want \"safe\" or \"none\")", s)
	}
}
```

- [ ] **Step 4: Add config fields** in `gen/main.go` — extend the `config` struct (after `printWidth` at line 47):

```go
	printWidth   int // gsx.toml printWidth; 0 means "unset" → 80 at use
	cssMinLevel  MinifyLevel // <style> minification level (zero = MinifySafe)
	jsMinLevel   MinifyLevel // <script> minification level (zero = MinifySafe)
	minifyLevelSet bool      // true once an option (WithMinifyLevel) pinned the levels
```

- [ ] **Step 5: Add the TOML schema** in `gen/configfile.go` — extend `tomlConfig` (after `PrintWidth` at line 34) and add the `tomlMinify` type after `tomlRule`:

```go
	PrintWidth     int               `toml:"printWidth"`
	Minify         *tomlMinify       `toml:"minify"`
}

// tomlMinify is the [minify] table: per-asset level spellings. A nil pointer
// (table absent) leaves both levels at their default (safe). An empty string for
// a key (key absent) likewise leaves that asset's default.
type tomlMinify struct {
	CSS string `toml:"css"`
	JS  string `toml:"js"`
}
```

- [ ] **Step 6: Parse it** in `gen/configfile.go` `loadConfig`, before `cfg.printWidth = tc.PrintWidth` (line 145):

```go
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
	cfg.printWidth = tc.PrintWidth
```

- [ ] **Step 7: Run tests to verify they pass**

Run: `go test ./gen/ -run 'TestMinifyLevel|TestParseMinifyLevel|TestLoadConfig_Minify' -v`
Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add gen/minify.go gen/minify_test.go gen/main.go gen/configfile.go
git commit -m "feat(config): MinifyLevel enum + [minify] TOML schema"
```

---

### Task 2: Env-override registry and resolution wiring

Adds the env layer: a registry, `applyEnvOverrides`, and wiring into `resolveConfig` (between `loadConfig` and `mergeConfig`), including the no-config-file path so `GSX_MINIFY` works without a `gsx.toml`.

**Files:**
- Create: `gen/envconfig.go`
- Modify: `gen/main.go:209-219` (`resolveConfig`)
- Test: `gen/envconfig_test.go`

**Interfaces:**
- Consumes: `config.cssMinLevel`, `config.jsMinLevel`, `MinifySafe`, `MinifyNone` (Task 1).
- Produces: `envOverride` struct (`name`, `desc string`; `apply func(raw string, cfg *config) error`); `envOverrides []envOverride` (registry); `applyEnvOverrides(cfg config) (config, error)`.

- [ ] **Step 1: Write the failing test**

Create `gen/envconfig_test.go`:

```go
package gen

import "testing"

func TestApplyEnvOverrides_Minify(t *testing.T) {
	t.Setenv("GSX_MINIFY", "off")
	cfg, err := applyEnvOverrides(config{})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.cssMinLevel != MinifyNone || cfg.jsMinLevel != MinifyNone {
		t.Fatalf("GSX_MINIFY=off → none/none, got %v/%v", cfg.cssMinLevel, cfg.jsMinLevel)
	}

	t.Setenv("GSX_MINIFY", "on")
	cfg, err = applyEnvOverrides(config{cssMinLevel: MinifyNone, jsMinLevel: MinifyNone})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.cssMinLevel != MinifySafe || cfg.jsMinLevel != MinifySafe {
		t.Fatalf("GSX_MINIFY=on → safe/safe, got %v/%v", cfg.cssMinLevel, cfg.jsMinLevel)
	}

	t.Setenv("GSX_MINIFY", "banana")
	if _, err := applyEnvOverrides(config{}); err == nil {
		t.Fatal("GSX_MINIFY=banana must error")
	}
}

func TestApplyEnvOverrides_AbsentIsNoop(t *testing.T) {
	// No GSX_* set: file value (none) is preserved untouched.
	base := config{cssMinLevel: MinifyNone, jsMinLevel: MinifySafe}
	cfg, err := applyEnvOverrides(base)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.cssMinLevel != MinifyNone || cfg.jsMinLevel != MinifySafe {
		t.Fatalf("absent env should preserve file values, got %v/%v", cfg.cssMinLevel, cfg.jsMinLevel)
	}
}

func TestResolveConfig_EnvWithoutFile(t *testing.T) {
	// In an empty temp dir (no gsx.toml), env still applies.
	dir := t.TempDir()
	chdir(t, dir)
	t.Setenv("GSX_MINIFY", "off")
	merged, path, err := resolveConfig(config{})
	if err != nil {
		t.Fatal(err)
	}
	if path != "" {
		t.Fatalf("expected no config path, got %q", path)
	}
	if merged.cssMinLevel != MinifyNone || merged.jsMinLevel != MinifyNone {
		t.Fatalf("env-only resolve → none/none, got %v/%v", merged.cssMinLevel, merged.jsMinLevel)
	}
}

// chdir changes to dir for the duration of the test.
func chdir(t *testing.T, dir string) {
	t.Helper()
	orig, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chdir(orig) })
}
```

Add `import "os"` to the test file's import block (alongside `testing`).

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./gen/ -run 'TestApplyEnvOverrides|TestResolveConfig_EnvWithoutFile' -v`
Expected: FAIL — `undefined: applyEnvOverrides`.

- [ ] **Step 3: Create `gen/envconfig.go`**

```go
package gen

import (
	"fmt"
	"os"
	"strings"
)

// envOverride is one curated environment variable that overrides a declarative
// (gsx.toml) config value. The mechanism is general (one table, one pass); the
// coverage is selective — only knobs that legitimately vary dev↔prod get a var.
// See docs/guide/config.md and the design spec for the three-layer model.
type envOverride struct {
	name  string // GSX_<THING>
	desc  string // one-line help (surfaced by `gsx info`)
	apply func(raw string, cfg *config) error
}

// envOverrides is the registry of user-facing env overrides. NOTE: GSXCACHE and
// GSX_PERF are internal/test knobs, NOT user config, and are deliberately absent.
var envOverrides = []envOverride{
	{
		name: "GSX_MINIFY",
		desc: `minify <style>/<script>: "on" | "off" (overrides [minify])`,
		apply: func(raw string, cfg *config) error {
			switch strings.ToLower(strings.TrimSpace(raw)) {
			case "on":
				cfg.cssMinLevel, cfg.jsMinLevel = MinifySafe, MinifySafe
			case "off":
				cfg.cssMinLevel, cfg.jsMinLevel = MinifyNone, MinifyNone
			default:
				return fmt.Errorf("GSX_MINIFY=%q: want \"on\" or \"off\"", raw)
			}
			return nil
		},
	},
}

// applyEnvOverrides returns cfg with every PRESENT registered env var applied.
// It takes cfg by value and returns a copy (no mutation of the caller's config),
// matching mergeConfig's style. An invalid value is a hard error naming the var.
func applyEnvOverrides(cfg config) (config, error) {
	for _, o := range envOverrides {
		if raw, ok := os.LookupEnv(o.name); ok {
			if err := o.apply(raw, &cfg); err != nil {
				return config{}, err
			}
		}
	}
	return cfg, nil
}
```

- [ ] **Step 4: Wire into `resolveConfig`** — replace `gen/main.go:209-219` with:

```go
func resolveConfig(optCfg config) (merged config, configPath string, err error) {
	// Layer 1: file defaults (base is the zero config when no gsx.toml exists).
	var base config
	path, ok := discoverConfig(".")
	if ok {
		base, err = loadConfig(path)
		if err != nil {
			return config{}, "", err
		}
		configPath = path
	}
	// Layer 3: env overrides the file. Applied to base BEFORE the merge so the
	// final precedence is option > env > config (mergeConfig lets opts win).
	base, err = applyEnvOverrides(base)
	if err != nil {
		return config{}, "", err
	}
	// Layer 2: programmatic options win (existing last-wins merge).
	return mergeConfig(base, optCfg), configPath, nil
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./gen/ -run 'TestApplyEnvOverrides|TestResolveConfig_EnvWithoutFile' -v`
Expected: PASS.

- [ ] **Step 6: Regression — existing resolveConfig behavior unchanged**

Run: `go test ./gen/ -run 'TestResolveConfig|TestConfig|Config' -v`
Expected: PASS (no env set → `mergeConfig(zeroBase, opts)` is byte-identical to the old `return optCfg`).

- [ ] **Step 7: Commit**

```bash
git add gen/envconfig.go gen/envconfig_test.go gen/main.go
git commit -m "feat(config): curated GSX_* env-override layer (GSX_MINIFY)"
```

---

### Task 3: `WithMinifyLevel` option and `mergeConfig` precedence

Adds the programmatic layer for the minify level so code can pin it (`option > env`), and teaches `mergeConfig` to apply it.

**Files:**
- Modify: `gen/options.go` (add `WithMinifyLevel`)
- Modify: `gen/configfile.go:169-214` (`mergeConfig`)
- Test: `gen/minify_test.go` (extend)

**Interfaces:**
- Consumes: `config.cssMinLevel/jsMinLevel/minifyLevelSet`, `MinifyLevel` (Task 1).
- Produces: `gen.WithMinifyLevel(css, js MinifyLevel) Option`.

- [ ] **Step 1: Write the failing test** — append to `gen/minify_test.go`:

```go
func TestMergeConfig_MinifyPrecedence(t *testing.T) {
	// option > config: opts pin via WithMinifyLevel beats file base.
	base := config{cssMinLevel: MinifyNone, jsMinLevel: MinifyNone}
	var opts config
	WithMinifyLevel(MinifySafe, MinifySafe)(&opts)
	merged := mergeConfig(base, opts)
	if merged.cssMinLevel != MinifySafe || merged.jsMinLevel != MinifySafe {
		t.Fatalf("WithMinifyLevel should win: got %v/%v", merged.cssMinLevel, merged.jsMinLevel)
	}

	// No option set → base (env/file) value flows through unchanged.
	merged = mergeConfig(base, config{})
	if merged.cssMinLevel != MinifyNone || merged.jsMinLevel != MinifyNone {
		t.Fatalf("no option should keep base: got %v/%v", merged.cssMinLevel, merged.jsMinLevel)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./gen/ -run TestMergeConfig_MinifyPrecedence -v`
Expected: FAIL — `undefined: WithMinifyLevel`.

- [ ] **Step 3: Add `WithMinifyLevel`** to `gen/options.go` (after `WithFieldMatcher`):

```go
// WithMinifyLevel pins the minification level for <style> CSS and <script> JS,
// overriding both the [minify] config table and the GSX_MINIFY env var (code is
// the most deliberate layer: option > env > config). The level GATES the pass;
// a custom WithCSSMinifier/WithJSMinifier supplies the implementation used when
// the level is MinifySafe. MinifyNone emits the asset verbatim and the custom
// minifier is not called.
func WithMinifyLevel(css, js MinifyLevel) Option {
	return func(cfg *config) {
		cfg.cssMinLevel = css
		cfg.jsMinLevel = js
		cfg.minifyLevelSet = true
	}
}
```

- [ ] **Step 4: Teach `mergeConfig`** — in `gen/configfile.go`, before `return merged` (line 213), add:

```go
	merged.cssMinLevel = base.cssMinLevel
	merged.jsMinLevel = base.jsMinLevel
	if opts.minifyLevelSet {
		merged.cssMinLevel = opts.cssMinLevel
		merged.jsMinLevel = opts.jsMinLevel
		merged.minifyLevelSet = true
	}
	return merged
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./gen/ -run 'TestMergeConfig_MinifyPrecedence|TestLoadConfig_Minify' -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add gen/options.go gen/configfile.go gen/minify_test.go
git commit -m "feat(config): WithMinifyLevel option + mergeConfig precedence"
```

---

### Task 4: Thread the minify gate through the pipeline and gate `generateFile`

Reduces the resolved level to two booleans and threads them through the generate pipeline, gating the existing `cssmin.MinifyFile` / `jsmin.MinifyFile` calls. After this task, `[minify]`/`GSX_MINIFY`/`WithMinifyLevel` actually change output.

**Files:**
- Modify: `internal/codegen/emit.go:28` (`generateFile` signature + the two gate calls at lines 39, 47)
- Modify: `internal/codegen/batch.go:96` (`GeneratePackagesWithFilters` sig), `:554` (call), `:637` (`GeneratePackages` default call)
- Modify: `internal/codegen/codegen.go:115` (call)
- Modify: `internal/codegen/resolver.go:362` (probe call)
- Modify: `gen/gen.go:144-145` (`generate`), `gen/cache.go:16,58,140,234,235` (pipeline funcs + GeneratePackagesWithFilters calls)
- Modify: `gen/main.go:155` (dispatch), `gen/main.go` `runGenerate` sig + `:315` call + watch construction, `gen/watchsession.go:56` + `watchConfig`
- Test: `internal/codegen/minify_gate_test.go`

**Interfaces:**
- Consumes: `MinifyLevel.enabled()` (Task 1).
- Produces: every pipeline function gains trailing `cssMinify, jsMinify bool` params (after the existing `jsMin` param). `generateFile` skips `cssmin.MinifyFile` when `!cssMinify` and `jsmin.MinifyFile` when `!jsMinify`.

- [ ] **Step 1: Write the failing test**

Create `internal/codegen/minify_gate_test.go`:

```go
package codegen

import (
	"path/filepath"
	"strings"
	"testing"
)

// styleSrc is a component with a <style> whose body the safe minifier would
// collapse ("1px  2px" → "1px 2px"); with minify OFF the double space survives.
const styleSrc = "package x\n\ncomponent Page() {\n\t<style>\n\t\t.card { margin: 1px  2px; }\n\t</style>\n}\n"

func genStyle(t *testing.T, cssMinify bool) string {
	t.Helper()
	dir := t.TempDir()
	repoRoot, _ := filepath.Abs("../..")
	writeFile(t, dir, "go.mod",
		"module example.com/x\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	writeFile(t, dir, "page.gsx", styleSrc)
	out, err := GeneratePackagesWithFilters(dir, []string{dir}, nil, nil, nil, nil, nil, nil, cssMinify, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	pr := out[dir]
	if pr == nil || len(pr.Files) == 0 {
		t.Fatal("no generated output")
	}
	var sb strings.Builder
	for _, b := range pr.Files {
		sb.Write(b)
	}
	return sb.String()
}

func TestMinifyGate_CSS(t *testing.T) {
	on := genStyle(t, true)
	if !strings.Contains(on, "1px 2px") || strings.Contains(on, "1px  2px") {
		t.Fatalf("cssMinify=true should minify; got:\n%s", on)
	}
	off := genStyle(t, false)
	if !strings.Contains(off, "1px  2px") {
		t.Fatalf("cssMinify=false should preserve double space; got:\n%s", off)
	}
}
```

NOTE: the `GeneratePackagesWithFilters` call inserts `cssMinify, true` immediately before the trailing `srcOverride` (`nil`) argument — that is the new parameter position established in Step 3.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/codegen/ -run TestMinifyGate_CSS -v`
Expected: FAIL — too many arguments to `GeneratePackagesWithFilters`.

- [ ] **Step 3: Add params to `generateFile` and gate the passes** — `internal/codegen/emit.go`. Change the signature (line 28) to add `cssMinify, jsMinify bool` after `jsMin func(string) (string, error)`:

```go
func generateFile(file *ast.File, resolved map[ast.Node]types.Type, table filterTable, structFields, nodeProps map[string]map[string]bool, byo *byoData, fset *token.FileSet, cls *attrclass.Classifier, fm FieldMatcher, bag *diag.Bag, cssMin, jsMin func(string) (string, error), cssMinify, jsMinify bool) ([]byte, bool) {
```

Wrap the CSS pass (line 39) and JS pass (line 47):

```go
	if cssMinify {
		if err := cssmin.MinifyFile(file, cssMin); err != nil {
			bag.Add(diag.Diagnostic{Severity: diag.Error, Message: err.Error(), Source: "codegen"})
			return nil, false
		}
	}
```

```go
	if jsMinify {
		if err := jsmin.MinifyFile(file, jsMin); err != nil {
			bag.Add(diag.Diagnostic{Severity: diag.Error, Message: err.Error(), Source: "codegen"})
			return nil, false
		}
	}
```

(Keep each pass's existing error-handling body exactly; only the `if cssMinify {` / `if jsMinify {` wrapper is new. Verify the original `return nil, false` lines stay inside.)

- [ ] **Step 4: Thread through `internal/codegen` call sites**

`batch.go:96` — `GeneratePackagesWithFilters` signature: add `cssMinify, jsMinify bool` after `jsMin func(string) (string, error)` and before `srcOverride map[string][]byte`:

```go
func GeneratePackagesWithFilters(moduleDir string, dirs []string, filterPkgs []string, aliases []FilterAlias, cls *attrclass.Classifier, fm FieldMatcher, cssMin, jsMin func(string) (string, error), cssMinify, jsMinify bool, srcOverride map[string][]byte) (map[string]*PackageResult, error) {
```

`batch.go:554` — the internal `generateFile` call: append `, cssMinify, jsMinify`:

```go
			gen, genOK := generateFile(file, resolved, table, pf, np, byo, fset, cls, fm, bag, cssMin, jsMin, cssMinify, jsMinify)
```

`batch.go:637` — `GeneratePackages` default: pass `true, true` (minify-on default) before the trailing `nil`:

```go
	return GeneratePackagesWithFilters(moduleDir, dirs, nil, nil, nil, nil, nil, nil, true, true, nil)
```

`codegen.go:115` — this call is inside `GenerateWithFilters`/equivalent; thread its own `cssMinify, jsMinify` params (add them to that function's signature too if present — search the enclosing func). Append `, cssMinify, jsMinify`:

```go
		gen, genOK := generateFile(file, resolved, table, propFields, nodeProps, byo, fset, cls, fm, bag, cssMin, jsMin, cssMinify, jsMinify)
```

`resolver.go:362` — the analysis probe (output discarded). Preserve today's behavior (built-in minify) by passing `true, true`:

```go
			gen, genOK := generateFile(file, resolved, table, propFields, nodeProps, byo, fset, cls, nil, bag, nil, nil, true, true)
```

NOTE for the implementer: after editing `codegen.go:115`, run `go build ./internal/codegen/` — if the enclosing function (around `codegen.go`) does not already receive `cssMin, jsMin`, trace its caller and thread `cssMinify, jsMinify` the same way `cssMin, jsMin` already flow. Do not invent new defaults; carry the booleans from `GeneratePackagesWithFilters`.

- [ ] **Step 5: Thread through the `gen` pipeline**

`gen/cache.go` — add `cssMinify, jsMinify bool` after `jsMin func(string) (string, error)` in:
- `generateCached` (line 16)
- `generateModule` (line 58)
- `mustGen` (line 234)

and forward them at the two `GeneratePackagesWithFilters` calls (lines 140, 235) by inserting `, cssMinify, jsMinify` before the final `nil`:

```go
		out, err := codegen.GeneratePackagesWithFilters(root, miss, filterPkgs, aliases, cls, fm, cssMin, jsMin, cssMinify, jsMinify, nil)
```
```go
	out, err := codegen.GeneratePackagesWithFilters(root, dirs, filterPkgs, aliases, cls, fm, cssMin, jsMin, cssMinify, jsMinify, nil)
```

Forward from `generateCached` → `generateModule` and `generateCached` → `mustGen` by appending `, cssMinify, jsMinify` at those internal call sites (search within `generateCached`).

`gen/gen.go:144-145` — `generate` gains the params and forwards (built-in path is minify-on):

```go
func generate(paths []string, filterPkgs []string, cssMin, jsMin func(string) (string, error)) (Result, error) {
	return generateCached(paths, filterPkgs, nil, attrclass.Builtin(), nil, cssMin == nil && jsMin == nil, cssMin, jsMin, true, true)
}
```

- [ ] **Step 6: Thread through `main.go` dispatch, `runGenerate`, and watch**

`gen/main.go:155` (generate dispatch) — append the reduced booleans:

```go
		return runGenerate(cmdArgs, stdout, stderr, quiet, verbose, false, merged.filterPkgs, merged.aliases, merged.classifier(), merged.fieldMatcher, merged.cssMin, merged.jsMin, merged.cssMinLevel.enabled(), merged.jsMinLevel.enabled())
```

`runGenerate` — add `cssMinify, jsMinify bool` to the signature (after `cssMin, jsMin func(string) (string, error)`), forward to `generateCached` at line 315:

```go
	res, err := generateCached(paths, filterPkgs, aliases, cls, fm, useCache, cssMin, jsMin, cssMinify, jsMinify)
```

and pass into the `watchConfig` literal (the `runWatch(watchConfig{…})` block near line 303):

```go
			fm: fm, cssMin: cssMin, jsMin: jsMin, cssMinify: cssMinify, jsMinify: jsMinify,
```

`gen/watchsession.go` — add `cssMinify, jsMinify bool` fields to the `watchConfig` struct, and forward them at the `generateCached` call (line 56):

```go
	res, gerr := generateCached(cfg.paths, cfg.filterPkgs, cfg.aliases, cfg.cls, cfg.fm, true, cfg.cssMin, cfg.jsMin, cfg.cssMinify, cfg.jsMinify)
```

- [ ] **Step 7: Build the whole module and run the gate test**

Run: `go build ./... && go test ./internal/codegen/ -run TestMinifyGate_CSS -v`
Expected: PASS. If `go build` reports an arity mismatch, a call site was missed — fix per the same pattern (append `cssMinify, jsMinify`).

- [ ] **Step 8: Regression — existing minify golden still minifies by default**

Run: `go test ./internal/corpus/ -run TestCorpus -v 2>&1 | tail -20`
Expected: PASS — `style/minify_block.txtar` still pins minified output (the default path passes `true, true`).

- [ ] **Step 9: Add a gen-level end-to-end test** (config → gate) in `gen/minify_test.go`:

```go
func TestGenerate_MinifyNoneViaConfig(t *testing.T) {
	dir := t.TempDir()
	repoRoot, _ := filepath.Abs("../..")
	if err := os.WriteFile(filepath.Join(dir, "go.mod"),
		[]byte("module example.com/x\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "page.gsx"),
		[]byte("package x\n\ncomponent Page() {\n\t<style>\n\t\t.card { margin: 1px  2px; }\n\t</style>\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "gsx.toml"), []byte("[minify]\ncss = \"none\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".git"), []byte(""), 0o644); err != nil { // bound the config walk to dir
		t.Fatal(err)
	}
	chdir(t, dir)

	merged, _, err := resolveConfig(config{})
	if err != nil {
		t.Fatal(err)
	}
	res, err := generateCached([]string{"."}, merged.filterPkgs, merged.aliases, merged.classifier(), merged.fieldMatcher, false, merged.cssMin, merged.jsMin, merged.cssMinLevel.enabled(), merged.jsMinLevel.enabled())
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Errs) > 0 {
		t.Fatalf("generate errors: %v", res.Errs)
	}
	b, err := os.ReadFile(filepath.Join(dir, "page.x.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "1px  2px") {
		t.Fatalf("[minify] css=none should preserve double space; got:\n%s", b)
	}
}
```

Add `"strings"` to the `gen/minify_test.go` import block.

- [ ] **Step 10: Run the e2e test**

Run: `go test ./gen/ -run TestGenerate_MinifyNoneViaConfig -v`
Expected: PASS.

- [ ] **Step 11: Commit**

```bash
git add internal/codegen/ gen/cache.go gen/gen.go gen/main.go gen/watchsession.go gen/minify_test.go
git commit -m "feat(minify): gate built-in minify passes on the resolved level"
```

---

### Task 5: Fold the minify level into the cache key

Makes a level change (via file or env) invalidate the incremental cache — without this, a dev→prod switch on the cached (built-in) path serves stale output.

**Files:**
- Modify: `gen/cachekey.go:139` (`computeKey` sig), `:192` (the hash write)
- Modify: `gen/cache.go:104` (the `computeKey` call)
- Test: `gen/cachekey_test.go` (create or extend)

**Interfaces:**
- Consumes: `cssMinify, jsMinify bool` already threaded into `generateModule` (Task 4).
- Produces: `computeKey` gains trailing `cssMinify, jsMinify bool` params and folds `minify=css:<0|1>,js:<0|1>` into the digest.

- [ ] **Step 1: Write the failing test** — create `gen/cachekey_minify_test.go`:

```go
package gen

import (
	"testing"
)

// keyWith computes a cache key for a tiny synthetic graph differing only in the
// minify booleans; everything else is held constant.
func keyWith(t *testing.T, cssMinify, jsMinify bool) string {
	t.Helper()
	dir := t.TempDir()
	graph := map[string]pkgInfo{}
	k, err := computeKey(dir, graph, "example.com/x", "gomod", "gosum", "bctx", "codegenid", nil, nil, "clsfp", false, cssMinify, jsMinify)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func TestComputeKey_MinifyChangesKey(t *testing.T) {
	on := keyWith(t, true, true)
	offCSS := keyWith(t, false, true)
	offJS := keyWith(t, true, false)
	if on == offCSS {
		t.Fatal("css minify change must change the cache key")
	}
	if on == offJS {
		t.Fatal("js minify change must change the cache key")
	}
	// Stable: same inputs → same key.
	if on != keyWith(t, true, true) {
		t.Fatal("same inputs must yield the same key")
	}
}
```

NOTE: the `computeKey` call appends `cssMinify, jsMinify` after the existing trailing `hasFieldMatcher` bool (`false` here) — the parameter order established in Step 3.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./gen/ -run TestComputeKey_MinifyChangesKey -v`
Expected: FAIL — too many arguments to `computeKey`.

- [ ] **Step 3: Extend `computeKey`** — `gen/cachekey.go:139`, add `cssMinify, jsMinify bool` to the end of the signature:

```go
func computeKey(dir string, graph map[string]pkgInfo, modPath, goModHash, goSumHash, buildCtx, codegenID string, filterPkgs []string, aliases []codegen.FilterAlias, clsFingerprint string, hasFieldMatcher bool, cssMinify, jsMinify bool) (string, error) {
```

Fold them into the digest — replace the second `fmt.Fprintf(h, ...)` (line 192) so it also writes a `minify=` field. Use `b2i` for 0/1:

```go
	fmt.Fprintf(h, "filters=%s\x00aliases=%s\x00cls=%s\x00fm=%s\x00minify=css:%d,js:%d\x00own=%s\x00", strings.Join(pins, "\x00"), strings.Join(aliasPins, "\x00"), clsFingerprint, fmStr, b2i(cssMinify), b2i(jsMinify), own)
```

Add the helper at the end of `gen/cachekey.go`:

```go
// b2i maps a bool to 1/0 for stable inclusion in the cache-key digest.
func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}
```

- [ ] **Step 4: Pass the booleans at the call site** — `gen/cache.go:104`, append `, cssMinify, jsMinify`:

```go
		k, err := computeKey(dir, graph, modPath, goModH, goSumH, bctx, codegenID, filterPkgs, aliases, clsFingerprint, fm != nil, cssMinify, jsMinify)
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./gen/ -run 'TestComputeKey_MinifyChangesKey' -v`
Expected: PASS.

- [ ] **Step 6: Build + cache regression**

Run: `go build ./... && go test ./gen/ -run 'Cache|Key' -v`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add gen/cachekey.go gen/cache.go gen/cachekey_minify_test.go
git commit -m "fix(cache): fold resolved minify level into the cache key"
```

---

### Task 6: Surface minify + env in `gsx info`

Makes the env layer and the resolved minify level inspectable, both human and `--json`.

**Files:**
- Modify: `gen/main.go:166` (the `runInfo` call — pass minify levels)
- Modify: `gen/info.go` (`runInfo` — add Minify + Environment sections; extend the JSON path)
- Modify: `gen/manifest.go` (extend the manifest struct with minify + env)
- Test: `gen/info_test.go` (extend)

**Interfaces:**
- Consumes: `merged.cssMinLevel/jsMinLevel` (Task 1), `envOverrides` + `os.LookupEnv` (Task 2).
- Produces: human output lines `minify: css=<level> js=<level>` and an `Environment:` block; JSON gains `"minify"` and `"env"` keys.

- [ ] **Step 1: Write the failing test** — append to `gen/info_test.go` (mirror the file's existing `runInfo` invocation style; capture stdout via a `bytes.Buffer`):

```go
func TestRunInfo_MinifyAndEnv(t *testing.T) {
	t.Setenv("GSX_MINIFY", "off")
	var out, errb bytes.Buffer
	// css=none/js=none reflect GSX_MINIFY=off; predLabel/fm empty, no filters.
	code := runInfo(&out, &errb, ".", "", nil, nil, nil, "", nil,
		[]string{}, MinifyNone, MinifyNone)
	if code != 0 {
		t.Fatalf("runInfo exit %d, stderr=%s", code, errb.String())
	}
	s := out.String()
	if !strings.Contains(s, "minify: css=none js=none") {
		t.Fatalf("missing minify line:\n%s", s)
	}
	if !strings.Contains(s, "GSX_MINIFY") || !strings.Contains(s, "off") {
		t.Fatalf("missing env section:\n%s", s)
	}
}
```

Ensure `gen/info_test.go` imports `bytes` and `strings`.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./gen/ -run TestRunInfo_MinifyAndEnv -v`
Expected: FAIL — too many arguments to `runInfo`.

- [ ] **Step 3: Extend `runInfo` signature** — `gen/info.go`, add `cssMinLevel, jsMinLevel MinifyLevel` to the end of the parameter list:

```go
func runInfo(stdout, stderr io.Writer, dir, configPath string, filterPkgs []string, aliases []codegen.FilterAlias, cls *attrclass.Classifier, predLabel string, fm codegen.FieldMatcher, cmdArgs []string, cssMinLevel, jsMinLevel MinifyLevel) int {
```

- [ ] **Step 4: Human output** — in `runInfo`, after the Attribute-rules block and before `return 0`, add the minify line and the environment section:

```go
	fmt.Fprintf(stdout, "\nminify: css=%s js=%s\n", cssMinLevel, jsMinLevel)

	fmt.Fprintf(stdout, "\nEnvironment:\n")
	etw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	for _, o := range envOverrides {
		val := "unset"
		if raw, ok := os.LookupEnv(o.name); ok {
			val = raw + " (active)"
		}
		fmt.Fprintf(etw, "  %s\t%s\t%s\n", o.name, val, o.desc)
	}
	etw.Flush()
```

Add `"os"` to the `gen/info.go` import block.

- [ ] **Step 5: JSON output** — extend the manifest. In `gen/manifest.go`, add fields to the manifest struct (find the struct built by `buildManifest`) :

```go
	Minify manifestMinify  `json:"minify"`
	Env    []manifestEnv   `json:"env"`
```

and the helper types:

```go
type manifestMinify struct {
	CSS string `json:"css"`
	JS  string `json:"js"`
}

type manifestEnv struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Value       string `json:"value"`  // "" when unset
	Active      bool   `json:"active"`
}
```

Extend `buildManifest`'s signature to accept `cssMinLevel, jsMinLevel MinifyLevel` and populate `Minify`; build `Env` by ranging `envOverrides` with `os.LookupEnv`. In the `asJSON` branch of `runInfo` (gen/info.go), pass the two levels into `buildManifest`. (Add `"os"` to `manifest.go` imports if not present.)

- [ ] **Step 6: Update the `runInfo` call** — `gen/main.go:166`, append the levels:

```go
		return runInfo(stdout, stderr, ".", configPath, merged.filterPkgs, merged.aliases, merged.classifier(), merged.predLabel, merged.fieldMatcher, cmdArgs, merged.cssMinLevel, merged.jsMinLevel)
```

- [ ] **Step 7: Run tests + build**

Run: `go build ./... && go test ./gen/ -run 'TestRunInfo' -v`
Expected: PASS (existing `runInfo` tests updated for the new args; the JSON test, if present, includes `"minify"`/`"env"`).

- [ ] **Step 8: Manual smoke check**

Run: `cd $(mktemp -d) && printf 'module m\ngo 1.26.1\n' > go.mod && printf '[minify]\ncss="none"\n' > gsx.toml && GSX_MINIFY=on go run github.com/gsxhq/gsx/cmd/gsx -C . info` (from a checkout with a replace, or `go run ./cmd/gsx` in the repo against a temp dir)
Expected: `minify: css=safe js=safe` (env on beats file none) and an `Environment:` block showing `GSX_MINIFY  on (active)`.

- [ ] **Step 9: Commit**

```bash
git add gen/info.go gen/manifest.go gen/main.go gen/info_test.go
git commit -m "feat(info): report minify level + environment overrides"
```

---

### Task 7: Documentation — users (`config.md`) and contributors (`CLAUDE.md`)

Documents the three-layer model, `[minify]`, and the env layer for users, and records the contributor decision rule.

**Files:**
- Modify: `docs/guide/config.md`
- Modify: `CLAUDE.md`

**Interfaces:** none (docs only).

- [ ] **Step 1: Add the "Three layers" intro to `docs/guide/config.md`** — after the opening paragraph (before the first code block / "## Location & discovery"), insert:

````markdown
## The config model — three layers

gsx config has three layers, applied per knob with precedence
**option > env > config**:

1. **Declarative `gsx.toml`** (preferred) — anything expressible as data:
   filters, attribute rules, `printWidth`, `[minify]`. Start here.
2. **Programmatic `gen.With*` options** — only for values that are Go
   *functions* (a custom minifier, an attribute-classifier predicate, a field
   matcher); see [Extensions](./extensions.md).
3. **Environment overrides (`GSX_*`)** — a curated subset of declarative knobs
   that legitimately differ between dev and prod (e.g. `GSX_MINIFY`), changed
   without editing a file or recompiling.

A higher layer wins only where it is set; otherwise the value falls through. Run
`gsx info` to see the resolved config and which env overrides are active.
````

- [ ] **Step 2: Document `[minify]`** — add a subsection under "## Options" in `docs/guide/config.md`:

````markdown
### `[minify]` — asset minification level

gsx minifies the static CSS of `<style>` and the static JS of `<script>` blocks
at generate time. `[minify]` sets the level per asset; the default is `safe`
(minify with the built-in safe minifier), so omitting the table keeps today's
behavior.

```toml
[minify]
css = "safe"   # "safe" | "none"
js  = "none"   # disable JS minification (e.g. for a readable dev build)
```

- `safe` — run the built-in safe minifier (or a custom one installed via
  `gen.WithCSSMinifier` / `gen.WithJSMinifier`).
- `none` — emit the asset verbatim; a custom minifier is not called.

For dev/prod, prefer the `GSX_MINIFY` env override (below) over editing this
table.
````

- [ ] **Step 3: Add the "Environment overrides" section** — after "## Options" in `docs/guide/config.md`:

````markdown
## Environment overrides

A curated set of `GSX_*` environment variables override declarative config so
dev and prod differ without editing `gsx.toml` or writing Go. Env overrides the
file but is itself overridden by a programmatic option
(`option > env > config`).

| Variable | Values | Effect |
|---|---|---|
| `GSX_MINIFY` | `on` \| `off` | Force minification on or off for both `<style>` and `<script>`, overriding `[minify]`. |

A typical dev loop disables minification for readable output, while prod uses
the default:

```sh
GSX_MINIFY=off gsx generate .   # dev: verbatim CSS/JS
gsx generate .                  # prod: safe minification (default)
```

`gsx info` lists every variable, whether it is set, and what it does. Internal
knobs like `GSXCACHE` are not config — they are not listed here.
````

- [ ] **Step 4: Note the func-valued-stays-code reference** — verify the existing "What is *not* in `gsx.toml`" section still reads correctly alongside the new model; if it duplicates the layer-2 description, leave it (it is the detailed version) but ensure no contradiction with "option > env > config".

- [ ] **Step 5: Add the contributor rule to `CLAUDE.md`** — under "## Conventions", add:

````markdown
### Configuration — where a new knob goes

Three layers, precedence **option > env > config**. To add a knob:

1. **Can it be data?** → add it to `gsx.toml` (`tomlConfig` in
   `gen/configfile.go`) and the resolved `config` struct. This is the default.
2. **Is it a Go function?** → add a `gen.With*` option in `gen/options.go`
   (functions can't be named in TOML).
3. **Does it vary dev↔prod?** → *also* register a `GSX_<THING>` var in
   `gen/envconfig.go` (`envOverrides`). A knob is never env-only.

Any knob that changes generated output MUST be folded into `computeKey`
(`gen/cachekey.go`), or the incremental cache will serve stale output. Document
user-facing knobs in `docs/guide/config.md`.
````

- [ ] **Step 6: Verify docs build inputs are consistent**

Run: `go run ./cmd/gsx info 2>/dev/null || true` then re-read the new `config.md` sections to confirm the `GSX_MINIFY` table and the `gsx info` output agree (values `on`/`off`, the `minify:` line).
Expected: consistent wording; fix any drift inline.

- [ ] **Step 7: Commit**

```bash
git add docs/guide/config.md CLAUDE.md
git commit -m "docs(config): three-layer model, [minify], env overrides"
```

---

## Final verification

- [ ] **Run the full CI mirror**

Run: `make ci`
Expected: PASS — build/vet/test both modules, examples drift clean, `gofmt` + `gsx fmt` clean. (The `docs` VitePress job is separate and not part of `make ci`.)

- [ ] **If goldens shifted unexpectedly**, regenerate and re-verify:

Run: `go test ./internal/corpus -run TestCorpus -update && go test ./internal/corpus -run TestCorpus`
Expected: only intentional changes; `coverage.golden` in sync. (No new corpus case is added here — minify-level is a config knob, not syntax, and the corpus batches one `GeneratePackages` call with no per-case config; the existing `style/minify_block.txtar` pins default-on, and Tasks 4/4-e2e cover minify-off at the codegen and gen layers.)

## Self-Review notes (resolved during planning)

- **Spec coverage:** three-layer model → Task 7 (docs) + Tasks 1-3 (code); env layer → Task 2; declarative minify → Tasks 1,3,4; cache invalidation requirement → Task 5; `gsx info` env section → Task 6; audit naming convention (`GSX_*` vs `GSXCACHE`) → Task 2 (registry comment) + Task 7 (`CLAUDE.md`). The two spec "Open questions" are settled: `WithMinifyLevel(css, js MinifyLevel)` takes the exported constants (Task 3); `gsx info --json` extends the manifest with new top-level `"minify"` and `"env"` keys (Task 6).
- **Corpus deviation:** flagged above — minify-level coverage is at the codegen/gen layers, not a new txtar case, because the corpus has no per-case config plumbing. Surface this at plan review; if a corpus case is required, an extra task would extend the corpus loader (out of current scope).
- **Type consistency:** `cssMinify, jsMinify bool` parameter naming and trailing-position convention used uniformly across `generateFile`, `GeneratePackagesWithFilters`, `generateCached`, `generateModule`, `mustGen`, `runGenerate`, `computeKey`, and `watchConfig`. `MinifyLevel`/`MinifySafe`/`MinifyNone` spelled consistently.
