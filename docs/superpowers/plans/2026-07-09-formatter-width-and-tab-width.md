# Formatter Width & Tab Width Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Give `gsx fmt` one configurable tab width (honoring `.editorconfig`), then replace paren-wrap-on-width with breaking a composite literal's fields.

**Architecture:** Change A threads a single `tabWidth` through `internal/pretty` → `internal/printer` → `internal/gsxfmt` → `gen`, resolved per-file as `option > gsx.toml > .editorconfig > 2`. Change B adds `breakWideLiterals`, a source pre-pass sibling to `blockFormBraces` that runs before `go/format` sees the region, and narrows `parenWrapDoc` to genuinely multi-line elements.

**Tech Stack:** Go 1.26.1, `go/format`, `go/printer`, `github.com/editorconfig/editorconfig-core-go/v2` (new), txtar corpus.

## Global Constraints

- Spec: `docs/superpowers/specs/2026-07-09-formatter-width-and-tab-width-design.md`. Read it first.
- Work in this worktree. Never commit to `main`. Before Task 1: `git checkout -b feat/fmt-tab-width origin/main`. Tasks 1–6 land on that branch and ship as one PR. **Task 6 ends with a hard stop**: that PR must be merged before Task 7, because Change B's width rule is meaningless until the measure is coherent. Before Task 7: `git fetch origin && git checkout -b feat/fmt-break-wide-literals origin/main`.
- The root `gsx` package is **standard-library only**. The editorconfig dependency lives in tooling (`gen/`), never in the runtime.
- `.editorconfig`'s `indent_style` is **explicitly unsupported** (gofmt always emits tabs). Document it; do not silently ignore it.
- Every formatter change ships a fmt-corpus case (`internal/gsxfmt/testdata/cases/*.txtar`). Regenerate with `go test ./internal/gsxfmt -run TestFmtCorpus -update`, then verify **without** `-update`.
- **A new golden must be proven to discriminate**: revert the pass, confirm the case fails, restore. A golden that passes both ways tests nothing.
- Never hand-edit a `.golden` or a generated `.x.go`.
- Before any PR: `make ci` and `make lint`, both exit 0.
- The invariant both source pre-passes must preserve: **output is a gofmt fixed point** — `gofmt(out) == out`, and the pass is a no-op on its own output.

---

## File Structure

**Change A**
- Modify `internal/pretty/print.go` — `tabWidth` const → `Print` parameter; `advance()` counts tabs.
- Modify `internal/printer/printer.go:36,42` — `Fprint`/`FprintWith` take `tabWidth`.
- Modify `internal/gsxfmt/gsxfmt.go:43` — `FormatOptions.TabWidth`.
- Create `gen/editorconfig.go` — `.editorconfig` resolution, cached.
- Create `gen/editorconfig_test.go`
- Modify `gen/configfile.go:44,199,273` — `[formatter] tab_width`.
- Modify `gen/fmt.go:144-160,220` — per-file settings resolution.
- Modify `internal/gsxfmt/corpus_test.go` — `-- tab_width --` section.
- Modify `docs/guide/config.md`, `docs/guide/cli.md`.

**Change B**
- Create `internal/printer/breakwide.go`
- Create `internal/printer/breakwide_test.go`
- Modify `internal/printer/printer.go` — call the pass; narrow `parenWrapDoc`.
- Delete `TestGoExprElementParenWrapsOnWidthOverflow` (`internal/printer/goexpr_test.go:25`).
- Delete `internal/gsxfmt/testdata/cases/element_paren_wrap_on_overflow.txtar`.
- Modify `internal/gsxfmt/testdata/cases/element_paren_wrap_no_align_drift.txtar`.
- Modify `docs/guide/cli.md`.

---

## Task 1: Thread tabWidth through internal/pretty

**Files:**
- Modify: `internal/pretty/print.go:8-10` (const block), `:29` (`Print`), `:57`, `:130` (`advance`)
- Test: `internal/pretty/print_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces: `func Print(d Doc, width, tabWidth int) string`. A `tabWidth <= 0` falls back to `DefaultTabWidth`. `const DefaultTabWidth = 2`.

- [ ] **Step 1: Write the failing test**

Append to `internal/pretty/print_test.go`:

```go
// A literal tab inside a Text doc is real output width. advance() counted runes,
// so a tab read as 1 column while the printer's own indent levels read as
// tabWidth. Same tab, two answers.
func TestPrintCountsLiteralTabsAtTabWidth(t *testing.T) {
	// Text of 3 tabs + 70 runes = 76 columns at tabWidth 2, 79 at tabWidth 4.
	// Budget 78: fits at 2, must break at 4.
	lead := Text("\t\t\t" + strings.Repeat("x", 70))
	doc := Concat(lead, Group(Concat(SoftLine, Text("yy"))))

	if got := Print(doc, 78, 2); strings.Contains(got, "\n") {
		t.Errorf("tabWidth=2: 76+2 = 78 columns fits, want flat, got %q", got)
	}
	if got := Print(doc, 78, 4); !strings.Contains(got, "\n") {
		t.Errorf("tabWidth=4: 79+2 = 81 columns overflows, want a break, got %q", got)
	}
}

func TestPrintDefaultsTabWidth(t *testing.T) {
	doc := Text("\tx")
	if Print(doc, 80, 0) != Print(doc, 80, DefaultTabWidth) {
		t.Error("tabWidth<=0 must fall back to DefaultTabWidth")
	}
}
```

Ensure `import "strings"` is present in that file.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/pretty -run 'TestPrintCountsLiteralTabs|TestPrintDefaultsTabWidth' 2>&1 | head`
Expected: FAIL to compile — `too many arguments in call to Print`.

- [ ] **Step 3: Implement**

In `internal/pretty/print.go`, delete `tabWidth = 4` from the const block at `:8-10` and add:

```go
// DefaultTabWidth is the column width of one tab when nothing is configured.
// Indentation is emitted as tabs; this is how many columns each one occupies
// when deciding whether a group fits. It applies both to the printer's own
// indent levels and to literal tabs inside a Text doc — the same physical tab
// in the same output file, which must be measured the same way.
const DefaultTabWidth = 2
```

Change `Print` (`:29`):

```go
func Print(d Doc, width, tabWidth int) string {
	if width <= 0 {
		width = defaultWidth
	}
	if tabWidth <= 0 {
		tabWidth = DefaultTabWidth
	}
	var b strings.Builder
	pos := 0
	stack := []cmd{{indent: 0, mode: modeBreak, doc: d}}
```

At `:57` replace `pos = c.indent * tabWidth` with `pos = c.indent * tabWidth` (now the parameter — no textual change, but confirm it resolves to the parameter, not a const).

Inside `Print`'s `kindText` case, replace `pos = advance(pos, c.doc.text)` with `pos = advance(pos, c.doc.text, tabWidth)`.

Replace `advance` (`:130`):

```go
// advance returns the column after writing s starting at column pos. A literal
// tab counts as tabWidth columns, matching how the printer's own indent levels
// are measured — otherwise the same tab is one column here and tabWidth there.
func advance(pos int, s string, tabWidth int) int {
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		s = s[i+1:]
		pos = 0
	}
	return pos + utf8.RuneCountInString(s) + (tabWidth-1)*strings.Count(s, "\t")
}
```

`fits` also measures text. Add a `tabWidth int` parameter to `fits` (`:142`) and to `fillStep` (`:86`), and in both `kindText` and `kindLine` cases replace `remaining -= utf8.RuneCountInString(c.doc.text)` with:

```go
			remaining -= utf8.RuneCountInString(c.doc.text) + (tabWidth-1)*strings.Count(c.doc.text, "\t")
```

