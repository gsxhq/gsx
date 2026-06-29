# Class Merger as Configuration — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the class-merge strategy a declarative, gen-driven extension (`gen.WithClassMerger` + `class_merger` in `gsx.toml`), emitted by codegen as a direct reference, replacing the mutable runtime global `gsx.ClassMerger`.

**Architecture:** Approach A from the spec — drop the global; runtime helpers take an explicit `merge func([]string) string` first argument; codegen always passes one (`gsx.DefaultClassMerge` by default, or a direct reference `<alias>.<FuncName>` to a configured merger). `Attrs.Class()` returns the raw joined class so the single outer site merges once. The configured merger must already be `func([]string) string` (validated by go/types); a non-conforming signature is a generate-time error.

**Tech Stack:** Go 1.26.1, `golang.org/x/tools/go/packages` (tooling only), `github.com/BurntSushi/toml`, txtar corpus.

## Global Constraints

- Root `gsx` package is **standard-library only**. The merger dependency appears only in the user's generated code + `go.mod`, never in the runtime.
- Go pinned to `GO_VERSION` in `.github/workflows/ci.yml` (currently **1.26.1**); a different minor reintroduces gofmt drift.
- **Never hand-edit `.x.go` or golden files** — regenerate with `go test ./internal/corpus -run TestCorpus -update`, then verify without `-update`.
- Any knob that changes generated output MUST fold into `computeKey` (`gen/cachekey.go`) or the cache serves stale output.
- Precedence: **option > env > config**. This knob is option + config only (no env — merge strategy does not vary dev↔prod).
- Run `make check` for the inner loop; `make ci` before merge.
- Verify the binary with `go run ./cmd/gsx version` (the `gsx` name collides with Ghostscript on PATH).

---

### Task 1: Runtime — drop the global, thread the merger, regenerate

Replace the `gsx.ClassMerger` global with an explicit per-call `merge func([]string) string`, export the default, make `Attrs.Class()` return the raw join, update codegen to always pass `gsx.DefaultClassMerge`, and regenerate goldens. This task keeps `merge` defaulted so the tree stays green with no config consumer yet.

**Files:**
- Modify: `class.go` (whole file — the merge seam)
- Modify: `attrs.go:88-95` (`Attrs.Class()`)
- Modify: `class_test.go` (replace global-swap test)
- Modify: `internal/codegen/emit.go` (5 emit sites + thread `mergeExpr`)
- Regenerate: `internal/corpus/testdata/**/*.golden`, `examples/*.txtar`

**Interfaces:**
- Produces: `func DefaultClassMerge(tokens []string) string`; `func ClassString(merge func([]string) string, parts ...ClassPart) string`; `func (gw *Writer) Class(merge func([]string) string, parts ...ClassPart)`; `func (gw *Writer) ClassMerged(merge func([]string) string, extra string, parts ...ClassPart)`; `func (a Attrs) Class() string` (now raw join, unchanged signature).
- Consumes: nothing from later tasks. Codegen emits the literal `gsx.DefaultClassMerge` as the merge arg.

- [ ] **Step 1: Rewrite `class.go` — export default, drop global, thread `merge`**

Replace the body of `class.go` from the `ClassMerger` declaration through `ClassMerged` with:

```go
// DefaultClassMerge is the built-in class-merge strategy: it keeps the LAST
// occurrence of each token (caller/last-wins), preserving the surviving tokens
// in source order. e.g. "a b a" -> "b a". Generated code passes this as the
// merge function when no class_merger is configured.
func DefaultClassMerge(tokens []string) string {
	lastIdx := make(map[string]int, len(tokens))
	for i, t := range tokens {
		lastIdx[t] = i
	}
	out := make([]string, 0, len(tokens))
	for i, t := range tokens {
		if lastIdx[t] == i {
			out = append(out, t)
		}
	}
	return strings.Join(out, " ")
}

// classTokens flattens the on parts into whitespace-split, non-empty tokens.
func classTokens(parts []ClassPart) []string {
	var toks []string
	for _, p := range parts {
		if !p.on {
			continue
		}
		toks = append(toks, strings.Fields(p.s)...)
	}
	return toks
}

// ClassString returns the merged class string for parts, run through merge (the
// value form of gw.Class), so generated code can place a composed class into an
// Attrs map.
func ClassString(merge func(tokens []string) string, parts ...ClassPart) string {
	return merge(classTokens(parts))
}

// Class composes parts, runs them through merge, and writes the escaped class
// attribute value.
func (gw *Writer) Class(merge func(tokens []string) string, parts ...ClassPart) {
	if gw.err != nil {
		return
	}
	gw.AttrValue(merge(classTokens(parts)))
}

// ClassMerged writes a class attribute composed of parts plus the extra string
// (e.g. a fallthrough Attrs.Class()), running everything through merge. It writes
// nothing when the merged token set is empty — so a root element with no class
// and no fallthrough class stays attribute-free.
func (gw *Writer) ClassMerged(merge func(tokens []string) string, extra string, parts ...ClassPart) {
	if gw.err != nil {
		return
	}
	all := parts
	if strings.TrimSpace(extra) != "" {
		all = append(append([]ClassPart{}, parts...), Class(extra))
	}
	merged := merge(classTokens(all))
	if merged == "" {
		return
	}
	gw.writeStr(` class="`)
	gw.AttrValue(merged)
	gw.writeStr(`"`)
}
```

Keep `ClassPart`, `Class`, `ClassIf`, `StyleString`, and `Style` unchanged. Delete the `ClassMerger` var and the old `defaultClassMerge`.

- [ ] **Step 2: Update `Attrs.Class()` to return the raw join (`attrs.go`)**

Replace `Attrs.Class()`:

```go
// Class returns the raw joined class string from the bag's "class" entry, or "".
// It does NOT merge/dedupe — the single outer codegen-emitted class site applies
// the configured merger exactly once over the bag class plus the root's parts.
func (a Attrs) Class() string {
	v, ok := a["class"]
	if !ok {
		return ""
	}
	return strings.TrimSpace(toStr(v))
}
```

Verify `attrs.go` still imports `strings` (it uses `strings` elsewhere; if not, add it).

- [ ] **Step 3: Replace the global-swap test in `class_test.go`**

Remove `TestClassMergerOverride`. Add:

```go
func TestDefaultClassMergeLastWins(t *testing.T) {
	if got := DefaultClassMerge([]string{"a", "b", "a"}); got != "b a" {
		t.Fatalf("got %q, want %q", got, "b a")
	}
}

