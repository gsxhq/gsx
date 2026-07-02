# CSP Nonce Injection Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `gsx.WithNonce(ctx, nonce)` runtime API + codegen that auto-injects `nonce="…"` into every emitted `<script>`/`<style>` open tag, author-written nonce always winning.

**Architecture:** A tiny stdlib-only runtime addition (`nonce.go`: context key + `Writer.Nonce`) plus a codegen pass in `internal/codegen/emit.go` that, for nonce-eligible elements (script/style without an author-written `nonce` attr), hoists one `gsx.Attrs` temp per spread attr and emits a guarded `_gsxgw.Nonce(ctx)` at the end of the open tag. Spec: `docs/superpowers/specs/2026-07-02-csp-nonce-injection-design.md`.

**Tech Stack:** Go 1.26.1, txtar corpus tests (`internal/corpus`), no new dependencies.

## Global Constraints

- **Work in the worktree**: all commands run from `/Users/jackieli/personal/gsxhq/gsx/.worktrees/csp-nonce` (branch `csp-nonce`).
- **Root package is standard-library only** — `nonce.go` may import only `context` (and use existing helpers).
- **Never hand-edit `.x.go` or golden files**; regenerate with `go test ./internal/corpus -run TestCorpus -update`, then verify WITHOUT `-update`.
- Corpus goldens for *existing* script/style cases WILL change (every script/style open tag gains a `Nonce(ctx)` call in generated goldens; `render.golden` stays byte-identical because the corpus renders with `context.Background()`). This is expected — regenerate, then eyeball the diff.
- The worktree has **pre-existing uncommitted changes** to `docs/ROADMAP.md`, `docs/guide/status.md`, `docs/guide/syntax/escaping.md` documenting the OLD (doc-only) decision. Do NOT commit them before Task 4, which rewrites them. Use explicit file paths in every `git add`.
- Inner loop: `make check`. Final gate: `make ci`.
- gofmt: generated-code emission uses tabs exactly as shown; run `gofmt -l .` before each commit.
- Commit messages: conventional style (`feat(codegen): …`), matching repo history.

---

### Task 1: Runtime nonce API (`gsx.WithNonce`, `NonceFromContext`, `Writer.Nonce`)

**Files:**
- Create: `nonce.go` (repo root package `gsx`)
- Test: `nonce_test.go` (repo root)
- Commit also: `docs/superpowers/specs/2026-07-02-csp-nonce-injection-design.md`, `docs/superpowers/plans/2026-07-02-csp-nonce-injection.md` (this plan)

**Interfaces:**
- Consumes: existing `Writer` internals — `gw.err`, `gw.writeStr(s string)`, `gw.AttrValue(s string)` (all in `writer.go`).
- Produces: `func WithNonce(ctx context.Context, nonce string) context.Context`, `func NonceFromContext(ctx context.Context) string`, `func (gw *Writer) Nonce(ctx context.Context)` — Task 2/3's generated code calls `_gsxgw.Nonce(ctx)`.

- [ ] **Step 1: Write the failing test**

Create `nonce_test.go`:

```go
package gsx

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestNonceContextRoundTrip(t *testing.T) {
	ctx := WithNonce(context.Background(), "abc123")
	if got := NonceFromContext(ctx); got != "abc123" {
		t.Fatalf("NonceFromContext = %q, want %q", got, "abc123")
	}
}

func TestNonceFromContextAbsent(t *testing.T) {
	if got := NonceFromContext(context.Background()); got != "" {
		t.Fatalf("NonceFromContext = %q, want empty", got)
	}
}

func TestWriterNonce(t *testing.T) {
	tests := []struct {
		name string
		ctx  context.Context
		want string
	}{
		{"no nonce in ctx", context.Background(), ""},
		{"empty nonce", WithNonce(context.Background(), ""), ""},
		{"plain", WithNonce(context.Background(), "abc123"), ` nonce="abc123"`},
		// entity forms must match htmlReplacer in escape.go — check
		// escape_test.go / escape.go for the exact entities and adjust if needed.
		{"hostile", WithNonce(context.Background(), `a"><script>`), ` nonce="a&#34;&gt;&lt;script&gt;"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var sb strings.Builder
			gw := W(&sb)
			gw.Nonce(tt.ctx)
			if err := gw.Err(); err != nil {
				t.Fatalf("Err() = %v", err)
			}
			if sb.String() != tt.want {
				t.Fatalf("output = %q, want %q", sb.String(), tt.want)
			}
		})
	}
}

type nonceFailWriter struct{}

func (nonceFailWriter) Write([]byte) (int, error) { return 0, errors.New("boom") }

