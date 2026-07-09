# Formatter width: one tab width, and breaking wide composite literals

Status: design, approved 2026-07-09. Validated against `main @ e495901`.

## Problem

`gsx fmt` paren-wraps an element when the line it sits on exceeds the print
width:

```go
{label: "Team View", icon: (
    <UsersIcon/>
), page: TeamViewPage{}, pathMatch: "/team", nonVendor: true},
```

`<UsersIcon/>` is twelve characters and perfectly happy inline. It is split
across three lines because the *Go fields around it* make the line 103 columns.
The element is paying for a width overrun it did not cause.

The rule was pinned by `TestGoExprElementParenWrapsOnWidthOverflow` with the
justification "prettier wraps here too." The analogy leaks. In a `.jsx` file
prettier owns the whole line: faced with a 103-column object literal it breaks
the object properties one per line, and wrapping the JSX element is one part of
a coherent reflow. In gsx, **gofmt owns the Go and never breaks a long line**.
So we apply prettier's remedy without prettier's ability to apply it where the
problem is, and the reader gets a three-line construct whose long line was never
the element's fault.

The right rule is the one prettier actually has: **break the composite
literal's fields**, and let the element stay inline.

That rule needs to know how wide a line is. Today gsx cannot answer:

- `internal/pretty/print.go:10` sets `tabWidth = 4`, used when laying out the
  printer's own indent levels for markup.
- `advance()` (`print.go:130`) counts a literal tab inside Go text as **1**
  column, because it counts runes.

Those are the same physical tab in the same output file, measured two ways.
Nobody chose it; it fell out of counting runes. Changing `tabWidth` from 4 to 2
breaks **zero** tests, and so does teaching `advance()` to count tabs. That is
not reassurance — it means no test or golden sits near a width boundary at
depth, so gsx's width behavior is currently unverified.

## Decisions

| Question | Decision |
|---|---|
| Which literals break? | Every composite literal in embedded Go, element-bearing or not. |
| Paren-wrap on width? | Dropped. Paren-wrap survives only for genuinely multi-line elements. |
| Nesting | Outermost first, re-measure, repeat. Descend only if the inner line is still over. |
| Literal holding a multi-line element | Treated as over-budget without measuring. |
| Ragged siblings | Accepted. A width rule means a 79-column literal stays inline next to an 85-column one that breaks. |
| Tab width | One value everywhere. Default **2**. |
| `.editorconfig` | Respected. gsx's own config overrides it. |

## Two changes, in order

`breakWideLiterals` is meaningless while "how wide is this line" has two
answers. Ship the measure first.

### Change A — one tab width, configurable

A single tab width, threaded through every place a column is counted:

- `internal/pretty`: the `tabWidth` constant becomes a field on the print
  context, used both for indent levels and by `advance()` for literal tabs in
  `Text`.
- `internal/printer`: `Fprint`/`FprintWith` take it alongside `width`.
- `internal/gsxfmt`: `FormatOptions` gains `TabWidth`.

Default **2**. Note this changes markup layout for files with no configuration
at all, since markup indent behaves as 4 today.

#### Resolution order

`tab_width` follows the shape `print_width` already has: a `[formatter]` key
plus a programmatic option. `print_width` has **no CLI flag and no env var**
today (`gen/configfile.go:44`, `gen/options.go`), so `tab_width` gets neither
either. Inventing `-tab-width` and `GSX_TAB_WIDTH` would add a layer that no
other formatter knob has. If someone asks for it, both knobs should grow it
together.

`.editorconfig` slots in below gsx's own config and above the built-in default:

```
option (programmatic)  >  gsx.toml [formatter]  >  .editorconfig  >  default (2)
```

This is prettier's model (own config beats `.editorconfig` beats defaults). It
deliberately inverts "nearer file wins": an `.editorconfig` may sit closer to
the source file than `gsx.toml` and still lose, because `.editorconfig` is a
cross-tool baseline and an explicit gsx setting is an explicit gsx setting.

`[formatter]` (`gen/configfile.go:43`) gains `tab_width`, beside the existing
`print_width` and `imports`. The same layering applies to `print_width` itself
once `max_line_length` is honored: `.editorconfig` becomes a new source below
`gsx.toml` for a key that already exists.

Resolution granularity changes: `printWidthFor(dir)` (`gen/fmt.go:156`) is
per-directory, but `.editorconfig` sections are per-file globs (`[*.gsx]`). Both
`print_width` and `tab_width` become per-file lookups, backed by a
per-directory cache of the resolved `.editorconfig` definition.

#### `.editorconfig` keys

| Key | Handling |
|---|---|
| `tab_width` | → tab width. |
| `indent_size` | → tab width when `tab_width` is absent, per the EditorConfig spec. Handled by the library. |
| `max_line_length` | → `print_width`. The value `off` means "no limit"; map it to the built-in default rather than an unbounded width. |
| `indent_style` | **Explicitly unsupported.** Documented, not silently ignored. |

`indent_style = space` cannot be honored. gofmt emits tabs for Go, always.
Satisfying it would mean re-indenting gofmt's output, which puts us back to
fighting gofmt — the thing every rule in this design is built to avoid.

#### Implementation

`github.com/editorconfig/editorconfig-core-go/v2` — the reference Go
implementation, which passes the upstream core test suite. A faithful reader is
`root = true` walk-up, per-section glob matching, and later-section-wins
merging; that is not something to hand-roll.