func TestClassStringUsesPassedMerger(t *testing.T) {
	merge := func(tokens []string) string { return "M:" + strings.Join(tokens, ",") }
	if got := ClassString(merge, Class("a b")); got != "M:a,b" {
		t.Fatalf("got %q", got)
	}
}

func TestAttrsClassRawNoMerge(t *testing.T) {
	a := Attrs{"class": "x  y x"}
	if got := a.Class(); got != "x  y x" {
		t.Fatalf("Attrs.Class() = %q, want raw %q", got, "x  y x")
	}
}
```

Update any other `class_test.go` callers of `ClassString`/`gw.Class`/`gw.ClassMerged` to pass `DefaultClassMerge` as the first arg.

- [ ] **Step 4: Run runtime tests (expect FAIL until codegen + render path compile)**

Run: `go test . -run 'TestDefaultClassMerge|TestClassStringUsesPassedMerger|TestAttrsClassRaw' -v`
Expected: PASS for these three (they don't depend on codegen).

- [ ] **Step 5: Thread `mergeExpr` through the 5 codegen emit sites (`internal/codegen/emit.go`)**

`generateFile` computes `mergeExpr := "gsx.DefaultClassMerge"` (a constant for now; Task 2 makes it configurable) and threads it as a new `mergeExpr string` parameter to `genComponent` → `emitRootElement` → `emitRootComposedClass`, `emitRootStaticClass`, `emitClassAttr`, and `classEntryExpr`. The `emitSpread` closure captures `mergeExpr` from `emitRootElement` scope.

Change the emitted strings:

`emitSpread` no-class branch (≈ line 501):
```go
fmt.Fprintf(b, "\t\t_gsxgw.ClassMerged(%s, _gsxp.Attrs.Class())\n", mergeExpr)
```

`emitRootComposedClass` (≈ line 694):
```go
fmt.Fprintf(b, "\t\t_gsxgw.Class(%s, %s)\n", mergeExpr, strings.Join(parts, ", "))
```

`emitRootStaticClass` (≈ line 703) — add a `mergeExpr string` param:
```go
fmt.Fprintf(b, "\t\t_gsxgw.Class(%s, gsx.Class(%s), gsx.Class(_gsxp.Attrs.Class()))\n", mergeExpr, strconv.Quote(a.Value))
```

`emitClassAttr` (≈ line 1367):
```go
fmt.Fprintf(b, "\t\t_gsxgw.Class(%s, %s)\n", mergeExpr, strings.Join(parts, ", "))
```

`classEntryExpr` (≈ line 2227) — add a `mergeExpr string` param and emit it first:
```go
return fmt.Sprintf("%s.ClassString(%s, %s)", rtPkg, mergeExpr, strings.Join(parts, ", ")), usedPkgs, nil
```
(When `rtPkg` is the gsx alias, `mergeExpr` for the default is `<rtPkg>.DefaultClassMerge`; for the constant default in this task use the same `rtPkg`-qualified form. Compute `mergeExpr` consistently with the rtPkg used here.)

Update every call site of these five functions to pass `mergeExpr`.

- [ ] **Step 6: Build codegen**

Run: `go build ./internal/codegen/...`
Expected: compiles (all signatures threaded).

- [ ] **Step 7: Regenerate goldens and verify**

Run: `go test ./internal/corpus -run TestCorpus -update`
Then: `go test ./internal/corpus -run TestCorpus`
Expected: PASS. `generated.x.go.golden` files now show `gsx.DefaultClassMerge`/`_gsxstd.DefaultClassMerge` as the first class arg; **`render.golden` files are unchanged** (default merge is idempotent, so the removed double-merge produces identical output).

- [ ] **Step 8: Run full check**

Run: `make check`
Expected: PASS (build/vet/test both modules, examples drift, gofmt + gsx fmt).

- [ ] **Step 9: Commit**

```bash
git add class.go attrs.go class_test.go internal/codegen/emit.go internal/corpus/testdata examples
git commit -m "feat: thread class merger explicitly, drop gsx.ClassMerger global