func TestWriterNonceAfterError(t *testing.T) {
	gw := W(nonceFailWriter{})
	gw.S("x") // sets the retained first-write error
	first := gw.Err()
	if first == nil {
		t.Fatal("setup: expected a retained write error")
	}
	gw.Nonce(WithNonce(context.Background(), "abc123"))
	if gw.Err() != first {
		t.Fatalf("Nonce after error replaced the retained error: %v", gw.Err())
	}
}
```

Before finalizing the `hostile` expectation, check the exact entity replacements: `grep -n 'htmlReplacer' escape.go` (the set is `\x00 & < > " '`; `"` is `&#34;` in html/template's port). Fix the expected string if the entities differ.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test . -run 'TestNonce|TestWriterNonce' -v`
Expected: FAIL — `undefined: WithNonce`, `undefined: NonceFromContext`, `gw.Nonce undefined`.

- [ ] **Step 3: Write the implementation**

Create `nonce.go`:

```go
package gsx

import "context"

// nonceKey is the context key for the per-request CSP nonce (see WithNonce).
type nonceKey struct{}

// WithNonce returns a context carrying the per-request CSP nonce. Generated
// code adds nonce="<value>" to every <script> and <style> open tag rendered
// with the returned context; an author-written nonce attribute (or a spread
// bag carrying a "nonce" key) wins and suppresses the automatic one. gsx does
// not generate nonce values and does not build the Content-Security-Policy
// header — both remain the server's job.
func WithNonce(ctx context.Context, nonce string) context.Context {
	return context.WithValue(ctx, nonceKey{}, nonce)
}

// NonceFromContext returns the nonce stored by WithNonce, or "" when absent.
func NonceFromContext(ctx context.Context) string {
	nonce, _ := ctx.Value(nonceKey{}).(string)
	return nonce
}

// Nonce writes ` nonce="<value>"` (attribute-escaped) when ctx carries a
// non-empty nonce (WithNonce), and nothing otherwise. Generated code calls it
// at the end of every <script>/<style> open tag that has no author-written
// nonce attribute.
func (gw *Writer) Nonce(ctx context.Context) {
	if gw.err != nil {
		return
	}
	nonce := NonceFromContext(ctx)
	if nonce == "" {
		return
	}
	gw.writeStr(` nonce="`)
	gw.AttrValue(nonce)
	gw.writeStr(`"`)
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test . -run 'TestNonce|TestWriterNonce' -v` → PASS, then `go test .` → PASS (no regressions).

- [ ] **Step 5: Commit**

```bash
git add nonce.go nonce_test.go docs/superpowers/specs/2026-07-02-csp-nonce-injection-design.md docs/superpowers/plans/2026-07-02-csp-nonce-injection.md
git commit -m "feat: WithNonce context API and Writer.Nonce for CSP nonce threading"
```

---

### Task 2: Codegen injection — plain-element path (`genNode`), incl. inline spreads

**Files:**
- Modify: `internal/codegen/emit.go` (helpers near `rootAttrName` ~line 686; `genNode` element case ~line 763; `emitAttr` ~line 1330 and its call sites at ~465, 469, 472, 503, 765, 1381, 1388)
- Create: 13 corpus cases under `internal/corpus/testdata/cases/nonce/`
- Regenerated: existing goldens under `internal/corpus/testdata/cases/{script,style,…}` + `coverage.golden`

**Interfaces:**
- Consumes: `_gsxgw.Nonce(ctx)` from Task 1; existing `rootAttrName(a ast.Attr) (string, bool)`, `spreadAttrExpr`, `interpTemp *int` counter (temps named `_gsxv%d`).
- Produces (used verbatim by Task 3):
  - `func nonceEligibleTag(tag string) bool`
  - `func hasExplicitNonce(attrs []ast.Attr) bool`
  - `type nonceInjection struct { temps map[*ast.SpreadAttr]string; order []string; extra []string }`
  - `func newNonceInjection(b *bytes.Buffer, tag string, attrs []ast.Attr, interpTemp *int, skip ast.Attr) *nonceInjection` (nil ⇒ not eligible; `skip` excludes one attr from the spread walk — Task 3 passes the MANUAL bag spread, this task passes `nil`)
  - `func (ni *nonceInjection) tempFor(s *ast.SpreadAttr) (string, bool)` (nil-safe)
  - `func (ni *nonceInjection) emitGuard(b *bytes.Buffer)` (nil-safe no-op)
  - `emitAttr` gains a trailing param `nonce *nonceInjection`.

- [ ] **Step 1: Write the first failing corpus cases (basic injection)**

Create `internal/corpus/testdata/cases/nonce/script_basic.txtar`:

```
# CSP nonce auto-injection: a <script> rendered with gsx.WithNonce on the
# context gets nonce="…" appended after its authored attributes.
-- support.go --
package views