Thread `tabWidth` from `Print` into every `fits`/`fillStep` call.

- [ ] **Step 4: Run tests**

Run: `go test ./internal/pretty -count=1`
Expected: PASS (all, including pre-existing tests).

- [ ] **Step 5: Fix the three callers so the tree builds**

`internal/printer/printer.go:48` → `pretty.Print(doc, width, tabWidth)` — for now pass `pretty.DefaultTabWidth`; Task 2 threads it properly.
`internal/printer/printer.go:1148` → `pretty.Print(p.markup(n), 1<<30, pretty.DefaultTabWidth)`
`internal/printer/printer.go:1333` → `pretty.Print(doc, wide, pretty.DefaultTabWidth)`

Both `:1148` and `:1333` measure a doc at effectively infinite width to learn its flat form; tab width cannot change a flat rendering, so `DefaultTabWidth` there is correct permanently, not a placeholder.

- [ ] **Step 6: Verify the whole tree builds and the corpus still passes**

Run: `go build ./... && go test ./internal/pretty ./internal/printer ./internal/gsxfmt -count=1`
Expected: PASS. **Note:** the default moved 4 → 2 for markup indent. If any golden shifts, STOP and report — the spec predicts zero shifts, and a shift means a real layout change worth showing the user before burying it in a regenerate.

- [ ] **Step 7: Commit**

```bash
git add internal/pretty internal/printer/printer.go
git commit -m "refactor(pretty): one tab width for indent levels and literal tabs

advance() counted a literal tab as one column while the printer's own indent
levels counted it as tabWidth. Same physical tab, two answers. Make tabWidth a
Print parameter, thread it through fits/fillStep, and count literal tabs with
it. Default drops 4 -> 2 per the width spec."
```

---

## Task 2: Thread tabWidth through printer and gsxfmt

**Files:**
- Modify: `internal/printer/printer.go:36,42-49`
- Modify: `internal/gsxfmt/gsxfmt.go:22,30,37,43-73`
- Modify: `internal/lsp/format.go:51`, `internal/lsp/codeaction.go:52`, `gen/fmt.go:160,215`
- Test: `internal/gsxfmt/gsxfmt_test.go`

**Interfaces:**
- Consumes: `pretty.Print(d, width, tabWidth)`, `pretty.DefaultTabWidth` (Task 1).
- Produces:
  - `func printer.Fprint(w io.Writer, f *ast.File, width, tabWidth int) error`
  - `func printer.FprintWith(w io.Writer, f *ast.File, width, tabWidth int, cssFmt, jsFmt rawfmt.Formatter) error`
  - `gsxfmt.FormatOptions.TabWidth int` (0 → `pretty.DefaultTabWidth`)
  - `func gsxfmt.Format(name string, src []byte, width int) ([]byte, error)` — unchanged signature, uses the default tab width.

- [ ] **Step 1: Write the failing test**

Append to `internal/gsxfmt/gsxfmt_test.go`:

```go
// Tab width changes where a line overflows, so the same source lays out
// differently at 2 and at 4. Nothing pinned this before: changing tabWidth
// broke zero tests, which measured coverage, not safety.
func TestFormatWithTabWidthChangesLayout(t *testing.T) {
	// A deeply-indented element whose line sits between the two budgets.
	src := []byte("package ui\n\ncomponent C() {\n\t<div>\n\t\t<span class=\"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\">x</span>\n\t</div>\n}\n")

	at2, err := FormatWith("x.gsx", src, FormatOptions{Width: 80, TabWidth: 2})
	if err != nil {
		t.Fatal(err)
	}
	at4, err := FormatWith("x.gsx", src, FormatOptions{Width: 80, TabWidth: 4})
	if err != nil {
		t.Fatal(err)
	}
	if string(at2) == string(at4) {
		t.Errorf("tab width had no effect on layout:\n%s", at2)
	}
}

func TestFormatOptionsTabWidthZeroIsDefault(t *testing.T) {
	src := []byte("package ui\n\ncomponent C() {\n\t<p>hi</p>\n}\n")
	zero, err := FormatWith("x.gsx", src, FormatOptions{Width: 80, TabWidth: 0})
	if err != nil {
		t.Fatal(err)
	}
	def, err := FormatWith("x.gsx", src, FormatOptions{Width: 80, TabWidth: pretty.DefaultTabWidth})
	if err != nil {
		t.Fatal(err)
	}
	if string(zero) != string(def) {
		t.Error("TabWidth 0 must mean DefaultTabWidth")
	}
}
```

Add `"github.com/gsxhq/gsx/internal/pretty"` to that file's imports.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/gsxfmt -run TestFormatWithTabWidth 2>&1 | head`
Expected: FAIL to compile — `unknown field TabWidth`.

If `TestFormatWithTabWidthChangesLayout` compiles but the two outputs are equal, the chosen `src` doesn't straddle the budget. Widen the `class` attribute until they differ, then keep that value. **Do not** weaken the assertion.

- [ ] **Step 3: Implement**

`internal/printer/printer.go`:

```go
func Fprint(w io.Writer, f *ast.File, width, tabWidth int) error {
	return FprintWith(w, f, width, tabWidth, defaultCSSFormatter(width), defaultJSFormatter(width))
}

func FprintWith(w io.Writer, f *ast.File, width, tabWidth int, cssFmt, jsFmt rawfmt.Formatter) error {
	p := printer{cssFmt: cssFmt, jsFmt: jsFmt}
	doc := p.file(f)
	if p.err != nil {
		return p.err
	}
	_, err := io.WriteString(w, pretty.Print(doc, width, tabWidth))
	return err
}
```

`internal/gsxfmt/gsxfmt.go`: add to `FormatOptions` after `Width`:

```go
	// TabWidth is how many columns one tab occupies when measuring a line
	// (0 → pretty.DefaultTabWidth). It does not change what is emitted —
	// indentation is always tabs — only where lines are judged too long.
	TabWidth int
```

In `FormatWith`, pass `opts.TabWidth` to `printer.Fprint`/`printer.FprintWith`. Leave `Format`, `FormatRemovingImports`, and `FormatRemovingImportsWith` signatures unchanged; they set `TabWidth: 0`.

- [ ] **Step 4: Fix remaining callers**

`internal/codegen/infer.go:946`, `internal/codegen/analyze.go:3420,3481` call **`go/printer`.Fprint**, not this one. Do not touch them. Verify with:

Run: `grep -n 'printer.Fprint' internal/codegen/*.go`
Expected: each line's file imports `go/printer`.

`gen/fmt.go:215` (`gsxfmt.Format`) needs no change.

- [ ] **Step 5: Run tests**

Run: `go build ./... && go test ./internal/printer ./internal/gsxfmt ./internal/lsp -count=1`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/printer internal/gsxfmt internal/lsp gen
git commit -m "feat(fmt): FormatOptions.TabWidth, threaded to the printer

Width alone cannot say whether a line overflows: a tab's column count is part
of the measure. Add TabWidth (0 = default) and thread it to pretty.Print."
```

---

## Task 3: `.editorconfig` resolution

**Files:**
- Create: `gen/editorconfig.go`
- Create: `gen/editorconfig_test.go`
- Modify: `go.mod`, `go.sum`

**Interfaces:**
- Consumes: nothing.
- Produces:

```go
// editorSettings are the .editorconfig values gsx honors. Zero means "unset".
type editorSettings struct {
	tabWidth  int
	printWidth int
}