Export DefaultClassMerge; runtime class helpers take a merge func; codegen
always passes gsx.DefaultClassMerge. Attrs.Class() returns the raw join so the
single outer site merges once (removes the hidden double-merge). Render output
unchanged; generated goldens regenerated."
```

---

### Task 2: Codegen — configurable merger reference (`ClassMergerRef`, validation, import alias)

Add `codegen.Options.ClassMerger`, validate the named merger is `func([]string) string` via go/types, register a reserved import alias, and make `mergeExpr` resolve to `<alias>.<FuncName>` when configured. Tested directly through `codegen` with a configured merger; no `gen` consumer yet.

**Files:**
- Create: `internal/codegen/classmerger.go` (`ClassMergerRef`, validation, alias)
- Modify: `internal/codegen/module.go:21-40` (`Options.ClassMerger`)
- Modify: `internal/codegen/emit.go` (compute `mergeExpr` from the ref; register import)
- Test: `internal/codegen/classmerger_test.go`

**Interfaces:**
- Consumes: from Task 1, the `mergeExpr` threading and `gsx.DefaultClassMerge` default.
- Produces: `type ClassMergerRef struct { PkgPath, FuncName string }`; `Options.ClassMerger *ClassMergerRef`; `func validateClassMerger(dir string, ref *ClassMergerRef) error` (returns a clear error if the symbol is missing or not `func([]string) string`). The reserved alias is `_gsxcm`.

- [ ] **Step 1: Write the failing test**

```go
// internal/codegen/classmerger_test.go
package codegen

import (
	"strings"
	"testing"
)

func TestValidateClassMergerSignature(t *testing.T) {
	dir := writeTempMergerPkg(t, `package mrg
func Good(t []string) string { return "" }
func BadVariadic(t ...any) string { return "" }
func BadReturn(t []string) int { return 0 }
`)
	if err := validateClassMerger(dir, &ClassMergerRef{PkgPath: "mrgmod/mrg", FuncName: "Good"}); err != nil {
		t.Fatalf("Good: unexpected error: %v", err)
	}
	err := validateClassMerger(dir, &ClassMergerRef{PkgPath: "mrgmod/mrg", FuncName: "BadVariadic"})
	if err == nil || !strings.Contains(err.Error(), "func([]string) string") {
		t.Fatalf("BadVariadic: want signature error, got %v", err)
	}
	if err := validateClassMerger(dir, &ClassMergerRef{PkgPath: "mrgmod/mrg", FuncName: "Missing"}); err == nil {
		t.Fatalf("Missing: want error, got nil")
	}
}
```

Add a `writeTempMergerPkg(t, src string) (dir string)` helper in the test that writes `go.mod` (`module mrgmod`, `go 1.26.1`) + `mrg/mrg.go` to `t.TempDir()` and returns the module dir. (Model it on existing codegen test temp-module helpers if present.)

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./internal/codegen -run TestValidateClassMergerSignature -v`
Expected: FAIL — `validateClassMerger` / `ClassMergerRef` undefined.

- [ ] **Step 3: Implement `internal/codegen/classmerger.go`**

