# internal/corpus — contributor guide

## What the corpus is

`internal/corpus` is the txtar-fixture test spine for gsx.
Every test case lives at `testdata/cases/<area>/<scenario>.txtar`.
Running `TestCorpus` loads **all** cases in a single batched `go/packages`
call (one `go run` invocation renders all renderable cases), then compares
each golden facet.

## Anatomy of a case file

A `.txtar` file is a sequence of named sections separated by `-- name --`
headers.  The sections the corpus recognises are:

| Section | Role | Checked |
|---|---|---|
| `input.gsx` | The GSX source under test | — (input) |
| `model.go` (or other `.go` siblings) | Extra Go types/helpers in the same package | — (input) |
| `invoke` | Go expression that calls the rendered component | — (input) |
| `diagnostics.golden` | Expected compiler diagnostics (one per line) | **always** |
| `render.golden` | Expected rendered HTML | presence-based |
| `generated.x.go.golden` | Expected generated Go output | presence-based |
| `ast.golden` | Expected AST dump (parser layer only) | presence-based |

**Presence-based** means the facet is only compared when the section is
already in the file.  `diagnostics.golden` is always compared — an absent
section is treated as empty (no diagnostics expected).

### `ast.golden` — parser-layer rule

Pinning `ast.golden` in a case tells the harness the case lives at the
**parser layer**: `batchCodegen` is **skipped** for that case
(`corpus_test.go:64-66`).  The only facets checked are `ast.golden` and
`diagnostics.golden`.  Use this for cases that test parsing, AST structure,
or parse-time diagnostics without involving the code generator.

### Render-safety rule

A renderable case — any case that has an `invoke` section and produces no
diagnostics — **must** have a `render.golden` section.  If it is missing and
you are not running with `-update`, `TestCorpus` fatals (`corpus_test.go:111-113`).
Run `-update` to generate it.

## Diagnostics format

Each diagnostic line follows the convention:

```
[path:]line:col: message
```

The parser always carries source positions, so parser-layer cases reliably
produce `line:col:` prefixes.  Code-generator diagnostics currently emit
messages without position information — this is a known gap documented in the
backlog:

> `../../docs/superpowers/specs/codegen-diagnostic-position-audit.md`

## The `-update` workflow

Regenerate all golden sections (and `coverage.golden`) in one shot:

```sh
go test ./internal/corpus -run TestCorpus -update
```

This writes back every present golden section and `testdata/coverage.golden`.
After running, review the diff with `git diff` and commit only the expected
changes.

### Reading `coverage.golden`

`testdata/coverage.golden` lists every case and the facets it exercises:

```
attrs/expr_attrs         diag render
codegen-shape/greeting   diag gen render
parser/01_elements       ast diag
```

Each line is `<area>/<scenario>` followed by the active facet tags (`ast`,
`diag`, `diag(error)`, `gen`, `render`).  Use it to spot gaps: a whole area
with only `diag` and no `render` suggests missing invocations or untested
output.

## Adding a case

1. **Choose area and name** — pick an existing area directory under
   `testdata/cases/` or create a new one.

2. **Write the `.txtar` file** — copy and adapt a small existing case.
   Example based on `testdata/cases/interpolation/field_access.txtar`:

   ```
   -- model.go --
   package views

   type Greeting struct {
       Who string
   }
   -- input.gsx --
   package views

   component Hello(g Greeting) {
       <p>Hello, {g.Who}!</p>
   }
   -- invoke --
   Hello(HelloProps{Greeting: Greeting{Who: "world"}})
   -- diagnostics.golden --
   ```

   The `-- diagnostics.golden --` section is required (even when empty) so
   the harness always checks that no unexpected diagnostics are produced.

3. **Run `-update`** to generate the remaining golden sections:

   ```sh
   go test ./internal/corpus -run TestCorpus -update
   ```

4. **Inspect the diff** — check that `render.golden` (and optionally
   `generated.x.go.golden`) look correct.

5. **Optionally pin `generated.x.go.golden`** — add the section only for
   cases where you want to lock in the exact generated Go output.  It is
   fine to leave it absent for most cases.

6. **Run without `-update`** to confirm the case passes:

   ```sh
   go test ./internal/corpus -run TestCorpus -count=1
   ```

7. **Commit the `.txtar` file** (with the generated goldens) and the updated
   `testdata/coverage.golden`.

### Adding a parser-layer case

If the case tests parsing or the AST structure, add an `-- ast.golden --`
section.  Run `-update`; the harness will fill it in.  The case will then be
excluded from codegen automatically.

### Adding a diagnostics-only / error case

For a case that should produce diagnostics and NOT render, omit `invoke` and
populate `-- diagnostics.golden --` with the expected messages.  Run
`-update` to confirm the snapshot.

## Coverage

```sh
make cover
```

This runs the corpus tests with Go's `-coverprofile` and opens the HTML
report.  The corpus exercises the compiler end-to-end, so coverage here
reflects real-world code paths rather than unit-test artefacts.