func newEditorConfigResolver() *editorConfigResolver
func (r *editorConfigResolver) settingsFor(path string) editorSettings
```

- [ ] **Step 1: Add the dependency**

```bash
go get github.com/editorconfig/editorconfig-core-go/v2@v2.6.4
go mod tidy
```

Expected new modules: `github.com/editorconfig/editorconfig-core-go/v2`, `gopkg.in/ini.v1`. `golang.org/x/mod` is already required.

- [ ] **Step 2: Write the failing test**

Create `gen/editorconfig_test.go`:

```go
package gen

import (
	"os"
	"path/filepath"
	"testing"
)

func writeTree(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for name, body := range files {
		p := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestEditorConfigTabWidth(t *testing.T) {
	root := writeTree(t, map[string]string{
		".editorconfig": "root = true\n\n[*.gsx]\ntab_width = 3\n",
		"ui/a.gsx":      "",
	})
	got := newEditorConfigResolver().settingsFor(filepath.Join(root, "ui/a.gsx"))
	if got.tabWidth != 3 {
		t.Errorf("tabWidth = %d, want 3", got.tabWidth)
	}
}

// Per the EditorConfig spec, tab_width defaults to indent_size.
func TestEditorConfigIndentSizeFallback(t *testing.T) {
	root := writeTree(t, map[string]string{
		".editorconfig": "root = true\n\n[*.gsx]\nindent_style = tab\nindent_size = 4\n",
		"ui/a.gsx":      "",
	})
	if got := newEditorConfigResolver().settingsFor(filepath.Join(root, "ui/a.gsx")); got.tabWidth != 4 {
		t.Errorf("tabWidth = %d, want 4 (from indent_size)", got.tabWidth)
	}
}

// A [*] section must not leak into .gsx when a [*.gsx] section overrides it.
func TestEditorConfigGlobSpecificity(t *testing.T) {
	root := writeTree(t, map[string]string{
		".editorconfig": "root = true\n\n[*]\ntab_width = 8\n\n[*.gsx]\ntab_width = 2\n",
		"ui/a.gsx":      "",
	})
	if got := newEditorConfigResolver().settingsFor(filepath.Join(root, "ui/a.gsx")); got.tabWidth != 2 {
		t.Errorf("tabWidth = %d, want 2", got.tabWidth)
	}
}

func TestEditorConfigMaxLineLength(t *testing.T) {
	root := writeTree(t, map[string]string{
		".editorconfig": "root = true\n\n[*.gsx]\nmax_line_length = 100\n",
		"ui/a.gsx":      "",
	})
	if got := newEditorConfigResolver().settingsFor(filepath.Join(root, "ui/a.gsx")); got.printWidth != 100 {
		t.Errorf("printWidth = %d, want 100", got.printWidth)
	}
}

// "off" means no limit. gsx has no unbounded width, so it means "unset" and the
// caller falls through to its own default.
func TestEditorConfigMaxLineLengthOff(t *testing.T) {
	root := writeTree(t, map[string]string{
		".editorconfig": "root = true\n\n[*.gsx]\nmax_line_length = off\n",
		"ui/a.gsx":      "",
	})
	if got := newEditorConfigResolver().settingsFor(filepath.Join(root, "ui/a.gsx")); got.printWidth != 0 {
		t.Errorf("printWidth = %d, want 0 (unset)", got.printWidth)
	}
}

func TestEditorConfigAbsentIsUnset(t *testing.T) {
	root := writeTree(t, map[string]string{"ui/a.gsx": ""})
	got := newEditorConfigResolver().settingsFor(filepath.Join(root, "ui/a.gsx"))
	if got.tabWidth != 0 || got.printWidth != 0 {
		t.Errorf("no .editorconfig: got %+v, want zero", got)
	}
}

// A malformed .editorconfig must never fail a format run.
func TestEditorConfigMalformedIsUnset(t *testing.T) {
	root := writeTree(t, map[string]string{
		".editorconfig": "\x00\x01 not ini [[[\n",
		"ui/a.gsx":      "",
	})
	got := newEditorConfigResolver().settingsFor(filepath.Join(root, "ui/a.gsx"))
	if got.tabWidth != 0 || got.printWidth != 0 {
		t.Errorf("malformed: got %+v, want zero", got)
	}
}
```

- [ ] **Step 3: Run test to verify it fails**

Run: `go test ./gen -run TestEditorConfig 2>&1 | head`
Expected: FAIL — `undefined: newEditorConfigResolver`.

- [ ] **Step 4: Implement**

Create `gen/editorconfig.go`:

```go
package gen

import (
	"strconv"
	"sync"

	"github.com/editorconfig/editorconfig-core-go/v2"
)

// editorSettings holds the .editorconfig values gsx honors, resolved for one
// file. A zero field means the key was absent (or unusable), and the caller
// falls through to the next configuration layer.
//
// indent_style is deliberately NOT honored. gofmt emits tabs for Go, always;
// satisfying `indent_style = space` would mean re-indenting gofmt's output,
// which is the one thing every layout rule in gsx fmt is built to avoid.
type editorSettings struct {
	tabWidth   int // from tab_width, or indent_size per the EditorConfig spec
	printWidth int // from max_line_length; "off" resolves to 0 (unset)
}

// editorConfigResolver resolves .editorconfig per file. Resolution walks up to
// the nearest `root = true`, so it is per-file, not per-directory: sections are
// filename globs ([*.gsx]). The library's CachedParser memoizes each
// .editorconfig file it reads, which is what keeps `gsx fmt ./...` from
// re-reading the same file once per source file.
type editorConfigResolver struct {
	mu  sync.Mutex
	cfg *editorconfig.Config
}

func newEditorConfigResolver() *editorConfigResolver {
	return &editorConfigResolver{cfg: &editorconfig.Config{Parser: editorconfig.NewCachedParser()}}
}

// settingsFor never fails: a missing, unreadable, or malformed .editorconfig
// yields the zero editorSettings, exactly like printWidthFor's own
// discovery/decode fallbacks. gsx fmt must not die on someone else's config.
func (r *editorConfigResolver) settingsFor(path string) editorSettings {
	r.mu.Lock()
	def, err := r.cfg.Load(path)
	r.mu.Unlock()
	if err != nil || def == nil {
		return editorSettings{}
	}
	s := editorSettings{tabWidth: def.TabWidth}
	if s.tabWidth < 0 {
		s.tabWidth = 0
	}
	// max_line_length lives in Raw; "off" means no limit, which gsx expresses as
	// "unset" so the caller's own default applies.
	if raw, ok := def.Raw["max_line_length"]; ok && raw != "off" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			s.printWidth = n
		}
	}
	return s
}
```

The mutex guards `CachedParser`, which is not documented as goroutine-safe; `gsx fmt` formats files concurrently.

- [ ] **Step 5: Run tests**

Run: `go test ./gen -run TestEditorConfig -count=1 -v 2>&1 | tail -20`
Expected: all seven PASS.

- [ ] **Step 6: Confirm the runtime stayed dependency-free**

Run: `go list -deps . | grep -c editorconfig`
Expected: `0`. If nonzero, the dependency leaked into the runtime package — STOP.

- [ ] **Step 7: Commit**

```bash
git add gen/editorconfig.go gen/editorconfig_test.go go.mod go.sum
git commit -m "feat(fmt): resolve tab_width and max_line_length from .editorconfig

