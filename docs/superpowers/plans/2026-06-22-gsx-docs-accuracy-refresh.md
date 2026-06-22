# gsx Docs Accuracy Refresh Plan

> **For agentic workers:** Targeted accuracy edits — no new sections or pages.
> Steps use checkbox (`- [ ]`) syntax.

**Goal:** Bring the docs in line with the current state of `main`: gsx is now
**runnable end-to-end** (`gsx generate`/`fmt`/`info` shipped; codegen far past
"phase 1"; pipeline `|>` + `std` filters shipped; example 02 green / 12/12 parse;
contextual auto-escaping shipped). Correct every stale claim. **Scope: accuracy
only** — no Getting Started, no new guide pages (deferred).

**Decisions (from review):**
- Accuracy fix only.
- Escape-hatch: **`gsx.Raw` only** (the one shipped opt-out), no pipeline/roadmap
  mention. The pipeline security filters (`raw`/`js`/`json`) and `gsx.SafeURL` are
  NOT shipped (`std` ships only `Upper`/`Lower`/`Trim`/`Truncate`/`Join`/`Default`);
  drop the `gsx.SafeURL` claim. *(Revised after verifying shipped filters.)*
- Your unpushed local-`main` `<style>` `${ }` / CSS-minification specs are
  **design, not shipped** → not documented here (watch item).

## Global constraints
- Status-honest, but now reflecting reality: gsx **is** runnable end-to-end; do not
  re-introduce "not yet buildable" language. Name remaining gaps accurately.
- `examples/` is canonical; no invented syntax. Every form shown exists in
  `examples/NN_*.gsx` or the specs. Pipeline `{ x |> raw }` is now shipped/canonical.
- Two repos: doc content in `gsxhq/gsx`; the site home blockquote in
  `gsxhq/gsxhq.github.io` (`index.md`).

## Canonical status banner (reuse verbatim where a status blockquote exists)

```
> **Status — alpha.** gsx is runnable end-to-end: `gsx generate` compiles
> `.gsx` → `.x.go` (plus `gsx fmt` and `gsx info`). Codegen covers interpolation,
> control flow, attributes with contextual escaping, the `|>` pipeline + filters,
> components/props/`{children}`, method components, named slots, and attribute
> fallthrough. Still in progress: some CLI commands (`vet`/`lsp`), `style`
> composition, and structured diagnostics. See the [roadmap](<roadmap-url-or-path>).
```

Use the existing per-file link form for the roadmap (relative `docs/ROADMAP.md`
in-repo; the GitHub blob URL where the file currently uses that).

---

## Task 1: gsx repo — status banners (README, index, vision)

**Files:** `README.md`, `docs/index.md`, `docs/guide/vision.md`

- [ ] Replace the stale status blockquote in each (README:7, index.md:11,
  vision.md:67 — all currently "…phase 1 are done. The CLI is a work in progress,
  so gsx is **not yet runnable end-to-end**…") with the canonical banner above,
  keeping each file's existing roadmap-link form.
- [ ] README "What is gsx": where it says codegen, ensure it does not imply only
  "phase 1". (The pipeline line `.gsx → … → go build → HTML` stays.)
- [ ] **README "A taste" caveat** (README:40): replace
  `*(Illustrative — `.gsx` files are not yet buildable; the CLI is WIP.)*`
  with an accurate one-liner (no full tutorial):
  `*Run `gsx generate` to compile this to plain Go (`.x.go`), then `go build`.*`
- [ ] Commit: `docs: status banners — gsx is runnable end-to-end`

## Task 2: gsx repo — syntax page (example 02 + status)

**Files:** `docs/guide/syntax.md`

- [ ] Banner (syntax.md:6): remove the now-false caveat
  "(one file, `02_text_escaping.gsx`, has a tracked parser gap; see the roadmap)".
  Replace with the clean claim, e.g.:
  "The [`examples/`](https://github.com/gsxhq/gsx/tree/main/examples) corpus is the
  canonical, always-current reference — every accepted form is demonstrated there
  (all 12 examples parse)."
- [ ] Status footer (syntax.md:75): replace
  "`.gsx` files are illustrative; the CLI that generates `.x.go` is a work in
  progress." with the canonical banner (or a one-line accurate equivalent:
  "`.gsx` files compile to plain Go via `gsx generate`.").
