# gsx Token Migration `${ }` → `@{ }` (Slice A) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Migrate the raw-text-block interpolation delimiter from `${ }` to `@{ }` in the shipped slice-1 `<style>` code, so the token is free of the JS template-literal `${}` collision before `<script>` interpolation lands.

**Architecture:** A faithful rename — no behavior change beyond the trigger byte. The parser's `<style>` interpolation trigger changes from `$`-then-`{` to `@`-then-`{`; the printer emits `@{ … }` instead of `${ … }`; all tests/examples/corpus inputs/doc-comments follow; corpus goldens are regenerated. After migration, `${ … }` inside `<style>` is plain literal CSS text (no longer a trigger) and `@{ … }` is the interpolation.

**Tech Stack:** Go. Files: `parser/markup.go`, `internal/printer/printer.go`, their tests, `internal/wsnorm`/`internal/codegen`/`internal/cssmin`/`gen` (doc-comments + one codegen test), `examples/02_text_escaping.gsx`, `internal/corpus/testdata/cases/**`.

## Global Constraints

- **Faithful rename, behavior-preserving.** The `@{` trigger detection MUST mirror the current `${` logic exactly: fire only when `@` is immediately followed by `{`, advance past `@`, and reuse `parseInterp()` (cursor at `{`). A bare `@` (not followed by `{`) stays literal, exactly as a bare `$` did.
- **`<style>` only.** `<script>` and other raw-text tags are untouched (still single verbatim `Text`).
- **`SafeCSS → RawCSS` is already on `main`** — do NOT rename types here; only the delimiter changes.
- This is ONE atomic task: the parser trigger and the printer emit are coupled by the corpus round-trip property test (`render(fmt(S)) ≡ render(S)`, `fmt(fmt(S)) == fmt(S)`), so they must change together and the suite is only green at the end.
- After the task: `go build ./...` and `go test ./...` (incl. the printer round-trip property test and the full corpus) pass before committing.

---

### Task 1: Migrate the `<style>` interpolation delimiter `${ }` → `@{ }`

**Files:**
- Modify: `parser/markup.go` (the trigger byte + 2 doc-comments)
- Modify: `internal/printer/printer.go` (`styleChildren` emit + doc-comment)
- Modify: `parser/markup_test.go`, `internal/printer/printer_test.go`, `internal/printer/fuzz_test.go`, `internal/wsnorm/wsnorm_test.go`, `internal/codegen/rawcss_test.go` (test strings `${` → `@{`)
- Modify: `internal/codegen/emit.go`, `internal/cssmin/cssmin.go`, `internal/cssmin/file.go`, `gen/options.go` (doc-comments referencing `${ }`)
- Modify: `examples/02_text_escaping.gsx` (`${` → `@{`)
- Modify (regenerate): `internal/corpus/testdata/cases/style/{block_interpolation,block_stringer,block_bytes,block_tuple_error,block_try_rejected,block_pipe_rejected}.txtar`, `internal/corpus/testdata/cases/codegen-shape/style_interp.txtar`

**Interfaces:**
- Consumes: existing `(*parser).parseInterp()` (cursor at `{`), `(*printer).styleChildren`, the corpus harness `-update`.
- Produces: `@{ }` as the `<style>` interpolation delimiter; `${ }` becomes literal CSS text.

- [ ] **Step 1: Change the parser trigger** in `parser/markup.go`. Replace the trigger block (currently `if interpolate && p.peek() == '$' && … s[p.i+1] == '{'`):