```go
package codegen

import (
	"fmt"
	"go/types"

	"golang.org/x/tools/go/packages"
)

// classMergerAlias is the reserved import alias for the configured class-merger
// package. Uses the _gsx prefix so it never collides with a user symbol.
const classMergerAlias = "_gsxcm"

// ClassMergerRef names the configured class merger: an exported package-level
// identifier (func decl or var of func type) whose type is exactly
// func([]string) string. Codegen emits a direct reference _gsxcm.<FuncName>.
type ClassMergerRef struct {
	PkgPath  string
	FuncName string
}

// validateClassMerger type-checks ref.PkgPath and verifies ref.FuncName names an
// exported package-level object whose type is exactly func([]string) string.
// Returns a clear, user-facing error otherwise (missing symbol, or wrong
// signature with a pointer at the wrapper idiom).
func validateClassMerger(dir string, ref *ClassMergerRef) error {
	cfg := &packages.Config{Mode: packages.NeedTypes | packages.NeedTypesInfo | packages.NeedDeps | packages.NeedImports, Dir: dir}
	pkgs, err := packages.Load(cfg, ref.PkgPath)
	if err != nil {
		return fmt.Errorf("class_merger: loading %q: %w", ref.PkgPath, err)
	}
	if len(pkgs) == 0 || pkgs[0].Types == nil {
		return fmt.Errorf("class_merger: package %q not found", ref.PkgPath)
	}
	obj := pkgs[0].Types.Scope().Lookup(ref.FuncName)
	if obj == nil || !obj.Exported() {
		return fmt.Errorf("class_merger: %q has no exported %s", ref.PkgPath, ref.FuncName)
	}
	sig, ok := obj.Type().(*types.Signature)
	if !ok {
		return classMergerSigErr(ref, obj.Type())
	}
	if !isStringSliceToString(sig) {
		return classMergerSigErr(ref, sig)
	}
	return nil
}

// isStringSliceToString reports whether sig is exactly func([]string) string
// (non-variadic, one param []string, one result string).
func isStringSliceToString(sig *types.Signature) bool {
	if sig.Variadic() || sig.Params().Len() != 1 || sig.Results().Len() != 1 {
		return false
	}
	p, ok := sig.Params().At(0).Type().(*types.Slice)
	if !ok {
		return false
	}
	if b, ok := p.Elem().(*types.Basic); !ok || b.Kind() != types.String {
		return false
	}
	r, ok := sig.Results().At(0).Type().(*types.Basic)
	return ok && r.Kind() == types.String
}

func classMergerSigErr(ref *ClassMergerRef, got types.Type) error {
	return fmt.Errorf("class_merger %q.%s has signature %s; it must be func([]string) string. "+
		"Wrap it in a one-line exported func in your own package — see docs/guide/config.md#class_merger",
		ref.PkgPath, ref.FuncName, got)
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./internal/codegen -run TestValidateClassMergerSignature -v`
Expected: PASS.

- [ ] **Step 5: Add `Options.ClassMerger` and wire `mergeExpr` + import (`module.go`, `emit.go`)**

In `module.go` `Options`, add:
```go
	ClassMerger *ClassMergerRef // configured class merger; nil = DefaultClassMerge
```

In `emit.go` `generateFile`, replace the constant `mergeExpr := "gsx.DefaultClassMerge"` (from Task 1) with:
```go
mergeExpr := rtPkg + ".DefaultClassMerge"
if opts.ClassMerger != nil {
	mergeExpr = classMergerAlias + "." + opts.ClassMerger.FuncName
	imports[opts.ClassMerger.PkgPath] = true // import registered under classMergerAlias
}
```
where `rtPkg` is the gsx runtime import alias already used in this file (use the same identifier the file uses for `gsx.`-qualified calls). Ensure `generateFile` has access to `opts` (thread `opts.ClassMerger` in if not already present).

In `writeImports` (the import emitter), give `opts.ClassMerger.PkgPath` the reserved alias `classMergerAlias` (mirror the `filterAlias` handling — a `map[path]alias` entry so the import line is `_gsxcm "<pkgPath>"`). Confirm collision handling: the `_gsxcm` alias is `_gsx`-prefixed so it cannot collide with user imports.

- [ ] **Step 6: Add an emit test for a configured merger**

```go
// internal/codegen/classmerger_test.go (add)
func TestGeneratedClassUsesConfiguredMerger(t *testing.T) {
	// generate a component with a static root class, ClassMerger set, assert the
	// emitted .x.go references _gsxcm.Merge and imports the merger pkg under _gsxcm.
	got := generateClassFixture(t, &ClassMergerRef{PkgPath: "mrgmod/mrg", FuncName: "Merge"})
	if !strings.Contains(got, `_gsxcm "mrgmod/mrg"`) {
		t.Fatalf("missing aliased import:\n%s", got)
	}
	if !strings.Contains(got, "_gsxgw.Class(_gsxcm.Merge,") {
		t.Fatalf("missing direct merger reference:\n%s", got)
	}
}
```
Implement `generateClassFixture(t, ref)` to write a tiny `.gsx` (`component Card() { <section class="card">{children}</section> }`) plus the merger pkg (`func Merge(t []string) string { return "" }`) into a temp module and run `GenerateDirs` with `Options{..., ClassMerger: ref}`, returning the generated source. Reuse existing codegen temp-module test helpers.

- [ ] **Step 7: Run codegen tests**

