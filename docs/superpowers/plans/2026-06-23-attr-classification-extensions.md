# Attribute-Classification Extensions Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let users extend gsx's JS / URL / CSS attribute-classification sets via code-level `gen.Main` options (declarative rules + a predicate escape hatch), and persist the resolved config to the build cache so external tools can ground on the last-good build.

**Architecture:** Introduce `internal/attrclass.Classifier` — built-in matchers (the safety floor, ported verbatim from `attrjs.IsJSAttr` + `urlAttrs` + `style`) plus additive user rules (serializable) plus an optional predicate (compiled-in escape hatch). Thread a `*attrclass.Classifier` through both the parser (JS facet, to split `@{ }` holes) and codegen (all three facets, for context-aware escaping). Add `gen.WithJSAttrs/WithURLAttrs/WithCSSAttrs/WithAttrClassifier` options, fold the resolved rules into the codegen cache key, and persist a JSON manifest into `~/.cache/gsx` keyed by module path, also emitted by `gsx info --json`.

**Tech Stack:** Go, `go/token`, `go/types`; existing `internal/codegen`, `parser`, `gen` packages; `encoding/json`; `crypto/sha256`.

## Global Constraints

- **Unexported by default** — new types/fields/methods start lowercase unless they need to cross a package boundary or be JSON-serialized (per user's global Go rules).
- **No behavior change without opt-in** — a built-ins-only Classifier (`attrclass.Builtin()`) MUST reproduce today's `attrjs.IsJSAttr` + `urlAttrs` + `style` decisions byte-for-byte; the whole corpus stays green at every task.
- **Additive only** — user rules and the predicate may only *add* classification for names the built-ins don't claim; they can never downgrade a built-in. Built-ins are checked first.
- **No new dependencies** — stdlib only (`encoding/json`, `crypto/sha256`).
- **No working-tree pollution** — the manifest lives in the existing build cache dir (`os.UserCacheDir()/gsx`, env `GSXCACHE`), never in the user's repo.
- **Module path:** `github.com/gsxhq/gsx`.
- **Run all tests with:** `go test ./...` from the repo root `/Users/jackieli/personal/gsxhq/gsx`.

---

## File Structure

- **Create** `internal/attrclass/attrclass.go` — the `Classifier`, `Rule`, `Rules`, `Context`, `Builtin()`, `New()`, `Context()`, `HasPredicate()`, `Fingerprint()`. Absorbs `internal/attrjs`.
- **Create** `internal/attrclass/attrclass_test.go` — unit + parity tests.
- **Delete** `internal/attrjs/attrjs.go` + `internal/attrjs/attrjs_test.go` (migrated into attrclass) — at the end of Task 3, once no importer remains.
- **Modify** `internal/codegen/emit.go` — drop local `attrCtx`/`urlAttrs`/`attrContext`; use `attrclass.Context`; `generateFile` gains a `*attrclass.Classifier` param.
- **Modify** `internal/codegen/batch.go` + `internal/codegen/codegen.go` — `GeneratePackagesWithFilters` / `GeneratePackageWithFilters` gain a classifier param; pass it to `generateFile` and to the parser.
- **Modify** `parser/parser.go`, `parser/file.go`, `parser/attrs.go` — `parser` struct carries a `*attrclass.Classifier`; add `ParseFileWithClassifier`; `parseSingleAttr` consults it.
- **Modify** `gen/options.go` — add `WithJSAttrs/WithURLAttrs/WithCSSAttrs/WithAttrClassifier`.
- **Modify** `gen/main.go`, `gen/cache.go`, `gen/cachekey.go`, `gen/info.go` — thread the classifier; fold rules into the cache key; `gsx info --json`.
- **Create** `gen/manifest.go` + `gen/manifest_test.go` — `manifest` type, `saveManifest`, `loadManifest`, stable project key.

---

## Task 1: `internal/attrclass` package — the Classifier

**Files:**
- Create: `internal/attrclass/attrclass.go`
- Test: `internal/attrclass/attrclass_test.go`

**Interfaces:**
- Consumes: nothing (leaf package; stdlib `strings`, `crypto/sha256`, `encoding/json`, `fmt`).
- Produces:
  - `type Context int` with `CtxPlain, CtxJS, CtxURL, CtxCSS Context` (CtxPlain == 0).
  - `type Rule struct { Name string; Prefix string }` (both JSON-exported).
  - `type Rules struct { JS []Rule; URL []Rule; CSS []Rule }` (JSON-exported).
  - `func Builtin() *Classifier` — built-ins only.
  - `func New(user Rules, predicate func(name string) (Context, bool)) *Classifier`.
  - `func (c *Classifier) Context(name string) Context`.
  - `func (c *Classifier) HasPredicate() bool`.
  - `func (c *Classifier) Fingerprint() string` — stable hash of user rules + hasPredicate (for cache-keying).
  - `func (r Rule) Valid() error` — exactly one of Name/Prefix set.

- [ ] **Step 1: Write the failing test**

Create `internal/attrclass/attrclass_test.go`:

```go
package attrclass

import "testing"

func TestBuiltinParity(t *testing.T) {
	c := Builtin()
	cases := []struct {
		name string
		want Context
	}{
		// JS (ported from attrjs): on*, @*, hx-on*, x-on:, x-data/init/show/if/effect, : bind
		{"onclick", CtxJS}, {"onChange", CtxJS}, {"@click", CtxJS},
		{"hx-on:click", CtxJS}, {"hx-on", CtxJS}, {"x-on:click", CtxJS},
		{"x-data", CtxJS}, {"x-init", CtxJS}, {"x-show", CtxJS},
		{"x-if", CtxJS}, {"x-effect", CtxJS}, {":class", CtxJS},
		// NOT JS — the precise on[a-z] rule must not over-match
		{"on", CtxPlain}, {"on-thing", CtxPlain}, {":", CtxPlain},
		{"online", CtxJS}, // "on"+lowercase letter — matches today's IsJSAttr exactly
		// URL (ported from urlAttrs)
		{"href", CtxURL}, {"src", CtxURL}, {"HREF", CtxURL},
		{"hx-get", CtxURL}, {"xlink:href", CtxURL},
		// CSS
		{"style", CtxCSS}, {"STYLE", CtxCSS},
		// plain
		{"id", CtxPlain}, {"data-x", CtxPlain}, {"class", CtxPlain},
	}
	for _, tc := range cases {
		if got := c.Context(tc.name); got != tc.want {
			t.Errorf("Context(%q) = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestUserRulesAdditive(t *testing.T) {
	c := New(Rules{
		JS:  []Rule{{Prefix: "wire:"}, {Prefix: "v-on:"}},
		URL: []Rule{{Name: "data-href"}},
		CSS: []Rule{{Name: "data-style"}},
	}, nil)
	checks := map[string]Context{
		"wire:click": CtxJS, "v-on:click": CtxJS,
		"data-href": CtxURL, "data-style": CtxCSS,
		// built-ins still win and are unchanged
		"onclick": CtxJS, "href": CtxURL, "style": CtxCSS,
		// unrelated still plain
		"data-x": CtxPlain,
	}
	for name, want := range checks {
		if got := c.Context(name); got != want {
			t.Errorf("Context(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestPredicateIsFallbackOnly(t *testing.T) {
	// predicate would say onclick is URL, but built-ins claim it first → stays JS.
	c := New(Rules{}, func(name string) (Context, bool) {
		if name == "onclick" {
			return CtxURL, true
		}
		if name == "fancy-go" {
			return CtxJS, true
		}
		return CtxPlain, false
	})
	if got := c.Context("onclick"); got != CtxJS {
		t.Errorf("predicate must not downgrade built-in: Context(onclick) = %v, want CtxJS", got)
	}
	if got := c.Context("fancy-go"); got != CtxJS {
		t.Errorf("predicate fallback: Context(fancy-go) = %v, want CtxJS", got)
	}
	if !c.HasPredicate() {
		t.Error("HasPredicate() = false, want true")
	}
}

func TestRuleValid(t *testing.T) {
	if err := (Rule{Name: "x"}).Valid(); err != nil {
		t.Errorf("name-only rule should be valid: %v", err)
	}
	if err := (Rule{Prefix: "x:"}).Valid(); err != nil {
		t.Errorf("prefix-only rule should be valid: %v", err)
	}
	if (Rule{Name: "x", Prefix: "y"}).Valid() == nil {
		t.Error("both Name and Prefix set should be invalid")
	}
	if (Rule{}).Valid() == nil {
		t.Error("empty rule should be invalid")
	}
}

func TestFingerprintStable(t *testing.T) {
	a := New(Rules{JS: []Rule{{Prefix: "wire:"}}}, nil)
	b := New(Rules{JS: []Rule{{Prefix: "wire:"}}}, nil)
	if a.Fingerprint() != b.Fingerprint() {
		t.Error("same rules must produce same fingerprint")
	}
	c := New(Rules{JS: []Rule{{Prefix: "other:"}}}, nil)
	if a.Fingerprint() == c.Fingerprint() {
		t.Error("different rules must produce different fingerprint")
	}
	withPred := New(Rules{JS: []Rule{{Prefix: "wire:"}}}, func(string) (Context, bool) { return CtxPlain, false })
	if a.Fingerprint() == withPred.Fingerprint() {
		t.Error("presence of predicate must change fingerprint")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/attrclass/`
Expected: FAIL — package/types not defined (build error).

- [ ] **Step 3: Write the implementation**

Create `internal/attrclass/attrclass.go`:

```go
// Package attrclass classifies HTML attribute names into security/escaping
// contexts (JS, URL, CSS, plain). The built-in set is the safety floor; users
// extend it additively via declarative Rules and an optional predicate, wired
// through gen.Main. The same Classifier is consulted by the parser (JS facet,
// to split @{ } holes) and by codegen (all facets, for context-aware escaping).
package attrclass

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
)

// Context is the escaping context implied by an attribute name.
type Context int

const (
	CtxPlain Context = iota
	CtxJS
	CtxURL
	CtxCSS
)

// Rule matches an attribute name by exact Name (case-insensitive) OR by Prefix.
// Exactly one field is set; the other is empty (see Valid).
type Rule struct {
	Name   string `json:"name,omitempty"`
	Prefix string `json:"prefix,omitempty"`
}

// Valid reports whether exactly one of Name/Prefix is set.
func (r Rule) Valid() error {
	switch {
	case r.Name != "" && r.Prefix != "":
		return fmt.Errorf("attrclass.Rule: set only one of Name/Prefix, got both (%q, %q)", r.Name, r.Prefix)
	case r.Name == "" && r.Prefix == "":
		return fmt.Errorf("attrclass.Rule: set exactly one of Name/Prefix, got neither")
	default:
		return nil
	}
}

// matches reports whether the already-lowercased lname matches this rule.
func (r Rule) matches(lname string) bool {
	if r.Name != "" {
		return lname == strings.ToLower(r.Name)
	}
	if r.Prefix != "" {
		return strings.HasPrefix(lname, strings.ToLower(r.Prefix))
	}
	return false
}

// Rules groups user-supplied classification rules by context.
type Rules struct {
	JS  []Rule `json:"js,omitempty"`
	URL []Rule `json:"url,omitempty"`
	CSS []Rule `json:"css,omitempty"`
}

// Classifier resolves an attribute name to a Context. Built-ins are the safety
// floor and are checked first; user rules and the predicate are additive.
type Classifier struct {
	rules     Rules
	predicate func(name string) (Context, bool)
}

// Builtin returns a Classifier with only gsx's built-in classification — no user
// rules, no predicate. Its decisions are identical to the historical
// attrjs.IsJSAttr + urlAttrs + style logic.
func Builtin() *Classifier { return &Classifier{} }

// New layers user rules and an optional predicate over the built-ins. predicate
// may be nil.
func New(user Rules, predicate func(name string) (Context, bool)) *Classifier {
	return &Classifier{rules: user, predicate: predicate}
}

// Context classifies name. Priority (union semantics):
//  1. built-ins (safety floor)
//  2. user declarative rules (URL, then CSS, then JS — mirrors built-in order)
//  3. user predicate (only for names no rule matched; CtxPlain results ignored)
func (c *Classifier) Context(name string) Context {
	ln := strings.ToLower(name)

	// 1. Built-ins, in the historical attrContext order: URL, CSS, JS.
	if builtinURL[ln] {
		return CtxURL
	}
	if ln == "style" {
		return CtxCSS
	}
	if builtinJS(ln) {
		return CtxJS
	}

	if c == nil {
		return CtxPlain
	}

	// 2. User declarative rules.
	for _, r := range c.rules.URL {
		if r.matches(ln) {
			return CtxURL
		}
	}
	for _, r := range c.rules.CSS {
		if r.matches(ln) {
			return CtxCSS
		}
	}
	for _, r := range c.rules.JS {
		if r.matches(ln) {
			return CtxJS
		}
	}

	// 3. Predicate escape hatch (receives the original name, not lowercased).
	if c.predicate != nil {
		if ctx, ok := c.predicate(name); ok && ctx != CtxPlain {
			return ctx
		}
	}
	return CtxPlain
}

// HasPredicate reports whether a predicate escape hatch is registered. The
// manifest records this so offline tools can warn that predicate-classified
// attributes are not available without a live build.
func (c *Classifier) HasPredicate() bool { return c != nil && c.predicate != nil }

// Rules returns the user rules (built-ins excluded). Used to serialize the
// manifest delta; built-ins are compiled into every consumer.
func (c *Classifier) Rules() Rules {
	if c == nil {
		return Rules{}
	}
	return c.rules
}

// Fingerprint is a stable hash of the user rules plus whether a predicate is
// present. It feeds the codegen cache key so changing rules invalidates cached
// output. NOTE: predicate *bodies* are not hashed (closures aren't inspectable),
// matching the existing treatment of WithCSSMinifier/WithJSMinifier — document
// that changing a predicate's logic requires `gsx clean --cache`.
func (c *Classifier) Fingerprint() string {
	type fp struct {
		Rules        Rules `json:"rules"`
		HasPredicate bool  `json:"hasPredicate"`
	}
	b, _ := json.Marshal(fp{Rules: c.Rules(), HasPredicate: c.HasPredicate()})
	sum := sha256.Sum256(b)
	return fmt.Sprintf("%x", sum[:])
}

// builtinURL is the URL-context attribute set (ported verbatim from
// codegen.urlAttrs). Keys are lowercase.
var builtinURL = map[string]bool{
	"href": true, "src": true, "action": true, "formaction": true, "poster": true,
	"cite": true, "ping": true, "data": true, "background": true, "manifest": true,
	"xlink:href": true, "hx-get": true, "hx-post": true, "hx-put": true,
	"hx-delete": true, "hx-patch": true,
}

// builtinJS reports whether the lowercased attribute name n is a JS-context
// attribute. Ported verbatim from the historical attrjs.IsJSAttr (input is
// already lowercased by the caller).
func builtinJS(n string) bool {
	switch {
	case strings.HasPrefix(n, "@"): // Alpine @click shorthand for x-on:
		return true
	case strings.HasPrefix(n, "hx-on"): // HTMX hx-on:*
		return true
	case strings.HasPrefix(n, "on") && len(n) > 2 && n[2] >= 'a' && n[2] <= 'z': // onclick…
		return true
	case n == "x-data" || n == "x-init" || n == "x-show" || n == "x-if" || n == "x-effect":
		return true
	case strings.HasPrefix(n, "x-on:"): // Alpine x-on:click
		return true
	case strings.HasPrefix(n, ":") && n != ":": // Alpine :class / x-bind shorthand
		return true
	default:
		return false
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/attrclass/`
Expected: PASS (all tests).

- [ ] **Step 5: Commit**

```bash
git add internal/attrclass/
git commit -m "feat(attrclass): Classifier with built-in floor + additive user rules/predicate"
```

---

## Task 2: Wire Classifier into codegen escaping

**Files:**
- Modify: `internal/codegen/emit.go` (drop `attrCtx`/`ctx*`/`urlAttrs`/`attrContext`; use `attrclass`; `generateFile` gains a classifier param; 6 `attrContext(...)` call sites → `cls.Context(...)`)
- Modify: `internal/codegen/batch.go:33,243` (`GeneratePackagesWithFilters` gains a classifier param; pass to `generateFile`)
- Modify: `internal/codegen/codegen.go:~38,94` (`GeneratePackageWithFilters` gains a classifier param; pass to `generateFile`)
- Test: `internal/codegen/attrclass_wire_test.go` (new)

**Interfaces:**
- Consumes: `attrclass.Builtin()`, `attrclass.New`, `attrclass.Classifier.Context`, `attrclass.CtxJS/CtxURL/CtxCSS/CtxPlain` (Task 1).
- Produces:
  - `func generateFile(file *ast.File, resolved map[ast.Node]types.Type, table filterTable, structFields map[string]map[string]bool, fset *token.FileSet, cls *attrclass.Classifier, cssMin, jsMin func(string) (string, error)) ([]byte, error)`
  - `func GeneratePackagesWithFilters(moduleDir string, dirs []string, filterPkgs []string, cls *attrclass.Classifier, cssMin, jsMin func(string) (string, error)) (map[string]*PackageResult, error)`
  - `func GeneratePackageWithFilters(dir string, filterPkgs []string, cls *attrclass.Classifier, cssMin, jsMin func(string) (string, error)) (map[string][]byte, error)`
  - Note: a nil `cls` is treated as `attrclass.Builtin()` by `generateFile` (defensive).

- [ ] **Step 1: Write the failing test**

Create `internal/codegen/attrclass_wire_test.go`:

```go
package codegen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gsxhq/gsx/internal/attrclass"
)

// A custom JS rule must route a custom-framework attribute through JS-context
// emission (gw.JS*Attr), not plain attr escaping.
func TestCustomJSAttrRuleEmitsJSContext(t *testing.T) {
	dir := t.TempDir()
	src := `package views

func Widget() {
	<div wire:click="@{ action }"></div>
}
`
	if err := os.WriteFile(filepath.Join(dir, "views.gsx"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	cls := attrclass.New(attrclass.Rules{JS: []attrclass.Rule{{Prefix: "wire:"}}}, nil)
	out, err := GeneratePackageWithFilters(dir, nil, cls, nil, nil)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	var gen string
	for _, b := range out {
		gen += string(b)
	}
	if !strings.Contains(gen, "JSAttr") {
		t.Errorf("expected a gw.JS*Attr call for wire:click, generated:\n%s", gen)
	}
}

// Built-ins-only (nil-equivalent) classifier keeps wire:click as a plain static
// attribute — proving the rule, not a regression, caused the change above.
func TestBuiltinClassifierLeavesCustomAttrPlain(t *testing.T) {
	dir := t.TempDir()
	src := `package views

func Widget() {
	<div wire:click="literal"></div>
}
`
	if err := os.WriteFile(filepath.Join(dir, "views.gsx"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := GeneratePackageWithFilters(dir, nil, attrclass.Builtin(), nil, nil)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	var gen string
	for _, b := range out {
		gen += string(b)
	}
	if strings.Contains(gen, "JSAttr") {
		t.Errorf("built-in classifier must not JS-classify wire:click, generated:\n%s", gen)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/codegen/ -run TestCustomJSAttrRule -v`
Expected: FAIL — `GeneratePackageWithFilters` does not yet accept a classifier arg (compile error).

- [ ] **Step 3: Implement — replace the context enum/maps with attrclass**

In `internal/codegen/emit.go`:

1. Add `"github.com/gsxhq/gsx/internal/attrclass"` to imports; remove the `attrjs` import.
2. Delete the local enum + map + function:

```go
// DELETE these:
type attrCtx int
const ( ctxPlain attrCtx = iota; ctxURL; ctxJS; ctxCSS )
var urlAttrs = map[string]bool{ ... }
func attrContext(name string) attrCtx { ... }
```

3. Change `generateFile`'s signature to thread the classifier, defaulting nil to Builtin:

```go
func generateFile(file *ast.File, resolved map[ast.Node]types.Type, table filterTable, structFields map[string]map[string]bool, fset *token.FileSet, cls *attrclass.Classifier, cssMin, jsMin func(string) (string, error)) ([]byte, error) {
	if cls == nil {
		cls = attrclass.Builtin()
	}
	// ... existing body, but the classifier must reach emitExprAttr/emitStaticAttr
	// and the <style> check at emit.go:789. Thread `cls` to those emit helpers.
```

4. Replace the 6 classification call sites (current line numbers in parens):
   - `emit.go:789` `if attrContext(t.Name) == ctxCSS {` → `if cls.Context(t.Name) == attrclass.CtxCSS {`
   - `emit.go:1005` `switch attrContext(a.Name) {` → `switch cls.Context(a.Name) {` and `case ctxCSS:` → `case attrclass.CtxCSS:`
   - `emit.go:1037` `attrContext(a.Name) != ctxJS` → `cls.Context(a.Name) != attrclass.CtxJS`
   - `emit.go:1043` `attrContext(a.Name) == ctxJS` → `cls.Context(a.Name) == attrclass.CtxJS`
   - `emit.go:1049` `attrContext(a.Name) == ctxURL` → `cls.Context(a.Name) == attrclass.CtxURL`
   - Thread `cls` into the signatures of `emitExprAttr`, `emitStaticAttr` (the helper at/around `emit.go:789`), and any other helper that classifies — add a `cls *attrclass.Classifier` parameter and pass `cls` at each call site within `generateFile`.

> Implementation note: search `emit.go` for every `ctxJS`/`ctxURL`/`ctxCSS`/`ctxPlain` token and replace with the `attrclass.Ctx*` equivalent; the compiler will flag every helper that needs the new `cls` parameter — add it and pass `cls` through.

In `internal/codegen/batch.go`:

```go
func GeneratePackagesWithFilters(moduleDir string, dirs []string, filterPkgs []string, cls *attrclass.Classifier, cssMin, jsMin func(string) (string, error)) (map[string]*PackageResult, error) {
	if cls == nil {
		cls = attrclass.Builtin()
	}
	// ... at the generateFile call (was batch.go:243):
	gen, err := generateFile(file, resolved, table, pf, fset, cls, cssMin, jsMin)
```

Add `"github.com/gsxhq/gsx/internal/attrclass"` to batch.go imports.

In `internal/codegen/codegen.go`:

```go
func GeneratePackageWithFilters(dir string, filterPkgs []string, cls *attrclass.Classifier, cssMin, jsMin func(string) (string, error)) (map[string][]byte, error) {
	if cls == nil {
		cls = attrclass.Builtin()
	}
	// ... at the generateFile call (was codegen.go:94):
	gen, err := generateFile(file, resolved, table, propFields, fset, cls, cssMin, jsMin)
```

Add the attrclass import to codegen.go.

- [ ] **Step 4: Fix the now-broken `gen`-layer call sites (compile only)**

These callers in package `gen` call the two exported funcs; pass `attrclass.Builtin()` for now (Task 4 supplies the real classifier). Add `"github.com/gsxhq/gsx/internal/attrclass"` import to each file as needed:
- `gen/cache.go:88` (inside `generateCached`): `codegen.GeneratePackagesWithFilters(root, miss, filterPkgs, attrclass.Builtin(), cssMin, jsMin)`
- `gen/cache.go:162` (`mustGen`): same — add `attrclass.Builtin()` before `cssMin`.
- Any other `GeneratePackage(s)WithFilters` callers flagged by the compiler.

Run: `go build ./...`
Expected: builds clean.

- [ ] **Step 5: Run tests**

Run: `go test ./internal/codegen/ ./internal/corpus/ -run 'TestCustomJSAttrRule|TestBuiltinClassifier|.' `
(Or simply `go test ./...`.)
Expected: the two new tests PASS; the full corpus stays green (built-ins reproduce prior behavior).

- [ ] **Step 6: Commit**

```bash
git add internal/codegen/ gen/cache.go
git commit -m "feat(codegen): classify attribute contexts via attrclass.Classifier (built-in default)"
```

---

## Task 3: Wire Classifier into the parser

**Files:**
- Modify: `parser/parser.go` (add `classifier *attrclass.Classifier` field; `newParser` defaults to Builtin)
- Modify: `parser/file.go` (add `ParseFileWithClassifier`; `ParseFile` delegates with `attrclass.Builtin()`; pass classifier into `newParser`)
- Modify: `parser/attrs.go:142` (`attrjs.IsJSAttr(name)` → `p.classifier.Context(name) == attrclass.CtxJS`)
- Modify: `internal/codegen/batch.go:75` + `internal/codegen/codegen.go:60` (parse `.gsx` via `ParseFileWithClassifier(fset, m, src, 0, cls)`)
- Delete: `internal/attrjs/attrjs.go`, `internal/attrjs/attrjs_test.go`
- Test: `parser/attrclass_test.go` (new)

**Interfaces:**
- Consumes: `attrclass.Builtin()`, `attrclass.Classifier`, `attrclass.CtxJS`; codegen's `cls` from Task 2.
- Produces:
  - `func ParseFileWithClassifier(fset *token.FileSet, filename string, src any, mode Mode, cls *attrclass.Classifier) (*ast.File, error)`
  - `ParseFile(...)` unchanged signature; now delegates to `ParseFileWithClassifier(..., attrclass.Builtin())`.

- [ ] **Step 1: Write the failing test**

Create `parser/attrclass_test.go`:

```go
package parser

import (
	"go/token"
	"testing"

	"github.com/gsxhq/gsx/internal/attrclass"
	"github.com/gsxhq/gsx/parser/ast"
)

func parseAttrType(t *testing.T, src string, cls *attrclass.Classifier) ast.Attr {
	t.Helper()
	full := "package p\n\nfunc C() {\n\t" + src + "\n}\n"
	f, err := ParseFileWithClassifier(token.NewFileSet(), "c.gsx", []byte(full), 0, cls)
	if err != nil {
		t.Fatalf("parse %q: %v", src, err)
	}
	// Walk to the first element's first attribute.
	var found ast.Attr
	ast.Inspect(f, func(n ast.Node) bool {
		if el, ok := n.(*ast.Element); ok && len(el.Attrs) > 0 && found == nil {
			found = el.Attrs[0]
			return false
		}
		return true
	})
	if found == nil {
		t.Fatalf("no attribute found in %q", src)
	}
	return found
}

// With a custom JS rule, a holey custom-framework attribute parses as *ast.JSAttr
// (holes split), not *ast.StaticAttr.
func TestCustomJSRuleSplitsHoles(t *testing.T) {
	cls := attrclass.New(attrclass.Rules{JS: []attrclass.Rule{{Prefix: "wire:"}}}, nil)
	got := parseAttrType(t, `<div wire:click="@{ action }"></div>`, cls)
	if _, ok := got.(*ast.JSAttr); !ok {
		t.Fatalf("with rule: got %T, want *ast.JSAttr", got)
	}
}

// Built-ins only: the same attribute is a plain StaticAttr (holes NOT split).
func TestBuiltinLeavesCustomAttrStatic(t *testing.T) {
	got := parseAttrType(t, `<div wire:click="@{ action }"></div>`, attrclass.Builtin())
	if _, ok := got.(*ast.StaticAttr); !ok {
		t.Fatalf("built-in: got %T, want *ast.StaticAttr", got)
	}
}
```

> If `ast.Inspect`/`ast.Element`/`ast.Attr` field names differ, adapt the walk to the actual AST (check `parser/ast`); the assertion (`*ast.JSAttr` vs `*ast.StaticAttr`) is the contract — `parseJSAttrValue` already returns `*ast.JSAttr` for holey values and `*ast.StaticAttr` for hole-free ones (see `parser/attrs.go` doc comment).

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./parser/ -run TestCustomJSRule`
Expected: FAIL — `ParseFileWithClassifier` undefined (compile error).

- [ ] **Step 3: Implement parser threading**

In `parser/parser.go`, add the field and default:

```go
import "github.com/gsxhq/gsx/internal/attrclass"

type parser struct {
	file       *token.File
	src        string
	base       int
	i          int
	classifier *attrclass.Classifier
}

func newParser(file *token.File, src string) *parser {
	return &parser{file: file, src: src, base: 0, classifier: attrclass.Builtin()}
}
```

In `parser/file.go`, split the entry point:

```go
// ParseFile parses with gsx's built-in attribute classification.
func ParseFile(fset *token.FileSet, filename string, src any, mode Mode) (*ast.File, error) {
	return ParseFileWithClassifier(fset, filename, src, mode, attrclass.Builtin())
}

// ParseFileWithClassifier parses using cls to classify attribute names (which
// JS-context attributes split @{ } holes). A nil cls means built-ins only.
func ParseFileWithClassifier(fset *token.FileSet, filename string, src any, mode Mode, cls *attrclass.Classifier) (*ast.File, error) {
	if cls == nil {
		cls = attrclass.Builtin()
	}
	// ... existing ParseFile body unchanged, EXCEPT every place that constructs
	// the top-level parser via newParser(...) must set p.classifier = cls.
}
```

Implementation detail: in `file.go`, after the `newParser(...)` call that builds the body parser, assign `p.classifier = cls` (search `file.go` for `newParser(`; the markup body parser is the one whose attributes matter). Add the `attrclass` import to `file.go`.

In `parser/attrs.go:142`, replace:

```go
		if p.classifier.Context(name) == attrclass.CtxJS {
			return p.parseJSAttrValue(name, attrStartPos)
		}
```

Add `"github.com/gsxhq/gsx/internal/attrclass"` to attrs.go imports; remove the `attrjs` import.

In `internal/codegen/batch.go:75` and `internal/codegen/codegen.go:60`, change the parse call to pass the classifier already in scope (Task 2 added `cls` to both funcs):

```go
f, err := gsxparser.ParseFile(fset, m, src, 0)
// becomes:
f, err := gsxparser.ParseFileWithClassifier(fset, m, src, 0, cls)
```

- [ ] **Step 4: Delete the obsolete attrjs package**

```bash
rm -f internal/attrjs/attrjs.go internal/attrjs/attrjs_test.go
rmdir internal/attrjs 2>/dev/null || true
```

Run: `grep -rn "internal/attrjs" .`
Expected: no matches.

- [ ] **Step 5: Run tests**

Run: `go test ./...`
Expected: new parser tests PASS; full corpus green.

- [ ] **Step 6: Commit**

```bash
git add parser/ internal/codegen/batch.go internal/codegen/codegen.go internal/attrjs
git commit -m "feat(parser): classify JS-context attrs via attrclass; remove internal/attrjs"
```

---

## Task 4: `gen` options + threading + cache key

**Files:**
- Modify: `gen/main.go` (config fields `jsRules/urlRules/cssRules attrclass.Rules`, `attrPredicate`, `predicateLabel`; build `*attrclass.Classifier`; thread into `runGenerate`/`runInfo`)
- Modify: `gen/options.go` (new options + Rule validation onto `cfg.errs`)
- Modify: `gen/cache.go` (`generateCached`/`mustGen` accept and pass the classifier)
- Modify: `gen/cachekey.go` (`computeKey` folds in the classifier fingerprint)
- Test: `gen/options_test.go` (new), `gen/cachekey_test.go` (extend or new)

**Interfaces:**
- Consumes: `attrclass.New`, `attrclass.Rules`, `attrclass.Rule`, `attrclass.Classifier.Fingerprint` (Task 1); codegen funcs from Task 2.
- Produces:
  - `func WithJSAttrs(rules ...attrclass.Rule) Option`
  - `func WithURLAttrs(rules ...attrclass.Rule) Option`
  - `func WithCSSAttrs(rules ...attrclass.Rule) Option`
  - `func WithAttrClassifier(label string, fn func(name string) (attrclass.Context, bool)) Option`
  - `func (cfg *config) classifier() *attrclass.Classifier` — builds from the accumulated rules + predicate.
  - `generateCached(paths, filterPkgs []string, cls *attrclass.Classifier, useCache bool, cssMin, jsMin ...) (Result, error)`
  - `computeKey(dir, graph, modPath, goModHash, goSumHash, buildCtx string, filterPkgs []string, clsFingerprint string) (string, error)`

- [ ] **Step 1: Write the failing test**

Create `gen/options_test.go`:

```go
package gen

import (
	"testing"

	"github.com/gsxhq/gsx/internal/attrclass"
)

func TestWithAttrOptionsBuildClassifier(t *testing.T) {
	var cfg config
	WithJSAttrs(attrclass.Rule{Prefix: "wire:"})(&cfg)
	WithURLAttrs(attrclass.Rule{Name: "data-href"})(&cfg)
	WithCSSAttrs(attrclass.Rule{Name: "data-style"})(&cfg)
	if len(cfg.errs) != 0 {
		t.Fatalf("unexpected errs: %v", cfg.errs)
	}
	cls := cfg.classifier()
	if cls.Context("wire:click") != attrclass.CtxJS {
		t.Error("wire:click should be JS")
	}
	if cls.Context("data-href") != attrclass.CtxURL {
		t.Error("data-href should be URL")
	}
	if cls.Context("data-style") != attrclass.CtxCSS {
		t.Error("data-style should be CSS")
	}
}

func TestWithAttrsRejectsInvalidRule(t *testing.T) {
	var cfg config
	WithJSAttrs(attrclass.Rule{Name: "x", Prefix: "y"})(&cfg) // both set
	if len(cfg.errs) == 0 {
		t.Fatal("expected an error for a rule with both Name and Prefix set")
	}
}

func TestWithAttrClassifierSetsPredicate(t *testing.T) {
	var cfg config
	WithAttrClassifier("fancy", func(name string) (attrclass.Context, bool) {
		if name == "fancy-go" {
			return attrclass.CtxJS, true
		}
		return attrclass.CtxPlain, false
	})(&cfg)
	cls := cfg.classifier()
	if !cls.HasPredicate() {
		t.Fatal("predicate not registered")
	}
	if cls.Context("fancy-go") != attrclass.CtxJS {
		t.Error("predicate fallback not applied")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./gen/ -run TestWithAttr`
Expected: FAIL — options + `cfg.classifier()` undefined.

- [ ] **Step 3: Implement options + config**

In `gen/main.go`, extend `config` (near the existing `filterPkgs`/`cssMin`/`jsMin`/`errs` fields):

```go
import "github.com/gsxhq/gsx/internal/attrclass"

type config struct {
	filterPkgs []string
	cssMin     func(string) (string, error)
	jsMin      func(string) (string, error)
	jsRules    []attrclass.Rule
	urlRules   []attrclass.Rule
	cssRules   []attrclass.Rule
	attrPred   func(name string) (attrclass.Context, bool)
	predLabel  string
	errs       []error
}

// classifier builds the resolved Classifier from the accumulated options. A
// config with no attr options yields a built-ins-only Classifier.
func (cfg *config) classifier() *attrclass.Classifier {
	return attrclass.New(attrclass.Rules{
		JS:  cfg.jsRules,
		URL: cfg.urlRules,
		CSS: cfg.cssRules,
	}, cfg.attrPred)
}
```

In `gen/options.go`, add:

```go
import "github.com/gsxhq/gsx/internal/attrclass"

// WithJSAttrs registers additional JS-context attribute rules (e.g. Vue v-on:,
// Livewire wire:). Rules are additive over the built-ins; an invalid rule (both
// or neither of Name/Prefix set) fails the run with a clear message.
func WithJSAttrs(rules ...attrclass.Rule) Option {
	return func(cfg *config) {
		cfg.jsRules = appendValidRules(cfg, "WithJSAttrs", cfg.jsRules, rules)
	}
}

// WithURLAttrs registers additional URL-context attribute rules.
func WithURLAttrs(rules ...attrclass.Rule) Option {
	return func(cfg *config) {
		cfg.urlRules = appendValidRules(cfg, "WithURLAttrs", cfg.urlRules, rules)
	}
}

// WithCSSAttrs registers additional CSS-context attribute rules.
func WithCSSAttrs(rules ...attrclass.Rule) Option {
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
func WithAttrClassifier(label string, fn func(name string) (attrclass.Context, bool)) Option {
	return func(cfg *config) {
		cfg.attrPred = fn
		cfg.predLabel = label
	}
}

func appendValidRules(cfg *config, who string, dst, add []attrclass.Rule) []attrclass.Rule {
	for i, r := range add {
		if err := r.Valid(); err != nil {
			cfg.errs = append(cfg.errs, fmt.Errorf("%s: rule %d: %w", who, i, err))
			continue
		}
		dst = append(dst, r)
	}
	return dst
}
```

(Ensure `fmt` is imported in `options.go` — it already is.)

- [ ] **Step 4: Thread the classifier through the generate path**

In `gen/main.go` `runConfig`, build the classifier once and pass to the generate + info dispatch:

```go
	case "generate":
		return runGenerate(cmdArgs, stdout, stderr, quiet, verbose, false, cfg.filterPkgs, cfg.classifier(), cfg.cssMin, cfg.jsMin)
	...
	case "info":
		return runInfo(stdout, stderr, ".", cfg.filterPkgs, cfg.classifier())
```

Update `runGenerate` (gen/main.go:179) to accept `cls *attrclass.Classifier` and pass to `generateCached`. Update `generateCached` (gen/cache.go:14) and `mustGen` (gen/cache.go:162) signatures to take `cls`, passing it to `codegen.GeneratePackagesWithFilters(root, miss, filterPkgs, cls, cssMin, jsMin)` (replacing the `attrclass.Builtin()` placeholder from Task 2).

In `gen/cachekey.go`, fold the classifier fingerprint into the key. Change `computeKey` to accept `clsFingerprint string` and mix it into the hashed material alongside `filterPkgs`:

```go
func computeKey(dir string, graph map[string]pkgInfo, modPath, goModHash, goSumHash, buildCtx string, filterPkgs []string, clsFingerprint string) (string, error) {
	// ... wherever filterPkgs is written into the hash, also write clsFingerprint:
	//   io.WriteString(h, clsFingerprint)
}
```

In `generateCached`, compute `clsFingerprint := cls.Fingerprint()` once and pass it at the `computeKey(...)` call site.

- [ ] **Step 5: Run tests + full build**

Run: `go build ./... && go test ./...`
Expected: option tests PASS; corpus green; a changed JS rule produces a different cache key (covered by a `gen/cachekey_test.go` case — add one asserting `computeKey` differs when `clsFingerprint` differs, using the existing cachekey test fixtures as a template).

- [ ] **Step 6: Commit**

```bash
git add gen/
git commit -m "feat(gen): WithJSAttrs/WithURLAttrs/WithCSSAttrs/WithAttrClassifier + cache-key fold-in"
```

---

## Task 5: Resolved-config manifest + `gsx info --json`

**Files:**
- Create: `gen/manifest.go` (`manifest` type, `saveManifest`, `loadManifest`, `manifestPath`)
- Modify: `gen/cache.go` (`generateCached` writes the manifest on success when caching is enabled)
- Modify: `gen/info.go` (`runInfo` gains a `--json` mode emitting the manifest form; carries `cls`)
- Modify: `gen/main.go` (`info` already parses subcommand args — wire a `--json` flag through `runInfo`)
- Test: `gen/manifest_test.go` (new)

**Interfaces:**
- Consumes: `attrclass.Rules`, `attrclass.Classifier.Rules/HasPredicate` (Task 1); `codegen.ResolveFilters`/`FilterInfo` (existing); `cacheDir()`/`writeSentinel()` (existing in `gen/cachestore.go`).
- Produces:
  - `type manifest struct { SchemaVersion int; Module string; UserRules attrclass.Rules; HasPredicate bool; PredicateLabel string; Filters []manifestFilter }` (JSON-tagged).
  - `func saveManifest(cacheDir, modPath string, m manifest) error`
  - `func loadManifest(cacheDir, modPath string) (manifest, bool)`
  - `func manifestPath(cacheDir, modPath string) string` — stable key: `manifest/<sha256(modPath)>.json`.

- [ ] **Step 1: Write the failing test**

Create `gen/manifest_test.go`:

```go
package gen

import (
	"testing"

	"github.com/gsxhq/gsx/internal/attrclass"
)

func TestManifestRoundTrip(t *testing.T) {
	dir := t.TempDir()
	const modPath = "github.com/example/app"
	want := manifest{
		SchemaVersion:  1,
		Module:         modPath,
		UserRules:      attrclass.Rules{JS: []attrclass.Rule{{Prefix: "wire:"}}},
		HasPredicate:   true,
		PredicateLabel: "fancy",
	}
	if err := saveManifest(dir, modPath, want); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, ok := loadManifest(dir, modPath)
	if !ok {
		t.Fatal("loadManifest: not found after save")
	}
	if got.Module != want.Module || got.HasPredicate != want.HasPredicate || got.PredicateLabel != want.PredicateLabel {
		t.Errorf("round-trip mismatch: got %+v want %+v", got, want)
	}
	if len(got.UserRules.JS) != 1 || got.UserRules.JS[0].Prefix != "wire:" {
		t.Errorf("rules lost in round-trip: %+v", got.UserRules)
	}
}

func TestManifestStableKey(t *testing.T) {
	dir := t.TempDir()
	const modPath = "github.com/example/app"
	// A second tool computes the same path from only the module path.
	if manifestPath(dir, modPath) != manifestPath(dir, modPath) {
		t.Fatal("manifestPath not stable for same module path")
	}
	if _, ok := loadManifest(dir, modPath); ok {
		t.Fatal("expected miss before any save")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./gen/ -run TestManifest`
Expected: FAIL — `manifest`/`saveManifest`/`loadManifest`/`manifestPath` undefined.

- [ ] **Step 3: Implement the manifest**

Create `gen/manifest.go`:

```go
package gen

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/gsxhq/gsx/internal/attrclass"
)

// manifestSchemaVersion is bumped on incompatible manifest layout changes so a
// reader can reject a manifest it does not understand.
const manifestSchemaVersion = 1

// manifest is the resolved, build-independent projection of a project's gsx
// configuration. It is the same data `gsx info` prints; persisted as JSON into
// the build cache so external tools can ground on the last successful build.
// It is a derived cache, never a hand-edited config file.
type manifest struct {
	SchemaVersion  int              `json:"schemaVersion"`
	Module         string           `json:"module"`
	UserRules      attrclass.Rules  `json:"userRules"`
	HasPredicate   bool             `json:"hasPredicate"`
	PredicateLabel string           `json:"predicateLabel,omitempty"`
	Filters        []manifestFilter `json:"filters,omitempty"`
}

type manifestFilter struct {
	Name string `json:"name"`
	Pkg  string `json:"pkg"`
	Func string `json:"func"`
}

// manifestPath returns the stable cache path for modPath's manifest. The key is
// derived from the module path alone so a tool that knows the module can find it
// without any content hash.
func manifestPath(cacheDir, modPath string) string {
	sum := sha256.Sum256([]byte(modPath))
	return filepath.Join(cacheDir, "manifest", fmt.Sprintf("%x.json", sum[:]))
}

func saveManifest(cacheDir, modPath string, m manifest) error {
	p := manifestPath(cacheDir, modPath)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	writeSentinel(cacheDir) // tag the cache root (idempotent, best-effort)
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(p), "tmp-")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), p) // atomic
}

func loadManifest(cacheDir, modPath string) (manifest, bool) {
	data, err := os.ReadFile(manifestPath(cacheDir, modPath))
	if err != nil {
		return manifest{}, false
	}
	var m manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return manifest{}, false
	}
	if m.SchemaVersion != manifestSchemaVersion {
		return manifest{}, false
	}
	return m, true
}

// buildManifest assembles a manifest from the resolved classifier and filters.
func buildManifest(modPath string, cls *attrclass.Classifier, predLabel string, filters []manifestFilter) manifest {
	return manifest{
		SchemaVersion:  manifestSchemaVersion,
		Module:         modPath,
		UserRules:      cls.Rules(),
		HasPredicate:   cls.HasPredicate(),
		PredicateLabel: predLabel,
		Filters:        filters,
	}
}
```

- [ ] **Step 4: Write the manifest on a successful generate**

In `gen/cache.go` `generateCached`, after a successful run when `enabled` (caching on) and `modPath != ""`, persist the manifest. Add near the end (before the final `return res, nil`), guarded so manifest write never fails the build (best-effort, mirroring `storePut`):

```go
	if enabled && modPath != "" && len(res.Errs) == 0 {
		filters, _ := codegen.ResolveFilters(root, filterPkgs) // best-effort
		mf := make([]manifestFilter, 0, len(filters))
		for _, fi := range filters {
			mf = append(mf, manifestFilter{Name: fi.Name, Pkg: fi.Pkg, Func: fi.Func})
		}
		_ = saveManifest(cdir, modPath, buildManifest(modPath, cls, predLabel, mf))
	}
```

`generateCached` must receive `predLabel string` (thread from `cfg.predLabel` through `runGenerate`), or pass the whole label via the classifier — simplest: add a `predLabel string` param to `runGenerate`/`generateCached` alongside `cls`. (FilterInfo fields `Name`/`Pkg`/`Func` are confirmed in `gen/info.go`.)

- [ ] **Step 5: Add `gsx info --json`**

In `gen/info.go`, change `runInfo` to accept the classifier + a json flag and emit the manifest form when `--json` is set:

```go
func runInfo(stdout, stderr io.Writer, dir string, filterPkgs []string, cls *attrclass.Classifier, asJSON bool) int {
	infos, err := codegen.ResolveFilters(dir, filterPkgs)
	if err != nil {
		fmt.Fprintf(stderr, "gsx: %v\n", err)
		return 1
	}
	if asJSON {
		modPath, _ := moduleRootModPath(dir) // resolve module path for the manifest Module field; "" if unknown
		mf := make([]manifestFilter, 0, len(infos))
		for _, fi := range infos {
			mf = append(mf, manifestFilter{Name: fi.Name, Pkg: fi.Pkg, Func: fi.Func})
		}
		data, _ := json.MarshalIndent(buildManifest(modPath, cls, "", mf), "", "  ")
		fmt.Fprintln(stdout, string(data))
		return 0
	}
	// ... existing human-readable output, plus a new "Attribute rules" section
	// printing cls.Rules() (JS/URL/CSS) and a "predicate: <label>" line when
	// cls.HasPredicate().
}
```

Wire the `--json` flag in `gen/main.go`'s `info` dispatch (parse `cmdArgs` with a small `flag.FlagSet`, then call `runInfo(stdout, stderr, ".", cfg.filterPkgs, cfg.classifier(), jsonFlag)`). Use the module-path helper already used by the cache layer (e.g. `moduleRoot`) to fill `Module`; if no such single-return helper exists, reuse `moduleRoot(dir)` and pass its modPath. Add `encoding/json` + `attrclass` imports to `info.go`.

- [ ] **Step 6: Run tests + manual check**

Run: `go test ./...`
Expected: manifest tests PASS; corpus green.

Manual smoke (optional): in an example dir, `go run ./cmd/gsx info --json` prints a JSON object with `schemaVersion`, `module`, `userRules`, `hasPredicate`, `filters`.

- [ ] **Step 7: Commit**

```bash
git add gen/
git commit -m "feat(gen): persist resolved-config manifest to build cache + gsx info --json"
```

---

## Task 6: Docs — ROADMAP + extension guide

**Files:**
- Modify: `docs/ROADMAP.md` (mark attribute-classification extensions done; note the manifest + `info --json`)
- Modify: the user-facing extension docs alongside the existing `gen.WithFilters` documentation (find with `grep -rln "WithFilters" docs/`)

**Interfaces:** none (docs only).

- [ ] **Step 1: Update ROADMAP**

In `docs/ROADMAP.md`, update the Codegen + CLI rows to record: custom JS/URL/CSS attribute classification via `gen.WithJSAttrs/WithURLAttrs/WithCSSAttrs` + `WithAttrClassifier` escape hatch; resolved-config manifest persisted to `~/.cache/gsx`; `gsx info --json`. Reference this plan + the spec `2026-06-23-attr-classification-extensions-design.md`.

- [ ] **Step 2: Document the options in the extension guide**

Add a section next to the `WithFilters` docs covering: declarative rules (recommended), the predicate escape hatch (with the "won't survive a broken build" caveat), the project-binary-as-toolserver model, and that the manifest is a derived cache (run `gsx clean --cache` after changing a predicate's logic, since predicate bodies aren't cache-keyed).

- [ ] **Step 3: Commit**

```bash
git add docs/
git commit -m "docs: attribute-classification extensions (options, manifest, info --json)"
```

---

## Self-Review

**Spec coverage:**
- §3 Classifier (rules + predicate, additive, priority) → Task 1 ✓
- §4 Registration API (`WithJSAttrs/WithURLAttrs/WithCSSAttrs/WithAttrClassifier`) → Task 4 ✓
- §5 predicate escape hatch (additive, codegen-correct, `hasPredicate` marker) → Tasks 1, 4, 5 ✓
- §6 Threading into parser + codegen → Tasks 2, 3 ✓
- §7 Tool delivery (project binary as toolserver) → out of scope for code; `gsx info --json` seam delivered in Task 5; LSP/vet subcommands explicitly deferred (spec §10) ✓
- §8 Degradation tiers → Tier 1/2 are runtime/process properties (no code here); Tier 3 fallback artifact (manifest) → Task 5; Tier 4 built-in stock = `attrclass.Builtin()` → Task 1 ✓
- §9 Manifest (content, location in build cache, stable key, lifecycle) → Task 5 ✓
- §10 Out of scope (raw-text tags, replace mode, LSP impl, hand-edited config) → honored; none implemented ✓
- §11 Testing strategy (attrclass unit + parity, corpus, manifest round-trip + stable key + hasPredicate, threading) → Tasks 1–5 tests ✓
- Rule "exactly one of Name/Prefix" → `Rule.Valid()` (Task 1) + option validation (Task 4) ✓

**Placeholder scan:** No "TBD"/"add error handling"/"similar to Task N" — every code step shows code. The two "search and replace each `ctx*` token" / "set `p.classifier = cls` at the newParser site" notes are mechanical-completion guidance backed by exact before/after snippets, not deferred design.

**Type consistency:** `*attrclass.Classifier` threaded uniformly; `attrclass.Ctx*` constants used everywhere (local `ctx*` enum removed in Task 2); `generateFile`/`GeneratePackage(s)WithFilters` signatures consistent between the task that adds the param (Task 2) and the tasks that pass it (Tasks 3, 4); `manifest`/`manifestFilter`/`buildManifest`/`saveManifest`/`loadManifest`/`manifestPath` names consistent across Task 5 and its test.

**Note for the implementer:** Task 2's reliance on exact `emit.go` line numbers — the compiler is the source of truth. After removing `attrContext`/`ctx*`/`urlAttrs`, fix every build error by adding the `cls *attrclass.Classifier` parameter to the flagged helper and passing `cls` at its call site. The corpus suite is the regression oracle: it must stay green with built-ins-only at the end of every task.