Verified against the real library: `GetDefinitionForFilename` resolves
`tab_width`, falls back to `indent_size`, exposes `max_line_length` in `Raw`,
and returns `TabWidth == 0` with a nil error when no `.editorconfig` exists —
a clean unset sentinel. New transitive dependency: `gopkg.in/ini.v1`.
`golang.org/x/mod` is already required.

The `gsx` runtime package stays standard-library only. This dependency lives in
tooling.

The LSP resolves once per document.

### Change B — `breakWideLiterals`

A pre-pass on the Go source before `go/format` sees it, sibling to
`blockFormBraces`.

```
loop:
  gofmt the region
  find the first line exceeding the budget
  if none: done
  break the fields of the OUTERMOST composite literal on that line, one per line
  if no literal on that line, or no field was broken: done   (no progress)
```

Termination is on *no progress*, not on a fixed iteration count. A single field
longer than the budget cannot be fixed by breaking, and must not loop.

Descend into a nested literal only when its own line is still over budget after
the outer break. This converges on the fewest breaks that bring every line under
the limit, which is what a human does.

A literal holding a multi-line element is broken without measuring. Its true
width is unknowable — the element reaches gofmt as a one-rune placeholder — and
it can never be a one-liner anyway, since the element forces a break.

Author line breaks are still preserved; that is gofmt's rule and it is unchanged.
`blockFormBraces` then closes the brace.

#### The safety property, unchanged

Output stays a **gofmt fixed point**. gofmt preserves breaks between elements
(it copies them from the source and never invents them), so every break this
pass adds survives gofmt untouched, and re-running the pass on its own output is
a no-op. gsx fmt extends gofmt; it never fights it. This is the same property
`blockFormBraces` holds, and it is the invariant that makes both rules
defensible rather than a fork of gofmt.

#### Paren-wrap narrows

`parenWrapDoc` fires only when the element's own doc is multi-line — a
block-level child, or an author's line break. Never on width.

Consequences:

- `TestGoExprElementParenWrapsOnWidthOverflow` is deleted, not adapted. It pins
  the behavior being removed.
- The `element_paren_wrap_on_overflow` fmt-corpus case is deleted.
- The `element_paren_wrap_no_align_drift` case (added in #62) must have its
  element replaced with a genuinely multi-line one, or it stops exercising the
  paren shape it was written to guard. **The `Sanitize` fix in #62 is still
  required** — author-written multi-line elements still produce the decorative
  paren, and still re-enter the formatter.

Once fields break, `var someVeryLongName = <div>x</div>` at 81 columns stays
flat: there are no fields to break, and gofmt would leave any other 81-column
expression alone. That is the intended outcome, and the one case where "never
wrap on width" feels least comfortable.

## What this does to the motivating file

At tab width 2, all eight nav items in `one-learning/ui/appshell_nav.gsx` exceed
80 columns — including `Dashboard` at 82 — so all eight break. The ragged-sibling
question dissolves for this file, not because it was solved but because
everything crossed the line.

```go
{
    label:     "Team View",
    icon:      <UsersIcon/>,
    page:      TeamViewPage{},
    pathMatch: "/team",
    nonVendor: true,
},
```

## Testing

The fmt corpus (`internal/gsxfmt/testdata/cases/*.txtar`) is the only thing that
can see any of this. The printer's property tests — faithfulness, idempotence,
re-parse safety, no-verbatim-fallback — are all structurally blind to layout: a
formatter that reflows the author's source is still faithful, still idempotent,
and still re-parses. This is exactly how the #58 regression shipped.

Change A:

- Tab-width resolution unit tests per layer, including `.editorconfig` walk-up,
  `root = true`, glob sections, `indent_size` fallback, `max_line_length = off`,
  and absent-file.
- Precedence tests: each layer beating the one below it.
- A fmt-corpus case at each of tab width 2 and 4, pinning that the *same* input
  lays out differently — the coverage that does not exist today. This requires
  the corpus harness to gain a `-- tab_width --` section, alongside the existing
  `-- imports --` and `-- unused --`.

Change B:

- Corpus cases: break at depth; outermost-first with a nested literal that then
  fits; a nested literal that does *not* fit and must also break; a literal with
  a multi-line element; a single field wider than the budget (it still breaks
  once — no break can bring its own line under budget, but the pass breaks
  unconditionally once the flat form doesn't fit, same as prettier — and the
  pass must terminate after that one round rather than looping); an
  element-free Go chunk (proving the rule is not element-gated).
- A `breakWideLiterals` output-is-a-gofmt-fixed-point test, mirroring
  `TestBlockFormBracesOutputIsGofmtFixedPoint`.
- Every new corpus golden must be checked to *discriminate*: revert the pass,
  confirm the case fails.

## Risks

**Blast radius.** Change A alters markup layout for every `.gsx` file with no
configuration, because markup indent silently behaves as 4 today. Zero tests
fail, which measures our coverage, not our safety. Reformatting one-learning
before and after is the real check.

**`.editorconfig` surprise.** A repo that sets `indent_size = 4` for `[*]` and
never thinks about gsx will get tab width 4. That is correct behavior and will
still surprise someone. `gsx fmt -d` shows them why.

**Third ASI-adjacent pass.** `blockFormBraces` and `breakWideLiterals` both
rewrite Go source before gofmt reads it. Inserting a newline where Go's
automatic semicolon insertion then places a `;` has already caused three bugs
(#57, #58, #62). `breakWideLiterals` breaks *between* fields, where a comma
already separates them, so it does not have `blockFormBraces`'s comma problem —
but the fixed-point test must be written before the pass, not after.