Run: `go test ./internal/codegen -run 'TestValidateClassMerger|TestGeneratedClassUsesConfiguredMerger' -v`
Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add internal/codegen
git commit -m "feat(codegen): configurable class merger reference (direct, go/types-validated)"
```

---

### Task 3: gen — `WithClassMerger` option

Add the option route: `gen.WithClassMerger(fn)` reflects `fn` to a `ClassMergerRef`, stores it on `config`, merges with `option > config` precedence, and threads it into `codegen.Options`.

**Files:**
- Modify: `gen/options.go` (add `WithClassMerger`)
- Modify: `gen/main.go:35-53` (`config.classMerger` field)
- Modify: `gen/configfile.go` (`mergeConfig` — option wins)
- Modify: `gen/cache.go:59-153` (thread into `codegen.Options`)
- Test: `gen/options_test.go` (or a new `gen/classmerger_test.go`)

**Interfaces:**
- Consumes: `codegen.ClassMergerRef` (Task 2); `resolveFilterFunc`, `splitPkgFunc` (existing in `gen`).
- Produces: `func WithClassMerger(fn any) Option`; `config.classMerger *codegen.ClassMergerRef`.

- [ ] **Step 1: Write the failing test**

```go
// gen/classmerger_test.go
package gen

import "testing"

func TestWithClassMergerResolvesTopLevelFunc(t *testing.T) {
	var cfg config
	WithClassMerger(sampleMerge)(&cfg)
	if len(cfg.errs) != 0 {
		t.Fatalf("unexpected errs: %v", cfg.errs)
	}
	if cfg.classMerger == nil || cfg.classMerger.FuncName != "sampleMerge" {
		t.Fatalf("got %+v", cfg.classMerger)
	}
}

func TestWithClassMergerRejectsClosure(t *testing.T) {
	var cfg config
	WithClassMerger(func(t []string) string { return "" })(&cfg)
	if len(cfg.errs) == 0 {
		t.Fatalf("want error for closure, got none")
	}
}

func sampleMerge(tokens []string) string { return "" }
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./gen -run TestWithClassMerger -v`
Expected: FAIL — `WithClassMerger` / `config.classMerger` undefined.

- [ ] **Step 3: Add `config.classMerger` field (`gen/main.go`)**

In `type config struct`, add:
```go
	classMerger *codegen.ClassMergerRef // configured class merger (option or toml); nil = default
```

- [ ] **Step 4: Implement `WithClassMerger` (`gen/options.go`)**

```go
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
```

- [ ] **Step 5: Merge precedence in `mergeConfig` (`gen/configfile.go`)**

In `mergeConfig`, after the existing field merges, add (option wins over file):
```go
	merged.classMerger = base.classMerger
	if opts.classMerger != nil {
		merged.classMerger = opts.classMerger
	}
```

- [ ] **Step 6: Thread into `codegen.Options` (`gen/cache.go`)**

Add `classMerger *codegen.ClassMergerRef` to the `generateCached`/`generateModule` parameter lists (or the struct they read from) and set `genOpts.ClassMerger = classMerger` where `codegen.Options{...}` is built (≈ `gen/cache.go:86`). Update all callers to pass `cfg.classMerger`.

- [ ] **Step 7: Run tests**

Run: `go test ./gen -run TestWithClassMerger -v`
Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add gen
git commit -m "feat(gen): WithClassMerger option (reflects to ClassMergerRef, option>config)"
```

---

### Task 4: gen — `class_merger` in `gsx.toml`

Add the data route: a `class_merger = "<pkgPath>.<Func>"` key parsed by `splitPkgFunc` into `config.classMerger`.

**Files:**
- Modify: `gen/configfile.go` (`tomlConfig.ClassMerger`, `loadConfig` parse)
- Test: `gen/configfile_test.go` (or the e2e config test)

**Interfaces:**
- Consumes: `config.classMerger` (Task 3); `splitPkgFunc` (existing).
- Produces: TOML key `class_merger`.

- [ ] **Step 1: Write the failing test**