Uses editorconfig-core-go, the reference implementation, rather than
hand-rolling root=true walk-up, glob sections, and later-section-wins merging.
indent_style is deliberately unsupported: gofmt always emits tabs.
Malformed config resolves to unset — never a format failure."
```

---

## Task 4: Wire tab_width into gsx.toml and the fmt command

**Files:**
- Modify: `gen/configfile.go:43-46` (`tomlFormatter`), `:199-207` (decode), `:270-280` (merge), and the `config` struct's `printWidth` neighbor
- Modify: `gen/fmt.go:144-160` (per-file loop), `:220-230` (`printWidthFor`)
- Test: `gen/configfile_test.go`, `gen/fmt_test.go`

**Interfaces:**
- Consumes: `editorConfigResolver.settingsFor(path)` (Task 3); `gsxfmt.FormatOptions.TabWidth` (Task 2).
- Produces: `func formatSettingsFor(dir, path string, ec *editorConfigResolver) (width, tabWidth int)` in `gen/fmt.go`, replacing `printWidthFor(dir)`.

Precedence, highest first: programmatic option → `gsx.toml [formatter]` → `.editorconfig` → built-in (`width` 80, `tabWidth` `pretty.DefaultTabWidth`).

- [ ] **Step 1: Write the failing precedence test**

Create `gen/formatsettings_test.go`:

```go
package gen

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/gsxhq/gsx/internal/pretty"
)

func TestFormatSettingsPrecedence(t *testing.T) {
	tests := []struct {
		name          string
		files         map[string]string
		wantWidth     int
		wantTabWidth  int
	}{{
		name:         "nothing configured: built-in defaults",
		files:        map[string]string{"a.gsx": ""},
		wantWidth:    80,
		wantTabWidth: pretty.DefaultTabWidth,
	}, {
		name: "editorconfig alone",
		files: map[string]string{
			".editorconfig": "root = true\n\n[*.gsx]\ntab_width = 3\nmax_line_length = 100\n",
			"a.gsx":         "",
		},
		wantWidth:    100,
		wantTabWidth: 3,
	}, {
		name: "gsx.toml overrides editorconfig",
		files: map[string]string{
			".editorconfig": "root = true\n\n[*.gsx]\ntab_width = 3\nmax_line_length = 100\n",
			"gsx.toml":      "[formatter]\nprint_width = 120\ntab_width = 8\n",
			"a.gsx":         "",
		},
		wantWidth:    120,
		wantTabWidth: 8,
	}, {
		name: "gsx.toml partial: unset keys still fall through to editorconfig",
		files: map[string]string{
			".editorconfig": "root = true\n\n[*.gsx]\ntab_width = 3\nmax_line_length = 100\n",
			"gsx.toml":      "[formatter]\ntab_width = 8\n",
			"a.gsx":         "",
		},
		wantWidth:    100, // from .editorconfig
		wantTabWidth: 8,   // from gsx.toml
	}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := writeTree(t, tt.files)
			cwd, _ := os.Getwd()
			t.Cleanup(func() { _ = os.Chdir(cwd) })
			if err := os.Chdir(root); err != nil {
				t.Fatal(err)
			}
			w, tw := formatSettingsFor(".", filepath.Join(root, "a.gsx"), newEditorConfigResolver())
			if w != tt.wantWidth || tw != tt.wantTabWidth {
				t.Errorf("got width=%d tabWidth=%d, want width=%d tabWidth=%d", w, tw, tt.wantWidth, tt.wantTabWidth)
			}
		})
	}
}
```

`os.Chdir` in a test is a reentrancy hazard — this package had a `gen -C os.Chdir` bug before. Do **not** add `t.Parallel()` to this test.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./gen -run TestFormatSettingsPrecedence 2>&1 | head`
Expected: FAIL — `undefined: formatSettingsFor`.

- [ ] **Step 3: Add tab_width to the config file**

`gen/configfile.go`, in `tomlFormatter` (`:43`):

```go
type tomlFormatter struct {
	PrintWidth int    `toml:"print_width"`
	TabWidth   int    `toml:"tab_width"`
	Imports    string `toml:"imports"`
}
```

Add `tabWidth int` to the `config` struct beside `printWidth`.

In the decode block (`:199`):

```go
	if tc.Formatter != nil {
		cfg.printWidth = tc.Formatter.PrintWidth
		cfg.tabWidth = tc.Formatter.TabWidth
```

In the merge block (`:273`), mirror `printWidth` exactly:

```go
	merged.tabWidth = base.tabWidth
	if opts.tabWidth > 0 {
		merged.tabWidth = opts.tabWidth
	}
```

Add `tabWidth int` to the options struct alongside `printWidth`.

Add an `effectiveTabWidth()` beside `effectivePrintWidth()`, returning `pretty.DefaultTabWidth` when `tabWidth <= 0`. **Do not** import `internal/pretty` into the runtime — `gen` is tooling, so this is fine; confirm with the Step 8 check.

- [ ] **Step 4: Replace printWidthFor with formatSettingsFor**

`gen/fmt.go`, replacing `printWidthFor` (`:220`):

```go
// formatSettingsFor resolves the print width and tab width for one file.
//
// Precedence, highest first: gsx.toml [formatter] > .editorconfig > built-in.
// (There is no CLI flag or env var for either knob; print_width has never had
// one, and tab_width should not grow one alone.) .editorconfig is a cross-tool
// baseline, so an explicit gsx setting beats it even when the .editorconfig
// sits closer to the file.
//
// dir selects the gsx.toml (discovery is per-directory); path selects the
// .editorconfig section (sections are filename globs). Every layer is
// best-effort: a missing or broken config falls through, never fails.
func formatSettingsFor(dir, path string, ec *editorConfigResolver) (width, tabWidth int) {
	es := ec.settingsFor(path)
	width, tabWidth = es.printWidth, es.tabWidth

	if cfgPath, ok := discoverConfig(dir); ok {
		if cfg, err := loadConfig(cfgPath); err == nil {
			if cfg.printWidth > 0 {
				width = cfg.printWidth
			}
			if cfg.tabWidth > 0 {
				tabWidth = cfg.tabWidth
			}
		}
	}
	if width <= 0 {
		width = 80
	}
	if tabWidth <= 0 {
		tabWidth = pretty.DefaultTabWidth
	}
	return width, tabWidth
}
```

Add `"github.com/gsxhq/gsx/internal/pretty"` to `gen/fmt.go`'s imports.

- [ ] **Step 5: Use it in the format loop**

`gen/fmt.go:144-166`. The old `widthByDir` cache keyed on directory; tab width and width are now per-file, so drop it — `editorConfigResolver` already memoizes the parsed `.editorconfig` files, and `discoverConfig`/`loadConfig` are the same cost per directory as before. Replace:

```go
	ec := newEditorConfigResolver()
	for _, path := range files {
		orig, err := os.ReadFile(path)
		if err != nil {
			fmt.Fprintf(stderr, "%s: %v\n", path, err)
			exit = 1
			continue
		}
		abs, _ := filepath.Abs(path)
		dir := filepath.Dir(path)
		width, tabWidth := formatSettingsFor(dir, abs, ec)
		mode := modeFor(path)
		formatted, err := gsxfmt.FormatWith(path, orig, gsxfmt.FormatOptions{
			Unused:   unusedByPath[abs],
			Width:    width,
			TabWidth: tabWidth,
			CSSFmt:   cssFmt,
			JSFmt:    jsFmt,
			Reorder:  mode.Reorder(),
		})
```

Pass `abs` (not `path`) to `formatSettingsFor` — `.editorconfig` walk-up needs an absolute path.

`gen/fmt.go:215` (`gsxfmt.Format(name, src, printWidthFor("."))`) becomes:

```go
	w, _ := formatSettingsFor(".", name, newEditorConfigResolver())
	return gsxfmt.Format(name, src, w)
```

- [ ] **Step 6: Run tests**

Run: `go test ./gen -run 'TestFormatSettings|TestEditorConfig|TestConfig' -count=1 2>&1 | tail`
Expected: PASS.