import (
	"context"
	"io"

	"github.com/gsxhq/gsx"
)

func withTestNonce(n gsx.Node) gsx.Node {
	return gsx.Func(func(ctx context.Context, w io.Writer) error {
		return n.Render(gsx.WithNonce(ctx, "r4nd0m"), w)
	})
}
-- input.gsx --
package views

component Page() { <script>init()</script> }
-- invoke --
withTestNonce(Page())
-- diagnostics.golden --
-- render.golden --
<script nonce="r4nd0m">init()</script>
-- generated.x.go.golden --
```

(The empty `generated.x.go.golden` section opts the case into pinning generated code; `-update` fills it.)

Create `internal/corpus/testdata/cases/nonce/style_basic.txtar` — same `support.go` section verbatim, plus:

```
-- input.gsx --
package views

component Page() { <style>.card{color:red}</style> }
-- invoke --
withTestNonce(Page())
-- diagnostics.golden --
-- render.golden --
<style nonce="r4nd0m">.card{color:red}</style>
```

Create `internal/corpus/testdata/cases/nonce/no_nonce_ctx.txtar` (no support.go, no wrapper — proves byte-identical output without a nonce):

```
# No nonce on the context: output is byte-identical to pre-feature (the
# injected runtime check writes nothing).
-- input.gsx --
package views

component Page() { <script>init()</script> }
-- invoke --
Page()
-- diagnostics.golden --
-- render.golden --
<script>init()</script>
```

Create `internal/corpus/testdata/cases/nonce/script_src.txtar` — same `support.go` as script_basic, plus:

```
-- input.gsx --
package views

component Page() { <script src="/app.js"></script> }
-- invoke --
withTestNonce(Page())
-- diagnostics.golden --
-- render.golden --
<script src="/app.js" nonce="r4nd0m"></script>
```

Create `internal/corpus/testdata/cases/nonce/script_json_island.txtar` (JSON data islands are still `<script>` tags and get the nonce; modeled on `datajson/island_value.txtar` — BYO props: the component takes the struct directly, so the invoke passes `Cfg{…}`, not a generated Props struct). Same `support.go` as script_basic, plus:

```
-- input.gsx --
package views

type Cfg struct {
	Note string
}

component Page(cfg Cfg) {
	<script type="application/json" id="cfg">@{ cfg }</script>
}
-- invoke --
withTestNonce(Page(Cfg{Note: "hi"}))
-- diagnostics.golden --
-- render.golden --
<script type="application/json" id="cfg" nonce="r4nd0m">{"Note":"hi"}</script>
```

Create `internal/corpus/testdata/cases/nonce/div_control.txtar` — same `support.go`, plus:

```
-- input.gsx --
package views

component Page() { <div>x</div> }
-- invoke --
withTestNonce(Page())
-- diagnostics.golden --
-- render.golden --
<div>x</div>
```

- [ ] **Step 2: Write the failing precedence cases**

Create `internal/corpus/testdata/cases/nonce/explicit_static_wins.txtar` — same `support.go`, plus:

```
-- input.gsx --
package views

component Page() { <script nonce="manual">init()</script> }
-- invoke --
withTestNonce(Page())
-- diagnostics.golden --
-- render.golden --
<script nonce="manual">init()</script>
```

Create `internal/corpus/testdata/cases/nonce/explicit_expr_wins.txtar` — same `support.go`, plus:

```
-- input.gsx --
package views

component Page(n string) { <script nonce={n}>init()</script> }
-- invoke --
withTestNonce(Page(PageProps{N: "manual"}))
-- diagnostics.golden --
-- render.golden --
<script nonce="manual">init()</script>
```

Create `internal/corpus/testdata/cases/nonce/cond_attr_nonce.txtar` — same `support.go`, plus:

```
-- input.gsx --
package views

component Page(hot bool) { <script { if hot { nonce="manual" } }>init()</script> }
-- invoke --
withTestNonce(Page(PageProps{Hot: false}))
-- diagnostics.golden --
-- render.golden --
<script>init()</script>
```

(A conditional nonce disables auto-injection on BOTH branches — the false branch gets no auto value either; that's the spec's "author took ownership" rule.)

- [ ] **Step 3: Write the failing spread cases**

Create `internal/corpus/testdata/cases/nonce/spread_without_nonce.txtar` — same `support.go`, plus:

```
-- input.gsx --
package views

import "github.com/gsxhq/gsx"

component Page(extra gsx.Attrs) { <script { extra... }>init()</script> }
-- invoke --
withTestNonce(Page(PageProps{Extra: gsx.Attrs{{Key: "id", Value: "s"}}}))
-- diagnostics.golden --
-- render.golden --
<script id="s" nonce="r4nd0m">init()</script>
-- generated.x.go.golden --
```

Create `internal/corpus/testdata/cases/nonce/spread_with_nonce.txtar` — same `support.go`, plus:

```
-- input.gsx --
package views