- [ ] (Optional, accuracy) The comment row/example: example 02 now uses braced
  `{/* … */}` content comments (bare `//` in content is literal text). If the page
  states a comment form, ensure it matches; do not invent syntax.
- [ ] Commit: `docs: syntax page — example 02 green, gsx generate ships`

## Task 3: gsx repo — principles (security shipped + pipeline opt-out primary)

**Files:** `docs/guide/principles.md`

- [ ] "Secure by construction" (principles.md ~27): keep auto-escape-by-default;
  state that contextual escaping is **shipped** (URL scheme allow-list,
  always-quoted attribute values, fail-closed compile errors for bare exprs in
  JS `on*`/CSS `style` contexts) rather than framing it purely as intent.
- [ ] Opt-out lines (principles.md:33-35): make the **pipeline filter primary**:
  "The opt-out is an explicit, grep-able pipeline filter: `{ x |> raw }` for
  trusted HTML, `{ data |> json }` for data in `<script>`, etc. — type-checked and
  pluggable. (The runtime equivalents `gsx.Raw`/`gsx.SafeURL` exist for direct use.)"
  Remove "(A pipeline-based opt-out form is a design direction on the roadmap.)" —
  it is shipped.
- [ ] Verify every form shown exists: `grep -rn "|> raw\|gsx.Raw\|gsx.SafeURL" examples/ std/ docs/superpowers/specs/` and confirm `std` registers the filters used. If a named filter (e.g. `json`) is not yet in `std`, show only filters that exist.
- [ ] Commit: `docs: principles — escaping shipped, pipeline opt-out primary`

## Task 4: gsx repo — authoring skill

**Files:** `skills/gsx/SKILL.md`

- [ ] Status note (SKILL.md:12): replace "`.gsx` files are not yet buildable
  end-to-end" with: "`.gsx` files compile to plain Go via `gsx generate`; treat
  `examples/*.gsx` as the canonical reference." Keep the examples-canonical steer.
- [ ] Escape-hatch line: where it lists `gsx.Raw`, present `{ x |> raw }` as the
  primary opt-out (runtime `gsx.Raw` as equivalent), consistent with principles.
- [ ] Confirm no other "WIP/not buildable" language remains; no invented syntax.
- [ ] Commit: `docs: skill — gsx generate ships, pipeline opt-out`

## Task 5: website repo — home hero status

**Files:** `~/personal/gsxhq/gsxhq.github.io/index.md`

- [ ] Replace the hero's status blockquote ("…phase 1 are done. The CLI is a work
  in progress, so gsx is **not yet runnable end-to-end**…") with the canonical
  banner (GitHub blob roadmap link, as it uses now).
- [ ] Confirm the three `features:` entries are still accurate (type-safe,
  close-to-HTML, templ-compatible) — they are; no change expected.
- [ ] Commit + push (triggers deploy). Verify the live site shows the new status.

---

## Verification
- `grep -rn "not yet runnable\|not yet buildable\|CLI is a work in progress\|tracked parser gap" docs/ skills/ README.md` → no matches.
- Every gsx form shown (esp. `{ x |> raw }`) is attested in `examples/`, `std/`, or specs.
- Website: `npm run build` succeeds (with `GSX_DOCS_SRC` pointed at the worktree), and the deployed home shows the runnable-end-to-end status.

## Landing
- gsx repo edits (Tasks 1–4): commit on the worktree branch, push, PR → `main`
  (or fast-forward), as before.
- Website edit (Task 5): direct commit + push to `gsxhq.github.io` `main`.