```go
		// Interpolation? (<style> only; trigger is exactly `@{`.)
		if interpolate && p.peek() == '@' && p.i+1 < len(p.src) && p.src[p.i+1] == '{' {
			flush(p.i)
			p.i++ // past '@'; cursor now at '{' for parseInterp
			in, err := p.parseInterp()
			if err != nil {
				return nil, err
			}
			nodes = append(nodes, in)
			segStart = p.i
			segStartPos = p.posAt(p.i)
			continue
		}
		p.i++
```

Also update the two doc-comments in that file that mention the token: the function-doc on `parseRawTextBody` ("is split into Text and `${ … }` Interp children" → "`@{ … }`") and any inline `${`→`@{` in comments.

- [ ] **Step 2: Change the printer emit** in `internal/printer/printer.go` `styleChildren`. Replace `p.ws("${ ")` with `p.ws("@{ ")`, and update the function-doc comment ("Interp nodes use the `${ }` delimiter" → "`@{ }`").

- [ ] **Step 3: Sweep `${` → `@{` in tests, example, doc-comments, and corpus inputs.** Run this targeted sweep (the only `${` tokens in the tree today are the gsx `<style>` delimiter — no JS template literals exist yet — so the rename is safe; Step 6 verifies nothing unintended remains):

```bash
cd /Users/jackieli/personal/gsxhq/gsx
for f in \
  parser/markup_test.go internal/printer/printer_test.go internal/printer/fuzz_test.go \
  internal/wsnorm/wsnorm_test.go internal/codegen/rawcss_test.go internal/codegen/emit.go \
  internal/cssmin/cssmin.go internal/cssmin/file.go gen/options.go examples/02_text_escaping.gsx \
  internal/corpus/testdata/cases/style/block_interpolation.txtar \
  internal/corpus/testdata/cases/style/block_stringer.txtar \
  internal/corpus/testdata/cases/style/block_bytes.txtar \
  internal/corpus/testdata/cases/style/block_tuple_error.txtar \
  internal/corpus/testdata/cases/style/block_try_rejected.txtar \
  internal/corpus/testdata/cases/style/block_pipe_rejected.txtar \
  internal/corpus/testdata/cases/codegen-shape/style_interp.txtar ; do
  perl -i -pe 's/\$\{/\@{/g' "$f"
done
```

(`perl` so the literal `${` is matched without shell expansion. This rewrites the txtar `input.gsx` sections AND their baked `generated.x.go`/`render` golden sections — Step 4 regenerates the goldens authoritatively, so any golden churn here is overwritten.)

- [ ] **Step 4: Add the behavior-change tests to `parser/markup_test.go`** — prove `@{` now triggers and `${` is now literal. Append:

```go
func TestStyleAtBraceTriggers(t *testing.T) {
	src := `<style>.a{width:@{w}px}</style>`
	p := testParser(src)
	n, err := p.parseElement()
	if err != nil {
		t.Fatal(err)
	}
	el := n.(*ast.Element)
	if len(el.Children) != 3 {
		t.Fatalf("got %d children, want 3: %#v", len(el.Children), el.Children)
	}
	if in, ok := el.Children[1].(*ast.Interp); !ok || in.Expr != "w" {
		t.Fatalf("child1 = %#v, want Interp{Expr:w}", el.Children[1])
	}
}

func TestStyleDollarBraceIsNowLiteral(t *testing.T) {
	// After the migration, ${ … } inside <style> is plain CSS text, not an interp.
	src := `<style>.a{content:"${ w }"}</style>`
	p := testParser(src)
	n, err := p.parseElement()
	if err != nil {
		t.Fatal(err)
	}
	el := n.(*ast.Element)
	if len(el.Children) != 1 {
		t.Fatalf("got %d children, want 1 (all literal): %#v", len(el.Children), el.Children)
	}
	if txt := el.Children[0].(*ast.Text); txt.Value != `.a{content:"${ w }"}` {
		t.Fatalf("text = %q, want it verbatim incl. ${ }", txt.Value)
	}
}
```

- [ ] **Step 5: Regenerate corpus + examples goldens**

Run:
```bash
go test ./internal/corpus -run TestCorpus -update
go test ./internal/corpus -run Example -update
```
Then inspect a regenerated case (`internal/corpus/testdata/cases/style/block_interpolation.txtar`) to confirm its `input.gsx` now uses `@{ … }` and the `generated.x.go.golden` is unchanged in substance (same `_gsxgw.CSS`/`strconv` calls — only the source delimiter differs). If `git diff` shows any golden where the *generated Go* changed beyond the source delimiter, STOP and report it.

- [ ] **Step 6: Verify no stray `${` remains and the suite is green**

Run:
```bash
go build ./...
grep -rn '\${' --include='*.go' --include='*.gsx' --include='*.txtar' . | grep -v '/\.git/\|/\.claude/worktrees/' || echo "no \${ remaining"
go test ./...
```
Expected: build clean; the grep prints `no ${ remaining` (every gsx `${` is now `@{`); `go test ./...` all green — including `internal/printer` (the round-trip faithfulness + idempotence property tests over the corpus) and `internal/corpus`. If the printer property test fails, the parser trigger and printer emit are out of sync (one still on `${`) — fix the mismatch.

- [ ] **Step 7: Note the migration in the slice-1 spec**

In `docs/superpowers/specs/2026-06-22-gsx-style-safe-interpolation-design.md`, add a one-line note under the delimiter section (search `${`): "**Migrated to `@{ }`** (2026-06-23, slice A) to avoid the JS template-literal `${}` collision — see `2026-06-23-gsx-js-interpolation-design.md`." (Leave the historical body otherwise intact.)

- [ ] **Step 8: Commit**

```bash
git add parser/ internal/ gen/ examples/ docs/superpowers/specs/2026-06-22-gsx-style-safe-interpolation-design.md
git commit -m "parser+printer: migrate <style> interpolation delimiter \${ } -> @{ } (slice A)"
```

---

## Self-Review

**Spec coverage (Component 5 — token migration):** the `${} → @{}` migration across parser trigger, printer emit, examples, corpus goldens, and a slice-1 spec note → Task 1 (all steps). `SafeCSS→RawCSS` is excluded (already on `main`), per the spec. ✓

**Placeholder scan:** no TBD/TODO; the only non-literal step is the `-update` golden regeneration (Step 5), which is the harness writing goldens, plus the `grep`-verifies-clean gate (Step 6). Every code edit shows exact code. ✓

**Type/name consistency:** the trigger is `@`+`{` everywhere (parser Step 1); the printer emits `"@{ "` (Step 2); the new tests assert `@{` triggers and `${` is literal (Step 4); the sweep + grep gate ensure no `${` token survives (Steps 3, 6). Parser and printer change together (Global Constraints) so the round-trip property test stays the coupling guard. ✓