import "github.com/gsxhq/gsx"

component Page(extra gsx.Attrs) { <script { extra... }>init()</script> }
-- invoke --
withTestNonce(Page(PageProps{Extra: gsx.Attrs{{Key: "id", Value: "s"}, {Key: "nonce", Value: "mine"}}}))
-- diagnostics.golden --
-- render.golden --
<script id="s" nonce="mine">init()</script>
```

Create `internal/corpus/testdata/cases/nonce/spread_eval_once.txtar` (proves the hoisted temp evaluates the spread expression exactly once):

```
# The spread expression must be evaluated exactly once even though the nonce
# guard also inspects it — the hoisted temp is assigned once and reused.
-- support.go --
package views

import (
	"context"
	"fmt"
	"io"

	"github.com/gsxhq/gsx"
)

var bagCalls int

func countingBag() gsx.Attrs {
	bagCalls++
	return gsx.Attrs{{Key: "data-calls", Value: fmt.Sprint(bagCalls)}}
}

func withTestNonce(n gsx.Node) gsx.Node {
	return gsx.Func(func(ctx context.Context, w io.Writer) error {
		return n.Render(gsx.WithNonce(ctx, "r4nd0m"), w)
	})
}
-- input.gsx --
package views

component Page() { <script { countingBag()... }>init()</script> }
-- invoke --
withTestNonce(Page())
-- diagnostics.golden --
-- render.golden --
<script data-calls="1" nonce="r4nd0m">init()</script>
```

Create `internal/corpus/testdata/cases/nonce/spread_in_cond_attr_on.txtar` — same plain `support.go` as script_basic, plus:

```
-- input.gsx --
package views

import "github.com/gsxhq/gsx"

component Page(hot bool, extra gsx.Attrs) { <script { if hot { { extra... } } }>init()</script> }
-- invoke --
withTestNonce(Page(PageProps{Hot: true, Extra: gsx.Attrs{{Key: "id", Value: "s"}}}))
-- diagnostics.golden --
-- render.golden --
<script id="s" nonce="r4nd0m">init()</script>
-- generated.x.go.golden --
```

Create `internal/corpus/testdata/cases/nonce/spread_in_cond_attr_off.txtar` — same, but `Hot: false` in invoke and:

```
-- render.golden --
<script nonce="r4nd0m">init()</script>
```

(no `generated.x.go.golden` section for the `_off` variant.)

**Contingency:** if the corpus run reveals a *diagnostic* rejecting a spread inside an element cond-attr branch (the component path rejects it; the element path is believed to allow it — verify), convert the two `spread_in_cond_attr_*` cases into one diagnostics-pinning case and KEEP the `CondAttr` walk in `newNonceInjection` (the parser accepts the shape; the walk must stay total).

- [ ] **Step 4: Run the corpus to verify the new cases fail**

Run: `go test ./internal/corpus -run 'TestCorpus/nonce' -v 2>&1 | tail -30`
Expected: FAIL — render output missing `nonce="r4nd0m"` (and coverage manifest complaints are fine at this point).

- [ ] **Step 5: Implement the codegen helpers**

In `internal/codegen/emit.go`, directly under `rootAttrName` (~line 698), add:

```go
// nonceEligibleTag reports whether tag is one gsx auto-decorates with the
// context CSP nonce (script/style; HTML tag names are case-insensitive).
func nonceEligibleTag(tag string) bool {
	return strings.EqualFold(tag, "script") || strings.EqualFold(tag, "style")
}

// hasExplicitNonce reports whether attrs carries an author-written attribute
// named "nonce" (case-insensitive), looking through cond-attr branches. An
// explicit nonce ANYWHERE on the tag disables auto-injection for the whole
// element — the author has taken ownership, even on a branch where a
// conditional nonce is absent.
func hasExplicitNonce(attrs []ast.Attr) bool {
	for _, a := range attrs {
		switch t := a.(type) {
		case *ast.CondAttr:
			if hasExplicitNonce(t.Then) || hasExplicitNonce(t.Else) {
				return true
			}
		case *ast.ClassAttr:
			if strings.EqualFold(t.Name, "nonce") {
				return true
			}
		default:
			if name, ok := rootAttrName(a); ok && strings.EqualFold(name, "nonce") {
				return true
			}
		}
	}
	return false
}