```go
// gen/configfile_test.go (add)
func TestLoadConfigClassMerger(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "gsx.toml"), `class_merger = "example.com/twcfg.Merge"`)
	cfg, err := loadConfig(filepath.Join(dir, "gsx.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.classMerger == nil || cfg.classMerger.PkgPath != "example.com/twcfg" || cfg.classMerger.FuncName != "Merge" {
		t.Fatalf("got %+v", cfg.classMerger)
	}
}

func TestLoadConfigClassMergerBadValue(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "gsx.toml"), `class_merger = "noDotHere"`)
	if _, err := loadConfig(filepath.Join(dir, "gsx.toml")); err == nil {
		t.Fatalf("want error for unqualified ref")
	}
}
```
(Use the existing test's file-writing helper; if none, add a small `writeFile`.)

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./gen -run TestLoadConfigClassMerger -v`
Expected: FAIL — unknown key `class_merger` (strict decode) / field absent.

- [ ] **Step 3: Add the TOML field and parse (`gen/configfile.go`)**

In `tomlConfig`, add:
```go
	ClassMerger    string            `toml:"class_merger"`
```
In `loadConfig`, after the filter-alias loop, add:
```go
	if tc.ClassMerger != "" {
		pkgPath, funcName, err := splitPkgFunc(tc.ClassMerger)
		if err != nil {
			return config{}, fmt.Errorf("%s: class_merger %q: %w", path, tc.ClassMerger, err)
		}
		cfg.classMerger = &codegen.ClassMergerRef{PkgPath: pkgPath, FuncName: funcName}
	}
```

- [ ] **Step 4: Run tests**

Run: `go test ./gen -run TestLoadConfigClassMerger -v`
Expected: PASS.

- [ ] **Step 5: Add an end-to-end gen test (option+toml → generated direct ref)**

Add a test that writes a `gsx.toml` with `class_merger`, a merger package (`func Merge(t []string) string { return "" }`), and a `.gsx`, runs the gen entry point, and asserts the generated `.x.go` contains `_gsxcm "<merger pkg>"` and `_gsxgw.Class(_gsxcm.Merge,`. Also assert a bad-signature merger produces the generate-time error from `validateClassMerger`. Model on `gen/configfile_e2e_test.go`.

> NOTE: the generate path must call `validateClassMerger` (Task 2) when `Options.ClassMerger != nil`, surfacing the error. If `GenerateDirs` does not yet invoke it, add the call at the start of generation (in `codegen` where `Options` is consumed) so both routes get validation. Add this wiring here and cover it with the bad-signature assertion.

- [ ] **Step 6: Run the e2e test**

Run: `go test ./gen -run 'TestLoadConfigClassMerger|TestGenClassMergerE2E' -v`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add gen
git commit -m "feat(gen): class_merger gsx.toml key + generate-time signature validation"
```

---

### Task 5: gen — fold the merger into the cache key

Ensure changing/adding/removing `class_merger` busts the incremental cache.

**Files:**
- Modify: `gen/cachekey.go:139` (`computeKey` signature + body)
- Modify: callers of `computeKey`
- Test: `gen/cachekey_test.go`

**Interfaces:**
- Consumes: `config.classMerger`.
- Produces: cache key sensitivity to the merger ref.

- [ ] **Step 1: Write the failing test**

```go
// gen/cachekey_test.go (add)
func TestComputeKeyVariesByClassMerger(t *testing.T) {
	base := func(ref *codegen.ClassMergerRef) string {
		k, err := computeKeyForTest(t, ref) // small helper invoking computeKey with a fixed graph
		if err != nil {
			t.Fatal(err)
		}
		return k
	}
	none := base(nil)
	a := base(&codegen.ClassMergerRef{PkgPath: "x/twcfg", FuncName: "Merge"})
	b := base(&codegen.ClassMergerRef{PkgPath: "x/twcfg", FuncName: "Other"})
	if none == a || a == b {
		t.Fatalf("cache key must vary by merger: none=%s a=%s b=%s", none, a, b)
	}
}
```
Provide `computeKeyForTest` wrapping `computeKey` with a minimal `graph`/args (follow existing cachekey test patterns).

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./gen -run TestComputeKeyVariesByClassMerger -v`
Expected: FAIL — merger not in key (a == none).

- [ ] **Step 3: Fold the merger into `computeKey`**

Add a `classMerger *codegen.ClassMergerRef` parameter to `computeKey` and include a stable marker in the hashed payload:
```go
	cm := "cm="
	if classMerger != nil {
		cm += classMerger.PkgPath + "." + classMerger.FuncName
	}
	// include cm in the same hash input as filterPkgs/aliases/codegenID
```
Thread `cfg.classMerger` from the caller(s) of `computeKey`.

- [ ] **Step 4: Run tests**

Run: `go test ./gen -run TestComputeKeyVariesByClassMerger -v`
Expected: PASS.

- [ ] **Step 5: Run the gen suite**

Run: `go test ./gen/...`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add gen
git commit -m "fix(gen): fold class_merger into the incremental cache key"
```

---

### Task 6: Corpus — per-case merger support + canonical cases

Extend the corpus harness to set a per-case merger and add txtar cases pinning the generated direct-reference output and render behavior, using a **case-local** merger package (repo stays dependency-free).

**Files:**
- Modify: `internal/corpus/codegen.go:64-70` (`codegenDirs` Options) and `internal/corpus/batch.go` (per-case Options)
- Modify: `internal/corpus/loader.go` (read a `class_merger` directive from the case)
- Create: `internal/corpus/testdata/cases/class/merger_static.txtar`
- Create: `internal/corpus/testdata/cases/class/merger_composable.txtar`
- Create: `internal/corpus/testdata/cases/class/merger_fallthrough.txtar`

**Interfaces:**
- Consumes: `codegen.Options.ClassMerger` (Task 2).
- Produces: a case mechanism — a `class_merger` line in the case selecting a case-local merger package.

- [ ] **Step 1: Decide and implement the per-case mechanism (lowest friction)**

Add support for a `-- gsx.toml --` section in a case archive; when present, the harness reads `class_merger` from it (reuse `loadConfig`) and sets `codegen.Options.ClassMerger` for that case. The merger package is a second package written by the case (multi-package cases already supported via `writeCaseSources` + `rewriteImportPath`). This keeps the case self-describing and exercises the real `gsx.toml` parse.

Wire it: in `batch.go` where each case's sources are written and `codegenDirs`/`GenerateDirs` is invoked, build per-case `codegen.Options` carrying `ClassMerger` when the case has a `gsx.toml` with `class_merger`.

- [ ] **Step 2: Write `merger_static.txtar`**

```
-- gsx.toml --
class_merger = "corpustest/cases/class/merger_static/mrg.Keep"
-- mrg/mrg.go --
package mrg

// Keep is a deterministic, NON-default merger: it joins tokens with "|" and
// keeps duplicates, so the golden visibly differs from DefaultClassMerge.
func Keep(tokens []string) string {
	out := ""
	for i, t := range tokens {
		if i > 0 {
			out += "|"
		}
		out += t
	}
	return out
}
-- input.gsx --
package views

component Card() { <section class="card">{children}</section> }
-- invoke --
Card(CardProps{Attrs: gsx.Attrs{"class": "hl"}})
-- diagnostics.golden --
-- render.golden --
```
(Leave `render.golden` empty; `-update` fills it. The merger uses `|`, so the rendered class becomes `card|hl`, proving the configured merger ran.)

- [ ] **Step 3: Write `merger_composable.txtar` and `merger_fallthrough.txtar`**

`merger_composable.txtar` — same `gsx.toml` + `mrg` package (adjust the import path to the case dir), with:
```
-- input.gsx --
package views

component C(on bool) { <div class={ "btn", "btn-on": on }>y</div> }
-- invoke --
C(CProps{On: true})
```
`merger_fallthrough.txtar` — exercises `{ attrs.Class() }` raw join + the outer merge:
```
-- input.gsx --
package views

component C() { <div class={ "btn" }>{ children }</div> }
-- invoke --
C(CProps{Attrs: gsx.Attrs{"class": "btn extra"}, Children: gsx.Raw("x")})
```
(Adjust each case's `class_merger` path to its own case dir.)

- [ ] **Step 4: Generate goldens**

Run: `go test ./internal/corpus -run TestCorpus -update`
Then: `go test ./internal/corpus -run TestCorpus`
Expected: PASS. The three new `generated.x.go.golden` show `_gsxcm "…/mrg"` + `_gsxgw.Class(_gsxcm.Keep,`; the `render.golden` show `|`-joined classes.

- [ ] **Step 5: Verify coverage manifest updated**

The `-update` run also rewrites `coverage.golden`. Confirm the suite passes without `-update` (a forgotten manifest bump fails the suite).

- [ ] **Step 6: Commit**

```bash
git add internal/corpus
git commit -m "test(corpus): per-case class_merger via case-local merger pkg (3 contexts)"
```

---

### Task 7: Docs — `class_merger` in the config guide

Document the knob, the signature contract, the wrapper idiom, and upgradeability.

**Files:**
- Modify: `docs/guide/config.md`

**Interfaces:** none (docs).

- [ ] **Step 1: Add a `class_merger` section to `docs/guide/config.md`**

Cover, with code blocks:
- `class_merger = "<pkgPath>.<Func>"` (data route) and `gen.WithClassMerger(fn)` (option route); precedence option > config.
- The contract: the named symbol must be an exported `func([]string) string` (func or var); other signatures are a generate-time error.
- The **wrapper idiom** for custom-configured tailwind-merge-go (`pkg/twmerge.CreateTwMerge`/`ExtendTailwindMerge` behind a one-line `func Merge([]string) string`), with the exact `twcfg` snippet from the spec.
- That the merger dependency + version live in the user's `go.mod` (upgrade = user-side bump + regenerate; no gsx release), and the runtime never imports it.

- [ ] **Step 2: Verify the guide builds locally (optional, only if editing VitePress)**

Per CLAUDE.md the docs job isn't in `make ci`; a prose-only `config.md` edit needs no build. Skip unless cross-referencing breaks links.

- [ ] **Step 3: Commit**

```bash
git add docs/guide/config.md
git commit -m "docs: document class_merger config knob + wrapper idiom"
```

---

### Task 8: Runnable example — `examples/tailwind-merge/`

A self-contained module wiring real `tailwind-merge-go` via the custom-config wrapper idiom, with committed generated output and a test asserting Tailwind merge.

**Files:**
- Create: `examples/tailwind-merge/go.mod`, `gsx.toml`, `twcfg/twcfg.go`, `views/card.gsx`, `views/card.x.go` (generated, committed), `example_test.go`, `README.md`
- Modify: `Makefile` / CI wiring as needed so `make ci` builds+tests it without breaking the txtar examples-drift check

**Interfaces:** consumes the shipped feature end-to-end.

- [ ] **Step 1: Scaffold the module**

`examples/tailwind-merge/go.mod`:
```
module github.com/gsxhq/gsx/examples/tailwind-merge

go 1.26.1

require (
	github.com/gsxhq/gsx v0.0.0
	github.com/jackielii/tailwind-merge-go v0.0.0-20260517071107-a44bd10e01e0
)

replace github.com/gsxhq/gsx => ../..
```
`gsx.toml`:
```toml
class_merger = "github.com/gsxhq/gsx/examples/tailwind-merge/twcfg.Merge"
```
`twcfg/twcfg.go`:
```go
// Package twcfg holds a custom-configured Tailwind class merger behind gsx's
// canonical func([]string) string seam.
package twcfg

import twmerge "github.com/jackielii/tailwind-merge-go/pkg/twmerge"

var merger = twmerge.CreateTwMerge(twmerge.GetDefaultConfig())

// Merge is what gsx.toml names. Exactly func([]string) string → direct reference.
func Merge(classes []string) string { return merger(classes) }
```
`views/card.gsx`:
```gsx
package views

component Card() { <section class="px-4 py-2">{children}</section> }
```

- [ ] **Step 2: Generate and inspect the output**

Run (from `examples/tailwind-merge/`): `go run github.com/gsxhq/gsx/cmd/gsx generate ./views`
Expected: `views/card.x.go` written, importing `_gsxcm "…/twcfg"` and emitting `_gsxgw.Class(_gsxcm.Merge, gsx.Class("px-4 py-2"), gsx.Class(_gsxp.Attrs.Class()))`.

- [ ] **Step 3: Write the test**

```go
// examples/tailwind-merge/example_test.go
package tailwindmerge_test

import (
	"context"
	"strings"
	"testing"

	gsx "github.com/gsxhq/gsx"
	"github.com/gsxhq/gsx/examples/tailwind-merge/views"
)

func TestTailwindMergeFallthrough(t *testing.T) {
	// caller passes conflicting px-8; tailwind merge must drop px-4.
	var sb strings.Builder
	node := views.Card(views.CardProps{Attrs: gsx.Attrs{"class": "px-8"}, Children: gsx.Raw("x")})
	if err := node.Render(context.Background(), &sb); err != nil {
		t.Fatal(err)
	}
	got := sb.String()
	if !strings.Contains(got, `class="py-2 px-8"`) {
		t.Fatalf("want merged class py-2 px-8 (px-4 dropped), got: %s", got)
	}
}
```

- [ ] **Step 4: Run the example test**

Run (from `examples/tailwind-merge/`): `go mod tidy && go test ./...`
Expected: PASS — `px-4` dropped, `class="py-2 px-8"`.

- [ ] **Step 5: Wire into CI**

Add the example module to whatever loop `make ci` uses for multi-module build/test (mirror how `playground/server` or other sub-modules are handled). Ensure the committed `views/card.x.go` is drift-checked (regenerate in CI and `git diff --exit-code`), and that the txtar `examples/*.txtar` drift check is NOT confused by this Go module (it lives under `examples/tailwind-merge/`, a directory, not a `.txtar`). Confirm `make ci` passes.

- [ ] **Step 6: Commit**

```bash
git add examples/tailwind-merge Makefile .github/workflows/ci.yml
git commit -m "example: tailwind-merge-go via custom-config wrapper + class_merger"
```

---

### Task 9: Final verification + ROADMAP

- [ ] **Step 1: Full CI**

Run: `make ci`
Expected: PASS (both modules build/vet/test, examples drift, gofmt + gsx fmt, the new example module).

- [ ] **Step 2: Grep for stragglers**

Run: `grep -rn "ClassMerger\b" --include=*.go . | grep -v ClassMergerRef`
Expected: no references to the removed global `gsx.ClassMerger` outside the new `ClassMergerRef` type / option.

- [ ] **Step 3: Update ROADMAP**

Tick the class-merger seam item in `docs/ROADMAP.md` (the runtime spec listed a real Tailwind merger as out of scope; record that the configurable seam shipped).

- [ ] **Step 4: Commit**

```bash
git add docs/ROADMAP.md
git commit -m "docs: ROADMAP — class merger config shipped"
```

---

## Self-Review Notes

- **Spec coverage:** option route (T3), config route (T4), drop global + thread + Attrs.Class raw (T1), direct-reference-only contract + go/types validation + reserved alias (T2), cache key fold (T5), corpus cases per context (T6), docs (T7), runnable custom-config example (T8). All spec sections map to a task.
- **Type consistency:** `ClassMergerRef{PkgPath, FuncName}` and `Options.ClassMerger` (Task 2) are used identically in Tasks 3–6; `mergeExpr` is the single string threaded through the five emit sites (Tasks 1–2); `DefaultClassMerge` is the exported default referenced by codegen.
- **Open items folded:** uniform threading (no `ClassWith` variant); corpus mechanism = a `gsx.toml` section in the case; example placement = own module under `examples/tailwind-merge/` with CI drift check.
