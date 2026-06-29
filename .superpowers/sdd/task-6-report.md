# Task 6 Report — `attributes` page

## Status

DONE — all fixtures pass, no CI drift, committed.

## Commit

`1c2e2f9` — `docs(syntax): attributes page`

## Test + drift summary

- `go test ./internal/corpus -run TestExamples` (no `-update`) → **PASS** (all 29 examples)
- `make ci-examples` → **exit 0** (no drift)

## Included partial paths

```
docs/guide/syntax/_generated/attributes/010-expression-attributes.md
docs/guide/syntax/_generated/attributes/020-boolean-attributes.md
docs/guide/syntax/_generated/attributes/030-conditional-attributes.md
docs/guide/syntax/_generated/attributes/040-spread-attributes.md
docs/guide/syntax/_generated/attributes/060-attribute-contexts.md
```

Page: `docs/guide/syntax/attributes.md`

## Fixtures created / edited

| File | pageOrder | Source corpus case | Notes |
|---|---|---|---|
| `examples/20-attributes.txtar` (edited) | 10 | (existing, simplified) | Removed bool + conditional; now expression attrs only (`href={url}`, `data-count={count}`) |
| `examples/230-boolean-attrs.txtar` (new) | 20 | `elements/static_and_bool_attrs_on` | Bare `required` + dynamic `disabled={on}` with `on: true` |
| `examples/231-conditional-attrs.txtar` (new) | 30 | `attrs/cond_attr_bool_on` | Renamed component to `Badge`; `{ if featured { class="featured" } }` |
| `examples/232-spread-attrs.txtar` (new) | 40 | `attrs/spread_trailing` | Two-key bag (`id`, `data-active`) demonstrates sorted output |
| `examples/234-attr-contexts.txtar` (new) | 60 | `security/url_blocked` | `javascript:` blocked → `about:invalid#gsx` |

No `233-ordered-attrs.txtar` created: `gsx.OrderedAttrs` and `{{ }}` sugar are not present on this branch (`grep OrderedAttrs *.go` returns nothing). Ordered-attrs documented as prose + static fenced `gsx` block in `attributes.md`, with a note that a runnable example is added once the feature lands.

## Concerns

None. All claims in `attributes.md` verified against:
- `attrs.go` — `Spread` sorts keys (`sort.Strings`); `bool` values render as bare attr / omit
- `security/url_blocked` render.golden — `javascript:` → `about:invalid#gsx`
- Corpus cases — syntax forms identical to source cases
- Generated partials — reviewed and confirmed correct