// nonceInjection carries the state for auto-injecting the context CSP nonce
// into a <script>/<style> open tag: one hoisted gsx.Attrs temp per spread
// attr (at any depth, including cond-attr branches) so the post-attr guard
// can ask each spread whether it already carried a "nonce" key. A nil
// *nonceInjection means "not eligible" (not script/style, or the author
// wrote an explicit nonce) and every method is a nil-safe no-op.
type nonceInjection struct {
	temps map[*ast.SpreadAttr]string
	order []string // temp names in declaration order
	extra []string // extra guard bag exprs (MANUAL fallthrough bag)
}

// newNonceInjection decides eligibility and, for an eligible element, writes
// the hoisted `var _gsxvN gsx.Attrs` declarations to b (they must precede the
// attr emits: a spread inside an untaken cond branch leaves its temp nil, and
// a nil Attrs.Has is false, so the guard stays correct). skip excludes one
// attr from the spread walk — the MANUAL `{ attrs... }` bag spread, which is
// consumed by emitFallthroughAttrs and guarded via extra instead.
func newNonceInjection(b *bytes.Buffer, tag string, attrs []ast.Attr, interpTemp *int, skip ast.Attr) *nonceInjection {
	if !nonceEligibleTag(tag) || hasExplicitNonce(attrs) {
		return nil
	}
	ni := &nonceInjection{temps: map[*ast.SpreadAttr]string{}}
	var walk func([]ast.Attr)
	walk = func(as []ast.Attr) {
		for _, a := range as {
			if a == skip {
				continue
			}
			switch t := a.(type) {
			case *ast.SpreadAttr:
				tmp := fmt.Sprintf("_gsxv%d", *interpTemp)
				*interpTemp++
				ni.temps[t] = tmp
				ni.order = append(ni.order, tmp)
			case *ast.CondAttr:
				walk(t.Then)
				walk(t.Else)
			}
		}
	}
	walk(attrs)
	for _, tmp := range ni.order {
		fmt.Fprintf(b, "\t\tvar %s gsx.Attrs\n", tmp)
	}
	return ni
}

// tempFor returns the hoisted temp for spread s (nil-safe).
func (ni *nonceInjection) tempFor(s *ast.SpreadAttr) (string, bool) {
	if ni == nil {
		return "", false
	}
	tmp, ok := ni.temps[s]
	return tmp, ok
}