- [ ] **Step 7: Full build and suite**

Run: `go build ./... && go test ./gen ./internal/gsxfmt ./internal/printer -count=1`
Expected: PASS.

- [ ] **Step 8: Confirm the runtime is still stdlib-only**

Run: `go list -deps . | grep -cE 'editorconfig|ini\.v1'`
Expected: `0`.

- [ ] **Step 9: Commit**

```bash
git add gen internal
git commit -m "feat(fmt): [formatter] tab_width, and .editorconfig as a config layer

Precedence: option > gsx.toml [formatter] > .editorconfig > built-in. Width and
tab width become per-file lookups, because .editorconfig sections are filename
globs while gsx.toml discovery is per-directory. max_line_length feeds
print_width, which therefore gains .editorconfig as a source too."
```

---

## Task 5: fmt-corpus coverage for tab width

**Files:**
- Modify: `internal/gsxfmt/corpus_test.go` (add a `-- tab_width --` section)
- Create: `internal/gsxfmt/testdata/cases/tab_width_2.txtar`
- Create: `internal/gsxfmt/testdata/cases/tab_width_4.txtar`

**Interfaces:**
- Consumes: `gsxfmt.FormatOptions.TabWidth` (Task 2).
- Produces: a corpus harness that reads an optional `-- tab_width --` section (absent → `0`, meaning default).

- [ ] **Step 1: Extend the harness**

In `internal/gsxfmt/corpus_test.go`, after the `-- imports --` parsing, add:

```go
			tabWidth := 0
			if raw, ok := archiveFile(ar, "tab_width"); ok {
				n, err := strconv.Atoi(strings.TrimSpace(string(raw)))
				if err != nil || n <= 0 {
					t.Fatalf("case %s: bad tab_width %q", path, raw)
				}
				tabWidth = n
			}
```

Add `tabWidth` to the `FormatOptions` literal:

```go
			opts := FormatOptions{Unused: unused, Width: fmtWidth, TabWidth: tabWidth, Reorder: mode.Reorder()}
```

Add `"strconv"` to imports, and document the new section in the file's doc comment alongside `-- imports --` and `-- unused --`.

- [ ] **Step 2: Write the two cases**

`internal/gsxfmt/testdata/cases/tab_width_2.txtar`:

```
The same source, formatted at tab width 2 and 4 (see tab_width_4.txtar), must lay
out differently: a tab's column count decides where a line overflows. Nothing
pinned this before — changing pretty's tabWidth broke zero tests, which measured
coverage, not safety.

Indentation is emitted as tabs either way. tab_width changes only the measure.

-- tab_width --
2
-- input.gsx --
package ui

component C() {
	<div>
		<section>
			<span class="aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa">x</span>
		</section>
	</div>
}
```

`internal/gsxfmt/testdata/cases/tab_width_4.txtar`: identical, with `4` in the `tab_width` section and the first paragraph's cross-reference pointing at `tab_width_2.txtar`.

- [ ] **Step 3: Generate the goldens**

Run: `go test ./internal/gsxfmt -run TestFmtCorpus -update && go test ./internal/gsxfmt -run TestFmtCorpus -count=1`
Expected: PASS both.

- [ ] **Step 4: Prove the cases discriminate**

Run:
```bash
diff <(sed -n '/-- fmt.golden --/,$p' internal/gsxfmt/testdata/cases/tab_width_2.txtar) \
     <(sed -n '/-- fmt.golden --/,$p' internal/gsxfmt/testdata/cases/tab_width_4.txtar)
```
Expected: **non-empty output.** If the goldens are identical, tab width had no effect on this input — widen the `class` attribute in both until they diverge, regenerate, and re-check. A pair of identical goldens tests nothing.

- [ ] **Step 5: Commit**

```bash
git add internal/gsxfmt
git commit -m "test(fmt): pin layout at tab width 2 and 4

The corpus had no case near a width boundary at depth, so changing pretty's
tabWidth broke zero tests. Add a -- tab_width -- section and two cases whose
goldens must differ."
```

---

## Task 6: Document tab_width and .editorconfig

**Files:**
- Modify: `docs/guide/config.md`
- Modify: `docs/guide/cli.md`

- [ ] **Step 1: Document the config key and precedence**

In `docs/guide/config.md`, in the `[formatter]` table, add `tab_width` (default `2`) beside `print_width`. Then add:

```markdown
### `.editorconfig`

`gsx fmt` reads [`.editorconfig`](https://editorconfig.org/), honoring two keys:

| Key | Effect |
|-----|--------|
| `tab_width` | how many columns one tab counts as when measuring a line (falls back to `indent_size`, per the EditorConfig spec) |
| `max_line_length` | the print width; `off` means "use gsx's default", since gsx has no unbounded width |

`indent_style` is **not** honored. gofmt emits tabs for Go, always, and gsx does
not re-indent gofmt's output.

Resolution order, highest first:

```
gsx.toml [formatter]  >  .editorconfig  >  built-in (print_width 80, tab_width 2)
```

An explicit gsx setting wins even when the `.editorconfig` sits closer to the
file: `.editorconfig` is a cross-tool baseline, `gsx.toml` is gsx's own answer.
A missing or malformed `.editorconfig` is ignored, never an error.
```

Check no `{{` appears in the added prose (VitePress parses it as a Vue interpolation and the docs build fails). Run: `grep -n '{{' docs/guide/config.md`

- [ ] **Step 2: Note the measure in the CLI guide**

In `docs/guide/cli.md`, after the "Your line breaks are preserved" section, add one paragraph: indentation is always tabs; `tab_width` changes only how wide a tab is *counted* when deciding whether a line overflows, and it comes from `gsx.toml` or `.editorconfig`.

- [ ] **Step 3: Verify and commit**

Run: `make ci && make lint`
Expected: both exit 0.

```bash
git add docs
git commit -m "docs: [formatter] tab_width and .editorconfig support"
```

- [ ] **Step 4: Open the PR for Change A**

```bash
git push -u origin feat/fmt-tab-width
gh pr create --base main --head feat/fmt-tab-width \
  --title "feat(fmt): one configurable tab width, honoring .editorconfig" --body "<see below>"
```

The PR body must state: the default moved from 4 (markup) / 1 (literal tabs in Go) to a single 2, so **markup layout changes for every `.gsx` file with no configuration**. Include a before/after of one real file. **STOP HERE and get the PR merged before starting Task 7.**

---

## Task 7: `breakWideLiterals` — the pass

**Files:**
- Create: `internal/printer/breakwide.go`
- Create: `internal/printer/breakwide_test.go`

**Interfaces:**
- Consumes: `blockFormBraces(src string) string` (`internal/printer/blockform.go`).
- Produces: `func breakWideLiterals(src string, width, tabWidth int) string`. `src` is a complete Go file. Returns `src` unchanged on any parse error.

- [ ] **Step 1: Write the failing test**

Create `internal/printer/breakwide_test.go`:

```go
package printer

import (
	"go/format"
	"strings"
	"testing"
)

func TestBreakWideLiterals(t *testing.T) {
	tests := []struct {
		name       string
		src, want  string
	}{{
		name: "narrow literal untouched",
		src:  "package p\n\nvar x = []T{\n\t{a: 1, b: 2},\n}\n",
		want: "package p\n\nvar x = []T{\n\t{a: 1, b: 2},\n}\n",
	}, {
		// 90 columns at tabWidth 1: the outer break alone brings it under.
		name: "outermost first: inner then fits, so it is left alone",
		src:  "package p\n\nvar x = []T{{alpha: \"aaaaaaaaaaaaaaaa\", beta: \"bbbbbbbbbbbbbbbb\", gamma: \"cccccccccccccccc\"}}\n",
		want: "package p\n\nvar x = []T{\n\t{alpha: \"aaaaaaaaaaaaaaaa\", beta: \"bbbbbbbbbbbbbbbb\", gamma: \"cccccccccccccccc\"},\n}\n",
	}, {
		// A single field wider than the budget: no break can help. Stop, don't loop.
		name: "no progress: single over-long field is left alone",
		src:  "package p\n\nvar x = T{a: \"" + strings.Repeat("z", 100) + "\"}\n",
		want: "package p\n\nvar x = T{a: \"" + strings.Repeat("z", 100) + "\"}\n",
	}, {
		name: "unparseable source passes through",
		src:  "package p\n\nvar x = T{{{\n",
		want: "package p\n\nvar x = T{{{\n",
	}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := breakWideLiterals(tt.src, 80, 1); got != tt.want {
				t.Errorf("breakWideLiterals:\n got %q\nwant %q", got, tt.want)
			}
		})
	}
}

// The invariant that makes this pass an extension of gofmt rather than a fork:
// gofmt accepts the output, is stable on it, and the pass is a no-op on gofmt's
// own output. Written BEFORE the pass, per the spec's risk note.
func TestBreakWideLiteralsOutputIsGofmtFixedPoint(t *testing.T) {
	srcs := []string{
		"package p\n\nvar x = []T{{alpha: \"aaaaaaaaaaaaaaaa\", beta: \"bbbbbbbbbbbbbbbb\", gamma: \"cccccccccccccccc\"}}\n",
		"package p\n\nvar x = map[string]string{\"alpha\": \"one\", \"beta\": \"two\", \"gamma\": \"three\", \"delta\": \"four\", \"epsilon\": \"five\"}\n",
		"package p\n\nvar x = T{a: \"" + strings.Repeat("z", 100) + "\"}\n",
	}
	for _, src := range srcs {
		rewritten := breakWideLiterals(src, 80, 1)
		out, err := format.Source([]byte(rewritten))
		if err != nil {
			t.Errorf("gofmt rejected breakWideLiterals output for %q: %v\n%s", src, err, rewritten)
			continue
		}
		again, err := format.Source(out)
		if err != nil || string(again) != string(out) {
			t.Errorf("gofmt not stable on breakWideLiterals output for %q", src)
			continue
		}
		if got := breakWideLiterals(string(out), 80, 1); got != string(out) {
			t.Errorf("breakWideLiterals re-fires on gofmt's output for %q:\n got %q\nwant %q", src, got, out)
		}
	}
}
```

Run the second test's `want` values only after Step 3 confirms them; if a `want` in the first test disagrees with reality, verify by hand which is correct **before** editing the test.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/printer -run TestBreakWideLiterals 2>&1 | head`
Expected: FAIL — `undefined: breakWideLiterals`.

- [ ] **Step 3: Implement**

Create `internal/printer/breakwide.go`:

```go
package printer

import (
	"go/format"
	goast "go/ast"
	goparser "go/parser"
	gotoken "go/token"
	"strings"
	"unicode/utf8"
)

// breakWideLiterals returns src with the fields of over-long composite literals
// broken one per line, until no line exceeds width (or no further progress is
// possible).
//
// gofmt never breaks a long line: go/printer copies the breaks between a
// literal's elements from the source and invents none (go/printer/nodes.go,
// exprList). So an over-long `{a: 1, b: 2, …}` stays over-long. prettier, faced
// with the same object literal, breaks its properties — and that, not wrapping
// the element that happens to sit inside it, is the remedy the line needs.
//
// Each round gofmt's the source, finds the first over-long line, and breaks the
// OUTERMOST composite literal on it. A nested literal is only reached on a later
// round, and only if its own line is still over budget after the outer break —
// which converges on the fewest breaks that bring every line under the limit.
//
// Termination is on NO PROGRESS, never on a round count: a single field wider
// than the budget cannot be fixed by breaking, and must not loop forever.
//
// The output is a gofmt FIXED POINT. gofmt preserves the breaks this pass adds,
// so re-running the pass on its own output is a no-op, and gsx fmt extends gofmt
// without ever fighting it. See TestBreakWideLiteralsOutputIsGofmtFixedPoint.
//
// src must be a complete Go file. On any parse or format error it is returned
// unchanged: this is a layout nicety, never a reason for gsx fmt to fail.
func breakWideLiterals(src string, width, tabWidth int) string {
	for {
		formatted, err := format.Source([]byte(src))
		if err != nil {
			return src
		}
		next, changed := breakFirstWideLiteral(string(formatted), width, tabWidth)
		if !changed {
			return string(formatted)
		}
		src = next
	}
}

// breakFirstWideLiteral finds the first line of src exceeding width and breaks
// the outermost composite literal that starts on it, returning changed=false
// when there is no such line or no literal to break (no progress).
func breakFirstWideLiteral(src string, width, tabWidth int) (string, bool) {
	fset := gotoken.NewFileSet()
	file, err := goparser.ParseFile(fset, "", src, goparser.SkipObjectResolution)
	if err != nil {
		return src, false
	}
	badLine := firstWideLine(src, width, tabWidth)
	if badLine == 0 {
		return src, false
	}

	// The outermost literal whose opening brace is on badLine and whose fields
	// are not already broken. goast.Inspect visits parents before children, so
	// the first match is the outermost.
	var target *goast.CompositeLit
	goast.Inspect(file, func(n goast.Node) bool {
		if target != nil {
			return false
		}
		lit, ok := n.(*goast.CompositeLit)
		if !ok || lit.Incomplete || len(lit.Elts) < 2 {
			return true
		}
		if fset.Position(lit.Lbrace).Line != badLine {
			return true
		}
		// Already one-per-line? Then breaking it again is not progress.
		first := fset.Position(lit.Elts[0].Pos()).Line
		last := fset.Position(lit.Elts[len(lit.Elts)-1].Pos()).Line
		if last > first {
			return true
		}
		target = lit
		return false
	})
	if target == nil {
		return src, false
	}

	// Insert a newline before every element after the first. gofmt supplies the
	// indentation, the alignment, and (with blockFormBraces) the closing brace.
	// A comma already separates the elements, so no comma is inserted here and
	// automatic semicolon insertion cannot fire.
	offsets := make([]int, 0, len(target.Elts)-1)
	for _, elt := range target.Elts[1:] {
		offsets = append(offsets, fset.Position(elt.Pos()).Offset)
	}
	out := src
	for i := len(offsets) - 1; i >= 0; i-- {
		off := offsets[i]
		// Replace the run of spaces before the element with a newline.
		start := off
		for start > 0 && (out[start-1] == ' ' || out[start-1] == '\t') {
			start--
		}
		out = out[:start] + "\n" + out[off:]
	}
	// The first element must also start its own line, and the brace must close on
	// one. blockFormBraces does the latter; do the former here.
	firstOff := fset.Position(target.Elts[0].Pos()).Offset
	start := firstOff
	for start > 0 && (out[start-1] == ' ' || out[start-1] == '\t') {
		start--
	}
	if start > 0 && out[start-1] == '{' {
		out = out[:start] + "\n" + out[firstOff:]
	}
	return blockFormBraces(out), true
}

// firstWideLine returns the 1-based number of the first line of src wider than
// width, or 0. A tab counts as tabWidth columns, matching internal/pretty.
func firstWideLine(src string, width, tabWidth int) int {
	for i, line := range strings.Split(src, "\n") {
		cols := utf8.RuneCountInString(line) + (tabWidth-1)*strings.Count(line, "\t")
		if cols > width {
			return i + 1
		}
	}
	return 0
}
```