// emitGuard writes the nonce injection at the end of the open tag's attrs:
// unconditional when no spread/bag could have carried a nonce, otherwise
// guarded on every bag having no "nonce" key.
func (ni *nonceInjection) emitGuard(b *bytes.Buffer) {
	if ni == nil {
		return
	}
	guards := make([]string, 0, len(ni.extra)+len(ni.order))
	for _, e := range ni.extra {
		guards = append(guards, "!"+e+".Has(\"nonce\")")
	}
	for _, tmp := range ni.order {
		guards = append(guards, "!"+tmp+".Has(\"nonce\")")
	}
	if len(guards) == 0 {
		b.WriteString("\t\t_gsxgw.Nonce(ctx)\n")
		return
	}
	fmt.Fprintf(b, "\t\tif %s {\n\t\t\t_gsxgw.Nonce(ctx)\n\t\t}\n", strings.Join(guards, " && "))
}
```

- [ ] **Step 6: Thread `nonce *nonceInjection` through `emitAttr`**

Change the `emitAttr` signature (~line 1330) to append the param:

```go
func emitAttr(b *bytes.Buffer, a ast.Attr, resolved map[ast.Node]types.Type, table filterTable, imports map[string]bool, interpTemp *int, cls *attrclass.Classifier, bag *diag.Bag, mergeExpr string, nonce *nonceInjection) bool {
```

Replace the `*ast.SpreadAttr` case body (keep the existing comment):

```go
		spreadExpr, ok := spreadAttrExpr(t, table, imports, bag)
		if !ok {
			return false
		}
		if tmp, hoisted := nonce.tempFor(t); hoisted {
			// Nonce-eligible element: assign the hoisted temp once (single
			// evaluation) and spread from it; the post-attr guard reads it.
			fmt.Fprintf(b, "\t\t%s = %s\n", tmp, spreadExpr)
			fmt.Fprintf(b, "\t\t_gsxgw.Spread(ctx, %s)\n", tmp)
		} else {
			fmt.Fprintf(b, "\t\t_gsxgw.Spread(ctx, %s)\n", spreadExpr)
		}
```

In the `*ast.CondAttr` case, pass `nonce` down to both recursive `emitAttr` calls (~lines 1381, 1388). Update the remaining call sites to pass `nil` for now: ~lines 465, 469, 472, 503 (inside `emitFallthroughAttrs` — Task 3 rewires these) — and the `genNode` site (~765) which the next step wires for real.

- [ ] **Step 7: Wire the `genNode` element path**

In `genNode`'s `*ast.Element` case (~line 763), change:

```go
		emitS(b, "<"+t.Tag)
		ni := newNonceInjection(b, t.Tag, t.Attrs, interpTemp, nil)
		for _, a := range t.Attrs {
			if !emitAttr(b, a, resolved, table, imports, interpTemp, cls, bag, mergeExpr, ni) {
				return false
			}
		}
		ni.emitGuard(b)
		if t.Void {
			emitS(b, "/>")
			return true
		}
		emitS(b, ">")
```

(The guard sits before the `t.Void` check so a self-closed `<script src=… />` still gets the nonce.)

- [ ] **Step 8: Build and run the new cases**

Run: `go build ./... && go test ./internal/corpus -run 'TestCorpus/nonce' 2>&1 | tail -20`
Expected: render assertions for the new cases PASS (generated/coverage goldens still stale).

- [ ] **Step 9: Regenerate all goldens, then verify clean**

```bash
go test ./internal/corpus -run TestCorpus -update
go test ./internal/corpus -run TestCorpus
```

Expected: PASS. Then `git diff --stat internal/corpus` and **review**:
- `nonce/*.txtar` `generated.x.go.golden` sections show the shapes from the spec (`_gsxgw.Nonce(ctx)`, hoisted `var _gsxvN gsx.Attrs`, guard `if !_gsxvN.Has("nonce")`).
- Pre-existing `script/`, `style/` case generated goldens gain exactly one `_gsxgw.Nonce(ctx)` line per open tag (their `render.golden` files are UNCHANGED — if any `render.golden` outside `nonce/` changed, stop and investigate).
- `coverage.golden` gained the new case rows.

- [ ] **Step 10: Root-package + codegen unit tests still pass**

Run: `go test . ./internal/codegen ./internal/corpus`
Expected: PASS.

- [ ] **Step 11: Commit**

```bash
gofmt -l internal/codegen && git add internal/codegen/emit.go internal/corpus/testdata
git commit -m "feat(codegen): auto-inject CSP nonce on script/style open tags (plain-element path)"
```

---

### Task 3: Codegen injection — MANUAL `{ attrs... }` fallthrough path

**Files:**
- Modify: `internal/codegen/emit.go` — `emitManualSpreadElement` (~line 602), `emitFallthroughAttrs` (~line 428)
- Create: 3 corpus cases under `internal/corpus/testdata/cases/nonce/`

**Interfaces:**
- Consumes: `newNonceInjection(b, tag, attrs, interpTemp, skip)` / `tempFor` / `emitGuard` / `emitAttr(…, nonce)` from Task 2. The MANUAL bag is the local `attrs` (bound from `_gsxp.Attrs`), spread as `_gsxgw.Spread(ctx, attrs.Without(…))`.
- Produces: `emitFallthroughAttrs` gains a trailing `nonce *nonceInjection` param.

- [ ] **Step 1: Write the failing corpus cases**

Create `internal/corpus/testdata/cases/nonce/manual_spread.txtar` (same `support.go` as `script_basic`):

```
-- input.gsx --
package views

component Page() { <script { attrs... }>init()</script> }
-- invoke --
withTestNonce(Page(PageProps{Attrs: gsx.Attrs{{Key: "defer", Value: true}}}))
-- diagnostics.golden --
-- render.golden --
<script defer nonce="r4nd0m">init()</script>
-- generated.x.go.golden --
```

Create `internal/corpus/testdata/cases/nonce/manual_spread_with_nonce.txtar` (same `support.go`):

```
-- input.gsx --
package views

component Page() { <script { attrs... }>init()</script> }
-- invoke --
withTestNonce(Page(PageProps{Attrs: gsx.Attrs{{Key: "nonce", Value: "mine"}}}))
-- diagnostics.golden --
-- render.golden --
<script nonce="mine">init()</script>
```

Create `internal/corpus/testdata/cases/nonce/manual_spread_style.txtar` (same `support.go`):

```
-- input.gsx --
package views

component Page() { <style { attrs... }>.c{color:red}</style> }
-- invoke --
withTestNonce(Page(PageProps{Attrs: gsx.Attrs{{Key: "media", Value: "screen"}}}))
-- diagnostics.golden --
-- render.golden --
<style media="screen" nonce="r4nd0m">.c{color:red}</style>
```

Note: the MANUAL bag path merges class/style through `ClassMerged`/`StyleMerged` — if the actual render shows a merged `class=""`/empty-style artifact, that would be a pre-existing behavior difference; check a `fallthrough/manual_*.txtar` golden for the expected shape before assuming a bug.

- [ ] **Step 2: Run to verify they fail**

Run: `go test ./internal/corpus -run 'TestCorpus/nonce' 2>&1 | tail -20`
Expected: FAIL — `manual_*` renders missing the injected nonce.

- [ ] **Step 3: Implement**

`emitFallthroughAttrs` (~line 428): append param `nonce *nonceInjection`. Inside it:
- `emitScalar` passes `nonce` to both its `emitAttr` calls (~465, 469, 472).
- The `staticStyle` emit (~503) passes `nonce`.
- The inline (non-bag) `*ast.SpreadAttr` case (~570–575) mirrors Task 2's temp handling:

```go
			spreadExpr, ok := spreadAttrExpr(t, table, imports, bag)
			if !ok {
				return false
			}
			if tmp, hoisted := nonce.tempFor(t); hoisted {
				fmt.Fprintf(b, "\t\t%s = %s\n", tmp, spreadExpr)
				fmt.Fprintf(b, "\t\t_gsxgw.Spread(ctx, %s)\n", tmp)
			} else {
				fmt.Fprintf(b, "\t\t_gsxgw.Spread(ctx, %s)\n", spreadExpr)
			}
			continue
```

`emitManualSpreadElement` (~line 602):

```go
	emitS(b, "<"+el.Tag)
	ni := newNonceInjection(b, el.Tag, el.Attrs, interpTemp, el.Attrs[splitIdx])
	if ni != nil {
		ni.extra = []string{"attrs"}
	}
	if !emitFallthroughAttrs(b, el.Attrs, splitIdx, resolved, table, imports, interpTemp, cls, bag, mergeExpr, "attrs", ni) {
		return false
	}
	ni.emitGuard(b)
	if el.Void {
```

(`skip = el.Attrs[splitIdx]` keeps the bag spread out of the temp walk — it's consumed by `emitFallthroughAttrs`' own `Spread(ctx, attrs.Without(…))` and covered by the `extra` guard `!attrs.Has("nonce")` on the FULL bag. `Without` can only drop a "nonce" key via forcedNames, and a forced `nonce` scalar means `hasExplicitNonce` already made `ni` nil.)

The dead `emitRootElement` (~line 375, no callers) also calls `emitFallthroughAttrs` — update its call site to pass `nil` for the new param; do not otherwise touch it.

- [ ] **Step 4: Run, regenerate goldens, verify**

```bash
go build ./... && go test ./internal/corpus -run TestCorpus -update && go test ./internal/corpus -run TestCorpus
```

Expected: PASS. Review `git diff internal/corpus/testdata/cases/nonce` — `manual_spread.txtar`'s generated golden shows `if !attrs.Has("nonce") { _gsxgw.Nonce(ctx) }` after the bag spread. Confirm no `render.golden` outside `nonce/` changed.

- [ ] **Step 5: Full test sweep**

Run: `go test . ./internal/codegen ./internal/corpus ./parser`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/codegen/emit.go internal/corpus/testdata
git commit -m "feat(codegen): CSP nonce injection for manual { attrs... } fallthrough elements"
```

---

### Task 4: Version bump, examples regeneration, docs rewrite

**Files:**
- Modify: `internal/codegen/version.go` (bump `"21"` → `"22"`)
- Modify: `docs/guide/syntax/escaping.md` (REWRITE the existing uncommitted "CSP nonces" section), `docs/ROADMAP.md` (item 7), `docs/guide/status.md` (Known Gaps)
- Possibly regenerated: `docs/examples.json`, `playground/server/examples.json`, `docs/guide/syntax/_generated/**`, `examples/tailwind-merge/views/card.x.go`

**Interfaces:**
- Consumes: shipped behavior from Tasks 1–3.
- Produces: user-facing docs; a cache-busting codegen version.

- [ ] **Step 1: Bump the codegen version**

In `internal/codegen/version.go` change `const version = "21"` to `const version = "22"` (release-boundary cache bust for the changed emission).

- [ ] **Step 2: Regenerate example artifacts and check drift**

```bash
make examples
go run ./cmd/gsx -C examples/tailwind-merge generate ./views
git status --short
```

Rendered example output is nonce-free (examples render without `WithNonce`), but any artifact embedding *generated code* for script/style tags will change — commit whatever regenerated.

- [ ] **Step 3: Rewrite the escaping.md CSP section**

Replace the entire `### CSP nonces` section of `docs/guide/syntax/escaping.md` (added uncommitted by the previous session — it documents the superseded app-owned pattern) with:

```markdown
### CSP nonces

Store the per-request CSP nonce on the render context with `gsx.WithNonce`.
Every `<script>` and `<style>` tag gsx renders with that context automatically
carries the matching `nonce` attribute:

```go
func withCSP(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nonce := newNonce() // yours: e.g. 128-bit crypto/rand, base64
		w.Header().Set("Content-Security-Policy",
			"script-src 'nonce-"+nonce+"'; style-src 'nonce-"+nonce+"'")
		next.ServeHTTP(w, r.WithContext(gsx.WithNonce(r.Context(), nonce)))
	})
}
```

```gsx
component Page() {
	<script>
		init()
	</script>
}
```

renders as `<script nonce="…">…</script>` — no template changes needed.

The rules:

- Every native `<script>` and `<style>` tag qualifies: inline, external
  (`src=…`), and JSON data islands alike. Adding a nonce where CSP ignores it
  is harmless, and uniformity keeps the rule predictable.
- **An author-written `nonce` always wins.** Writing `nonce={expr}` — or a
  conditional `{ if c { nonce="…" } }` — anywhere on the tag turns
  auto-injection off for that tag entirely.
- **A spread bag carrying a `"nonce"` key wins too**: `<script { attrs... }>`
  is only auto-decorated when the bag has no `nonce` entry.
- The value is attribute-escaped like any quoted attribute; an absent or empty
  context nonce emits nothing (output is byte-identical to not using the
  feature).
- `gsx.NonceFromContext(ctx)` reads the nonce back when you need it by hand
  (e.g. for markup gsx does not own, like `gsx.RawHTML`).

gsx does not generate nonce values and does not build the
`Content-Security-Policy` header — both stay in your server, as in the
middleware above.
```

- [ ] **Step 4: Update ROADMAP + status**

`docs/ROADMAP.md` — replace the (uncommitted) item 7 text with:

```markdown
7. [x] **CSP nonce threading** for emitted `<script>`/`<style>` —
   `gsx.WithNonce(ctx, nonce)` stores the per-request nonce on the render
   context; generated code adds `nonce="…"` to every emitted `<script>`/
   `<style>` open tag (an author-written `nonce` attribute or a spread bag
   carrying a `"nonce"` key wins). No nonce generation, middleware, or CSP
   header engine — the header stays the server's job. See
   `2026-07-02-csp-nonce-injection-design.md`.
```

`docs/guide/status.md` — the Known Gaps bullet about CSP (uncommitted edit) is now wrong in both its committed and edited forms: delete the CSP bullet entirely (the feature shipped; escaping.md documents it). If that leaves "## Known Gaps" empty, keep the heading with a single line `- None currently tracked here; see the roadmap below.`

- [ ] **Step 5: Verify the docs build constraint**

The new prose contains no literal `{{ }}` (VitePress/Vue would choke) — confirm: `grep -n '{{' docs/guide/syntax/escaping.md` shows nothing new outside existing `::: v-pre` blocks.

- [ ] **Step 6: Run `make check`**

Run: `make check`
Expected: PASS (both modules build/vet/test, examples drift clean, formatting clean).

- [ ] **Step 7: Commit**

```bash
git add internal/codegen/version.go docs/ROADMAP.md docs/guide/status.md docs/guide/syntax/escaping.md docs/examples.json playground/server/examples.json docs/guide/syntax/_generated examples/tailwind-merge/views/card.x.go
git commit -m "docs: CSP nonce injection docs; bump codegen version"
```

(Drop paths from `git add` that didn't actually change.)

---

### Task 5: Independent adversarial review + full CI

**Files:** none created (review may add fix commits)

- [ ] **Step 1: Dispatch the independent adversarial reviewer**

Per repo convention: an independent reviewer who **builds throwaway probe programs** (in the scratchpad dir, not the repo), not just reads the diff. Probe ideas the reviewer must try:
- A real HTTP-style render: component with `<script>`, `<style>`, `<div>`; render with and without `WithNonce`; assert byte-exact outputs, no duplicate attributes.
- Hostile nonce value round-trip (`" onload=alert(1)` etc.) — assert escaping.
- A spread whose bag has key `"NONCE"` (uppercase) — document behavior (guard is exact-match `nonce`; HTML parses attr names case-insensitively; bag keys are trusted developer input per `Attrs` contract — reviewer confirms this is documented, not silently wrong).
- Nested components: parent passes ctx; child's script gets the nonce.
- `gsx generate` on a scratch module with a script-heavy `.gsx`; `go vet` the generated output; check `//line` directives didn't drift around the injected statements (LSP/diagnostics positions).
- Re-render the same Node twice with different nonces — both correct (no caching bug).

- [ ] **Step 2: Fix anything found; commit fixes individually**

- [ ] **Step 3: Run the authoritative gate**

Run: `make ci`
Expected: PASS (uncached, `-count=1`, both modules, drift + format checks).

- [ ] **Step 4: Update memory/state**

The branch is ready for merge review. Do not merge to main without the user's go-ahead (repo convention: finishing-a-development-branch skill).