Note the offsets are computed against `src` but applied to `out`, which the loop mutates. Applying right-to-left keeps every earlier offset valid — the same argument `blockFormBraces` and `collapseHoleWhitespace` rely on. The first-element insertion happens **after** the loop and at a smaller offset than all of them, so it is also safe.

- [ ] **Step 4: Run the tests**

Run: `go test ./internal/printer -run TestBreakWideLiterals -v -count=1 2>&1 | tail -20`
Expected: PASS. If a `want` is wrong, decide by hand which of test and code is right before changing either. If the fixed-point test fails, the pass is fighting gofmt — STOP and report, do not "fix" the test.

- [ ] **Step 5: Commit**

```bash
git add internal/printer/breakwide.go internal/printer/breakwide_test.go
git commit -m "feat(fmt): breakWideLiterals — break composite-literal fields on overflow

gofmt never breaks a long line. prettier breaks the object's properties. This
pass does the latter for embedded Go: outermost literal first, re-measure,
repeat, terminating on no progress. Output is a gofmt fixed point."
```

---

## Task 8: Wire the pass in and narrow paren-wrap

**Files:**
- Modify: `internal/printer/printer.go` — `fmtGoChunk`, `fmtGoExprParts`, `goWithElements`; `printer` gains `width`/`tabWidth` fields
- Modify: `internal/printer/goexpr_test.go:25` — delete `TestGoExprElementParenWrapsOnWidthOverflow`
- Delete: `internal/gsxfmt/testdata/cases/element_paren_wrap_on_overflow.txtar`
- Modify: `internal/gsxfmt/testdata/cases/element_paren_wrap_no_align_drift.txtar`

**Interfaces:**
- Consumes: `breakWideLiterals(src, width, tabWidth)` (Task 7); `goExprFlatWidth(doc) (int, bool)` (existing, `printer.go:1327`).
- Produces: no new exported API.

- [ ] **Step 1: Write the failing test**

Append to `internal/printer/goexpr_test.go`:

```go
// The element is 12 characters and fits anywhere. The line is 103 columns
// because of the Go fields around it. Breaking the element's parens does not
// address that; breaking the literal's fields does.
func TestGoExprWideLiteralBreaksFieldsNotElement(t *testing.T) {
	src := "package main\n\nvar nav = []item{\n\t{label: \"Team View\", icon: <UsersIcon/>, page: TeamViewPage{}, pathMatch: \"/team\", nonVendor: true},\n}\n"
	want := "package main\n\nvar nav = []item{\n\t{\n\t\tlabel:     \"Team View\",\n\t\ticon:      <UsersIcon/>,\n\t\tpage:      TeamViewPage{},\n\t\tpathMatch: \"/team\",\n\t\tnonVendor: true,\n\t},\n}\n"
	checkFormat(t, src, want)
}

// A short element on a short line never wraps, and a genuinely multi-line one
// still does. Paren-wrap is for multi-line elements, not for wide lines.
func TestGoExprElementNeverParenWrapsOnWidthAlone(t *testing.T) {
	name := strings.Repeat("x", 62)
	src := "package main\n\nvar " + name + " = <div>x</div>\n"
	checkFormat(t, src, src) // 81 columns, and it stays flat — gofmt would too
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/printer -run 'TestGoExprWideLiteral|TestGoExprElementNeverParenWraps' 2>&1 | head -20`
Expected: both FAIL — the first still paren-wraps, the second wraps on width.

- [ ] **Step 3: Give the printer its width and tab width**

`printer` currently has no width. Add fields and set them in `FprintWith`:

```go
type printer struct {
	// ... existing fields
	width    int
	tabWidth int
}
```

```go
func FprintWith(w io.Writer, f *ast.File, width, tabWidth int, cssFmt, jsFmt rawfmt.Formatter) error {
	if width <= 0 {
		width = 80
	}
	if tabWidth <= 0 {
		tabWidth = pretty.DefaultTabWidth
	}
	p := printer{cssFmt: cssFmt, jsFmt: jsFmt, width: width, tabWidth: tabWidth}
```

`fmtGoChunk` is a package-level function. Make it a method: `func (p *printer) fmtGoChunk(src string) string`, and update its one call site in `decl` (`printer.go:130`).

- [ ] **Step 4: Run both passes at both gofmt call sites**

In `fmtGoChunk`, replace `blockFormBraces(goExprWrapper + src)` with:

```go
	prepared := breakWideLiterals(goExprWrapper+src, p.width, p.tabWidth)
	out, err := format.Source([]byte(blockFormBraces(prepared)))
```

In `fmtGoExprParts`, replace `blockFormBraces(sanitized)` with:

```go
	prepared := breakWideLiterals(sanitized, p.width, p.tabWidth)
	out, err := format.Source([]byte(blockFormBraces(prepared)))
```

`breakWideLiterals` measures the placeholder, which is exactly as wide as the element renders flat — so the line it measures is the line that will be printed. A multi-line element reaches it as a one-rune placeholder and so under-measures; that case is handled in Step 5.

- [ ] **Step 5: Break a literal holding a multi-line element unconditionally**

A multi-line element's true width is unknowable through a one-rune placeholder, and such a literal can never be a one-liner. In `fmtGoExprParts`, before calling `breakWideLiterals`, widen the placeholder for a non-flat value from `1` rune to `width+1` runes so the line measures as over-budget:

```go
		width, flat := goExprFlatWidth(doc)
		if !flat {
			// Multi-line: it can never be a one-liner, and its real width is
			// unknowable. Make the line measure as over-budget so the enclosing
			// literal breaks, then let the element printer lay it out.
			width = p.width + 1
		}
```

**Careful:** the placeholder's rune count is also what gofmt aligns comments to. Confirm the existing `TestGoWithElementsNarrowElementCommentAlignment` and `TestGoWithElementsMultilineElementStillFormats` still pass. If they don't, keep the 1-rune placeholder for gofmt and pass a separate over-budget hint to `breakWideLiterals` instead.

- [ ] **Step 6: Narrow parenWrapDoc**

In `goWithElements` (`printer.go` ~`:238`), replace:

```go
		if eligible(i) {
			doc = parenWrapDoc(doc)
		}
```

with:

```go
		// Paren-wrap is for an element that is ITSELF multi-line — a block-level
		// child, or an author's line break. Never for a wide line: the fields
		// around the element make the line wide, and breakWideLiterals breaks
		// those. Wrapping the element instead moves the ink without moving the
		// problem.
		if _, flat := goExprFlatWidth(doc); eligible(i) && !flat {
			doc = parenWrapDoc(doc)
		}
```

- [ ] **Step 7: Delete the tests that pin the removed behavior**

Delete `TestGoExprElementParenWrapsOnWidthOverflow` (`internal/printer/goexpr_test.go:25-34`) — it pins exactly what Step 6 removes, so it is deleted, not adapted.

```bash
rm -f internal/gsxfmt/testdata/cases/element_paren_wrap_on_overflow.txtar
```

- [ ] **Step 8: Repair the #62 regression case**

`element_paren_wrap_no_align_drift.txtar` guards against alignment drift in the decorative-paren shape. Its elements are single-line, so under the new rule they never paren-wrap and the case stops testing anything. Replace both elements with genuinely multi-line ones so the paren shape still occurs. The `Sanitize` fix from #62 is still required — author-written multi-line elements still produce the paren and still re-enter the formatter.

Rewrite the `input.gsx` section to:

```
-- input.gsx --
package ui

var nav = []item{
	{label: "Team View", icon: <UsersIcon>
		<title>users</title>
	</UsersIcon>, page: TeamViewPage{}, pathMatch: "/team"},
	{label: "Search", icon: <SearchIcon>
		<title>search</title>
	</SearchIcon>, page: ListPage{}, pathMatch: "/list/"},
}
```

- [ ] **Step 9: Regenerate and verify**

Run: `go test ./internal/gsxfmt -run TestFmtCorpus -update && go test ./internal/gsxfmt -count=1`
Expected: PASS. Read every changed golden; the diff is the feature.

- [ ] **Step 10: Prove `element_paren_wrap_no_align_drift` still discriminates**

Temporarily revert `Sanitize`'s before-collapse (make `inParens` always `false` in `internal/goexprshape/shape.go`), then:

Run: `go test ./internal/gsxfmt -run 'TestFmtCorpus/element_paren_wrap_no_align_drift' -count=1`
Expected: **FAIL**, on the harness's idempotence check. Restore `Sanitize` and confirm PASS. If it passes both ways, the case no longer guards #62 — fix the case, not the assertion.

- [ ] **Step 11: Full suite**

Run: `go test ./... -count=1 2>&1 | grep -v '^ok' | head`
Expected: no failures.

- [ ] **Step 12: Commit**

```bash
git add internal docs
git commit -m "feat(fmt): break wide composite literals; paren-wrap only multi-line elements

An element paid for a width overrun it did not cause: <UsersIcon/> is 12
characters, split across three lines because the Go fields around it made the
line 103 columns. Break the fields instead, which is prettier's actual rule.

parenWrapDoc now fires only for a genuinely multi-line element.
TestGoExprElementParenWrapsOnWidthOverflow and its corpus case pinned the
removed behavior and are deleted."
```

---

## Task 9: Corpus coverage for `breakWideLiterals`, docs, and PR

**Files:**
- Create: `internal/gsxfmt/testdata/cases/breakwide_nav_items.txtar`
- Create: `internal/gsxfmt/testdata/cases/breakwide_outermost_first.txtar`
- Create: `internal/gsxfmt/testdata/cases/breakwide_nested_also_breaks.txtar`
- Create: `internal/gsxfmt/testdata/cases/breakwide_multiline_element.txtar`
- Create: `internal/gsxfmt/testdata/cases/breakwide_no_progress.txtar`
- Create: `internal/gsxfmt/testdata/cases/breakwide_gochunk_no_elements.txtar`
- Modify: `docs/guide/cli.md`

- [ ] **Step 1: Write the six cases**

Each is a txtar with a prose header stating the layout fact it pins, and an `-- input.gsx --`. No `fmt.golden` yet.

1. `breakwide_nav_items` — the motivating file's shape: three sibling items, each over budget, each broken.
2. `breakwide_outermost_first` — one over-long line with a nested literal; breaking the outer brings the inner under budget, so the inner stays inline. Header must say so.
3. `breakwide_nested_also_breaks` — the inner literal is *still* over budget after the outer break, so it breaks too.
4. `breakwide_multiline_element` — a literal holding an element with a block child: broken without measuring, element paren-wrapped inside.
5. `breakwide_no_progress` — a single field wider than the budget: nothing breaks, the line stays long. Pins termination.
6. `breakwide_gochunk_no_elements` — an over-long `map[string]string{…}` in a decl with no gsx element anywhere, proving the rule is not element-gated.

- [ ] **Step 2: Generate and verify**

Run: `go test ./internal/gsxfmt -run TestFmtCorpus -update && go test ./internal/gsxfmt -count=1`
Expected: PASS. Read all six goldens. `breakwide_no_progress`'s golden must still contain the long line — if it doesn't, the pass is breaking something it cannot fix, and it may not be terminating for the right reason.

- [ ] **Step 3: Prove they discriminate**

```bash
git stash push -u -m "breakwide-discriminate-check" -- internal/printer/breakwide.go internal/printer/printer.go
SHA=$(git stash list --format='%H %gs' | grep breakwide-discriminate-check | head -1 | cut -d' ' -f1)
go test ./internal/gsxfmt -run TestFmtCorpus 2>&1 | grep -c 'FAIL: TestFmtCorpus/breakwide'
git stash apply "$SHA"
git stash list --format='%gd %gs' | grep breakwide-discriminate-check | head -1 | cut -d' ' -f1 | xargs -r git stash drop
```

Expected: at least 5 of the 6 fail without the pass. (`breakwide_no_progress` legitimately passes either way — it asserts nothing changes. That is fine, and is *why* the other five must fail.)

Never use bare `git stash` / `git stash pop` — the stash stack is shared across worktrees.

- [ ] **Step 4: Document the rule**

In `docs/guide/cli.md`, next to the block-form brace rule, add: when a line exceeds the print width, gsx fmt breaks the fields of the outermost composite literal on it, one per line, and repeats until every line fits or no break can help. Elements are never wrapped in parens merely because their line is long — only when the element itself spans multiple lines. Show the nav-item before/after. Check for `{{`.

- [ ] **Step 5: Reformat one-learning as the real check**

```bash
go run ./cmd/gsx fmt -l /Users/jackieli/work/one-learning-gsx/ui
go run ./cmd/gsx fmt /Users/jackieli/work/one-learning-gsx/ui/appshell_nav.gsx > /tmp/bw1.gsx
go run ./cmd/gsx fmt /tmp/bw1.gsx | diff - /tmp/bw1.gsx && echo IDEMPOTENT
```

Expected: idempotent. Read `bw1.gsx` and confirm the nav items broke into fields with the icons inline. This is the acceptance criterion the whole change exists for.

- [ ] **Step 6: Full gate**

Run: `make ci && make lint`
Expected: both exit 0.

- [ ] **Step 7: Commit and open the PR for Change B**

```bash
git add internal docs
git commit -m "test(fmt): corpus coverage for breakWideLiterals"
git push -u origin feat/fmt-break-wide-literals
gh pr create --base main --head feat/fmt-break-wide-literals \
  --title "feat(fmt): break wide composite literals instead of paren-wrapping the element" --body "<see below>"
```

The PR body must include: the before/after on `appshell_nav.gsx`; that `TestGoExprElementParenWrapsOnWidthOverflow` and `element_paren_wrap_on_overflow.txtar` are **deleted** because they pinned the removed behavior; that `element_paren_wrap_no_align_drift` was rewritten with multi-line elements and re-proven to discriminate against the #62 `Sanitize` fix; and the gofmt-fixed-point property with its test name.

---

## Notes for the implementer

**The three bugs before this.** `Sanitize` (#57), its over-broad collapse (#58), and the alignment drift (#62) were all the same shape: a source rewrite that changed what `go/printer` reads. `go/printer` decides layout from **original source positions** — `exprList` at `nodes.go:145` (one-line fast path), the element loop (breaks between elements), and `nodes.go:294` (the closing brace). Every rewrite in this plan must be reasoned about against those three, not against what the output "looks like."

**Why the property tests can't save you.** `internal/printer`'s faithfulness, idempotence, re-parse-safety, and no-verbatim-fallback properties are all blind to layout. A formatter that reflows the author's source passes every one. Only a pinned fmt-corpus golden catches a layout regression — which is why every task that changes layout ships a case, and why every new golden must be proven to fail without the change.

**Automatic semicolon insertion.** `blockFormBraces` inserts `,\n` rather than `\n` because a bare newline before `}` lets Go insert a `;` after the last element. `breakWideLiterals` inserts `\n` *between* elements, where a comma already sits, so it does not have that problem. If you ever find yourself inserting a newline after an identifier, literal, `)`, `]`, or `}`, stop and check ASI.
