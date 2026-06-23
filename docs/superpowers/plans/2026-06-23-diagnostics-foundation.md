# Diagnostics Foundation (`internal/diag`) — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a structured diagnostics foundation (`internal/diag`) — `Diagnostic`/`Severity`/`Bag` + rich/compact/JSON renderers — and wire it through gsx so `gsx generate` reports **most** errors at once (all `go/types` errors + per-component codegen recovery) with real `.gsx` positions, rich human output, and `--json` for agents.

**Architecture:** A leaf `internal/diag` package holds the model (positions stored **resolved** as `token.Position`, fileset-agnostic) and three renderers. A per-package `*diag.Bag` is threaded through type-resolution and codegen; `PackageResult` gains `Diags []diag.Diagnostic`. Codegen recovers at the **component** boundary (per-component buffer; on error, record diagnostics and skip that component, continue siblings). Writes stay **per-package all-or-nothing**. The corpus harness renders diagnostics from `Diags` as a stable `line:col: message` projection; rich/structured fields are pinned by a few JSON-shape goldens + unit tests.

**Tech Stack:** Go; `go/token` (`token.Position`), `go/types`/`golang.org/x/tools/go/packages`; existing `internal/codegen`, `parser`, `internal/jsx`, `gen`, `internal/corpus` packages; `encoding/json`.

## Global Constraints

- **Slice 1 of 2.** This plan delivers the model, rendering, and **semantic-layer** recovery. **Parser error recovery is out of scope** (Slice 2) — the parser still returns a single `error`, wrapped into one diagnostic.
- **Report most errors, not the first** — surface ALL `go/types` errors; codegen accumulates per-component and continues.
- **Write safety unchanged** — a package with any `Error`-severity diagnostic writes **no** `.x.go` (never partial output); other packages still process and write.
- **Positions are resolved `token.Position`** (Filename, 1-based Line, 1-based byte Column) — diagnostics come from two filesets (gsx parser fset; `go/packages` fset for type errors), so the model is fileset-agnostic.
- **Severity:** only `Error` is *produced* in Slice 1; the full 4-level enum (`Error,Warning,Info,Hint`) exists for LSP-readiness.
- **No new dependencies** (stdlib + the already-present `golang.org/x/tools`).
- **Corpus churn kept minimal** — `diagnostics.golden` keeps its `line:col: message` projection (codegen gains positions; jsx gets real positions; parser unchanged); the rich/structured model is pinned by new JSON-shape goldens + unit tests. The `normalizeDiag` "at N" hack is deleted.
- **Migration worklist:** `docs/superpowers/specs/codegen-diagnostic-position-audit.md` enumerates all 55 positionless codegen error sites and the AST node that supplies each position — Task 3 follows it site-by-site.
- **Module:** `github.com/gsxhq/gsx`. **Run tests with:** `go test ./...` from the worktree root.
- **Worktree:** all work happens in `.claude/worktrees/diag-foundation` on branch `worktree-diag-foundation` (per the user's "CLI dev work uses a worktree" rule).

---

## File Structure

- **Create** `internal/diag/diag.go` — `Severity`, `Diagnostic`, `Bag`.
- **Create** `internal/diag/render.go` — `SourceProvider`, `RenderCompact`, `RenderJSON`, `RenderRich`.
- **Create** `internal/diag/diag_test.go`, `internal/diag/render_test.go`.
- **Modify** `internal/codegen/batch.go` — `PackageResult.Diags`; per-package `Bag`; all `go/types` errors → diagnostics; parse/jsx errors → diagnostics.
- **Modify** `internal/codegen/analyze.go` — return ALL `pkg.TypeErrors` (not first `pkg.Errors`), positioned.
- **Modify** `internal/codegen/codegen.go` — mirror the orchestration changes in the single-dir path; thread the bag.
- **Modify** `internal/codegen/emit.go` — thread `*diag.Bag` through `generateFile`→`genComponent`→emit chain; convert `fmt.Errorf("codegen:…")` sites to `bag.Errorf(node.Pos(),node.End(),code,…)`; component-boundary recovery.
- **Modify** `internal/jsx/jsx.go` — emit positioned diagnostics instead of `"… at <token.Pos> …"`.
- **Modify** `gen/cache.go`, `gen/main.go` — collect `Diags`, render (rich on TTY / compact / `--json`), `--json` flag, exit codes; drop the `gsx: <dir>: %v` double-prefix.
- **Modify** `internal/corpus/batch.go`, `internal/corpus/codegen.go` — render diagnostics from `Diags` as `line:col: message`; delete `normalizeDiag`.
- **Modify** `docs/ROADMAP.md`.

---

## Task 1: `internal/diag` package — model + renderers

**Files:**
- Create: `internal/diag/diag.go`, `internal/diag/render.go`
- Test: `internal/diag/diag_test.go`, `internal/diag/render_test.go`

**Interfaces:**
- Consumes: stdlib (`go/token`, `encoding/json`, `fmt`, `io`, `sort`, `bufio`/`bytes`).
- Produces:
  - `type Severity int`; `const ( Error Severity = iota; Warning; Info; Hint )`; `func (Severity) String() string` → `"error"|"warning"|"info"|"hint"`.
  - `type Diagnostic struct { Start, End token.Position; Severity Severity; Code, Message, Help, Source string }`.
  - `type Bag struct {…}`; `func NewBag(fset *token.FileSet) *Bag`; `func (*Bag) Add(Diagnostic)`; `func (*Bag) Report(pos, end token.Pos, sev Severity, code, source, format string, args ...any)`; `func (*Bag) Errorf(pos, end token.Pos, code, format string, args ...any)` (delegates to `Report` with `Error`+`"codegen"`); `func (*Bag) HasErrors() bool`; `func (*Bag) Sorted() []Diagnostic`.
  - `type SourceProvider func(filename string) ([]byte, bool)`.
  - `func RenderCompact(w io.Writer, diags []Diagnostic)`; `func RenderJSON(w io.Writer, diags []Diagnostic) error`; `func RenderRich(w io.Writer, diags []Diagnostic, src SourceProvider)`.

- [ ] **Step 1: Write the failing test (model + compact + json)**

Create `internal/diag/diag_test.go`:

```go
package diag

import (
	"bytes"
	"go/token"
	"strings"
	"testing"
)

func pos(file string, line, col int) token.Position {
	return token.Position{Filename: file, Line: line, Column: col}
}

func TestSeverityString(t *testing.T) {
	for s, want := range map[Severity]string{Error: "error", Warning: "warning", Info: "info", Hint: "hint"} {
		if got := s.String(); got != want {
			t.Errorf("Severity(%d).String() = %q, want %q", s, got, want)
		}
	}
}

func TestBagErrorfResolvesAndHasErrors(t *testing.T) {
	fset := token.NewFileSet()
	f := fset.AddFile("views.gsx", fset.Base(), 100)
	for i := 0; i < 100; i++ {
		if i == 20 || i == 40 {
			f.AddLine(i)
		}
	}
	b := NewBag(fset)
	if b.HasErrors() {
		t.Fatal("new bag should have no errors")
	}
	start := f.Pos(25)
	end := f.Pos(28)
	b.Errorf(start, end, "reserved-param", "param name %q is reserved", "ctx")
	if !b.HasErrors() {
		t.Fatal("bag should report errors after Errorf")
	}
	d := b.Sorted()[0]
	if d.Severity != Error || d.Code != "reserved-param" || d.Source != "" {
		t.Errorf("unexpected diag fields: %+v", d)
	}
	if d.Message != `param name "ctx" is reserved` {
		t.Errorf("message = %q", d.Message)
	}
	if d.Start.Filename != "views.gsx" || d.Start.Line == 0 || d.Start.Column == 0 {
		t.Errorf("start not resolved: %+v", d.Start)
	}
}

func TestSortedByFileLineColumn(t *testing.T) {
	b := &Bag{}
	b.Add(Diagnostic{Start: pos("b.gsx", 1, 1), Severity: Error, Message: "b1"})
	b.Add(Diagnostic{Start: pos("a.gsx", 2, 5), Severity: Error, Message: "a2"})
	b.Add(Diagnostic{Start: pos("a.gsx", 2, 1), Severity: Error, Message: "a1"})
	got := b.Sorted()
	order := []string{got[0].Message, got[1].Message, got[2].Message}
	want := []string{"a1", "a2", "b1"}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("sort order = %v, want %v", order, want)
		}
	}
}

func TestRenderCompact(t *testing.T) {
	var buf bytes.Buffer
	RenderCompact(&buf, []Diagnostic{
		{Start: pos("views.gsx", 3, 13), Severity: Error, Code: "reserved-param", Message: "param name \"ctx\" is reserved", Source: "codegen"},
		{Start: pos("views.gsx", 5, 2), Severity: Error, Message: "no code here", Source: "parser"},
	})
	out := buf.String()
	if !strings.Contains(out, "views.gsx:3:13: error[reserved-param]: param name \"ctx\" is reserved\n") {
		t.Errorf("compact with code wrong:\n%s", out)
	}
	if !strings.Contains(out, "views.gsx:5:2: error: no code here\n") {
		t.Errorf("compact without code wrong:\n%s", out)
	}
}

func TestRenderJSON(t *testing.T) {
	var buf bytes.Buffer
	if err := RenderJSON(&buf, []Diagnostic{
		{Start: pos("views.gsx", 3, 13), End: pos("views.gsx", 3, 16), Severity: Error, Code: "reserved-param", Message: "m", Help: "rename it", Source: "codegen"},
	}); err != nil {
		t.Fatal(err)
	}
	s := buf.String()
	for _, want := range []string{
		`"file":"views.gsx"`, `"start":{"line":3,"col":13}`, `"end":{"line":3,"col":16}`,
		`"severity":"error"`, `"code":"reserved-param"`, `"help":"rename it"`, `"source":"codegen"`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("json missing %s:\n%s", want, s)
		}
	}
	// help/code omitted when empty
	buf.Reset()
	_ = RenderJSON(&buf, []Diagnostic{{Start: pos("a.gsx", 1, 1), Severity: Error, Message: "m"}})
	if strings.Contains(buf.String(), `"help"`) || strings.Contains(buf.String(), `"code"`) {
		t.Errorf("empty help/code must be omitted:\n%s", buf.String())
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/diag/`
Expected: FAIL — package/types undefined (build error).

- [ ] **Step 3: Implement `internal/diag/diag.go`**

```go
// Package diag is gsx's structured-diagnostic foundation: a fileset-agnostic
// Diagnostic model (resolved token.Position ranges, severity, code, help,
// source), a Bag collector for error recovery, and renderers (see render.go).
package diag

import (
	"fmt"
	"go/token"
	"sort"
)

type Severity int

const (
	Error Severity = iota
	Warning
	Info
	Hint
)

func (s Severity) String() string {
	switch s {
	case Error:
		return "error"
	case Warning:
		return "warning"
	case Info:
		return "info"
	case Hint:
		return "hint"
	default:
		return "error"
	}
}

// Diagnostic is one structured problem with already-resolved positions. Start..End
// is a range; End may equal Start for a point. Positions are resolved because
// diagnostics originate from two token.FileSets (gsx parser; go/packages).
type Diagnostic struct {
	Start, End token.Position
	Severity   Severity
	Code       string
	Message    string
	Help       string
	Source     string
}

// Bag accumulates diagnostics for one package's resolve+codegen pass. It holds
// the gsx parser fset only to resolve AST token.Pos in Errorf.
type Bag struct {
	fset  *token.FileSet
	diags []Diagnostic
}

func NewBag(fset *token.FileSet) *Bag { return &Bag{fset: fset} }

// Add appends an already-resolved diagnostic (e.g. a go/types error).
func (b *Bag) Add(d Diagnostic) { b.diags = append(b.diags, d) }

// Report records a diagnostic for an AST node range with an explicit severity
// and source, resolving pos/end through the Bag's fset. end may be token.NoPos
// (then End == Start). Used by non-codegen layers (e.g. jsx) that must set their
// own Source.
func (b *Bag) Report(pos, end token.Pos, sev Severity, code, source, format string, args ...any) {
	d := Diagnostic{
		Severity: sev,
		Code:     code,
		Message:  fmt.Sprintf(format, args...),
		Source:   source,
	}
	if b.fset != nil {
		d.Start = b.fset.Position(pos)
		if end.IsValid() {
			d.End = b.fset.Position(end)
		} else {
			d.End = d.Start
		}
	}
	b.diags = append(b.diags, d)
}

// Errorf is the codegen convenience: an Error-severity, Source:"codegen"
// diagnostic for an AST node range.
func (b *Bag) Errorf(pos, end token.Pos, code, format string, args ...any) {
	b.Report(pos, end, Error, code, "codegen", format, args...)
}

func (b *Bag) HasErrors() bool {
	for _, d := range b.diags {
		if d.Severity == Error {
			return true
		}
	}
	return false
}

// Sorted returns the diagnostics in deterministic filename→line→column order.
func (b *Bag) Sorted() []Diagnostic {
	out := append([]Diagnostic(nil), b.diags...)
	sort.SliceStable(out, func(i, j int) bool {
		a, c := out[i].Start, out[j].Start
		if a.Filename != c.Filename {
			return a.Filename < c.Filename
		}
		if a.Line != c.Line {
			return a.Line < c.Line
		}
		return a.Column < c.Column
	})
	return out
}
```

> Note: codegen uses `Errorf` (`Error`+`"codegen"`). Other gsx-fset layers use `Report` with their own source — jsx uses `Report(pos,end,Error,"<code>","jsx",…)`. Type errors arrive **pre-resolved from go/packages' fset**, so they use `Add` with a constructed `Diagnostic` (Task 2).

- [ ] **Step 4: Implement `internal/diag/render.go`**

```go
package diag

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// SourceProvider yields a file's bytes for snippet rendering (CLI reads disk;
// the LSP supplies the in-memory buffer). A nil provider disables snippets.
type SourceProvider func(filename string) ([]byte, bool)

// header formats "severity[code]: message" (code omitted when empty).
func header(d Diagnostic) string {
	if d.Code != "" {
		return fmt.Sprintf("%s[%s]: %s", d.Severity, d.Code, d.Message)
	}
	return fmt.Sprintf("%s: %s", d.Severity, d.Message)
}

// RenderCompact writes one deterministic line per diagnostic:
// file:line:col: severity[code]: message
func RenderCompact(w io.Writer, diags []Diagnostic) {
	for _, d := range diags {
		fmt.Fprintf(w, "%s:%d:%d: %s\n", d.Start.Filename, d.Start.Line, d.Start.Column, header(d))
	}
}

type jsonPos struct {
	Line int `json:"line"`
	Col  int `json:"col"`
}
type jsonRange struct {
	Start jsonPos `json:"start"`
	End   jsonPos `json:"end"`
}
type jsonDiag struct {
	File     string    `json:"file"`
	Range    jsonRange `json:"range"`
	Severity string    `json:"severity"`
	Code     string    `json:"code,omitempty"`
	Message  string    `json:"message"`
	Help     string    `json:"help,omitempty"`
	Source   string    `json:"source,omitempty"`
}

// RenderJSON writes the diagnostics as a JSON array (1-based positions).
func RenderJSON(w io.Writer, diags []Diagnostic) error {
	out := make([]jsonDiag, 0, len(diags))
	for _, d := range diags {
		out = append(out, jsonDiag{
			File:     d.Start.Filename,
			Range:    jsonRange{jsonPos{d.Start.Line, d.Start.Column}, jsonPos{d.End.Line, d.End.Column}},
			Severity: d.Severity.String(),
			Code:     d.Code,
			Message:  d.Message,
			Help:     d.Help,
			Source:   d.Source,
		})
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}

// RenderRich writes rustc/Go-style diagnostics with a source snippet + caret.
// It degrades to the compact line when src yields no source for the file.
func RenderRich(w io.Writer, diags []Diagnostic, src SourceProvider) {
	for _, d := range diags {
		line, ok := sourceLine(src, d.Start.Filename, d.Start.Line)
		if !ok {
			fmt.Fprintf(w, "%s:%d:%d: %s\n", d.Start.Filename, d.Start.Line, d.Start.Column, header(d))
			continue
		}
		fmt.Fprintf(w, "%s\n", header(d))
		fmt.Fprintf(w, "  --> %s:%d:%d\n", d.Start.Filename, d.Start.Line, d.Start.Column)
		gutter := fmt.Sprintf("%d", d.Start.Line)
		pad := strings.Repeat(" ", len(gutter))
		fmt.Fprintf(w, " %s |\n", pad)
		fmt.Fprintf(w, " %s | %s\n", gutter, line)
		fmt.Fprintf(w, " %s | %s%s\n", pad, strings.Repeat(" ", caretIndent(d.Start.Column)), carets(d))
		if d.Help != "" {
			fmt.Fprintf(w, " %s = help: %s\n", pad, d.Help)
		}
		fmt.Fprintln(w)
	}
}

// caretIndent converts a 1-based byte column to the leading-space count.
func caretIndent(col int) int {
	if col <= 1 {
		return 0
	}
	return col - 1
}

// carets returns the underline: one '^' per byte column in the range on the
// start line (at least one), capped so a multi-line range underlines to EOL of
// the start line via the caller's source length is left simple here: single-line.
func carets(d Diagnostic) string {
	n := 1
	if d.End.Line == d.Start.Line && d.End.Column > d.Start.Column {
		n = d.End.Column - d.Start.Column
	}
	return strings.Repeat("^", n)
}

// sourceLine returns the 1-based line lineNo of filename's source, without the
// trailing newline.
func sourceLine(src SourceProvider, filename string, lineNo int) (string, bool) {
	if src == nil || lineNo < 1 {
		return "", false
	}
	b, ok := src(filename)
	if !ok {
		return "", false
	}
	lines := strings.Split(string(b), "\n")
	if lineNo > len(lines) {
		return "", false
	}
	return strings.TrimRight(lines[lineNo-1], "\r"), true
}
```

- [ ] **Step 5: Write the rich renderer test**

Create `internal/diag/render_test.go`:

```go
package diag

import (
	"bytes"
	"go/token"
	"strings"
	"testing"
)

func TestRenderRichSnippet(t *testing.T) {
	src := func(name string) ([]byte, bool) {
		if name == "views.gsx" {
			return []byte("package p\n\ncomponent X(ctx string) {\n}\n"), true
		}
		return nil, false
	}
	var buf bytes.Buffer
	RenderRich(&buf, []Diagnostic{{
		Start: token.Position{Filename: "views.gsx", Line: 3, Column: 13},
		End:   token.Position{Filename: "views.gsx", Line: 3, Column: 16},
		Severity: Error, Code: "reserved-param",
		Message: `param name "ctx" is reserved`, Help: "rename the parameter",
	}}, src)
	out := buf.String()
	for _, want := range []string{
		`error[reserved-param]: param name "ctx" is reserved`,
		`--> views.gsx:3:13`,
		`component X(ctx string) {`,
		`^^^`,
		`= help: rename the parameter`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("rich output missing %q:\n%s", want, out)
		}
	}
}

func TestRenderRichDegradesWithoutSource(t *testing.T) {
	var buf bytes.Buffer
	RenderRich(&buf, []Diagnostic{{
		Start: token.Position{Filename: "x.gsx", Line: 2, Column: 4}, Severity: Error, Message: "m",
	}}, nil)
	if !strings.Contains(buf.String(), "x.gsx:2:4: error: m") {
		t.Errorf("expected compact degradation, got:\n%s", buf.String())
	}
}
```

- [ ] **Step 6: Run tests to verify they pass**

Run: `go test ./internal/diag/`
Expected: PASS (all model + render tests). Fix the caret-indent / snippet rendering until `TestRenderRichSnippet` passes (the caret must sit under `ctx`).

- [ ] **Step 7: Commit**

```bash
git add internal/diag/
git commit -m "feat(diag): structured Diagnostic model + Bag + rich/compact/JSON renderers"
```

---

## Task 2: Wire `Bag` through orchestration; surface all type errors + positioned jsx

**Files:**
- Modify: `internal/codegen/batch.go` (`PackageResult.Diags`; per-package `Bag`; parse/jsx/type errors → diagnostics)
- Modify: `internal/codegen/analyze.go` (return ALL `pkg.TypeErrors`, positioned)
- Modify: `internal/codegen/codegen.go` (single-dir path: same wiring)
- Modify: `internal/jsx/jsx.go` (positioned diagnostics)
- Modify: `internal/corpus/batch.go`, `internal/corpus/codegen.go` (render diagnostics from `Diags`; delete `normalizeDiag`)
- Test: `internal/codegen/diag_wire_test.go` (new)

**Interfaces:**
- Consumes: `diag.NewBag`, `diag.Bag.Add/Errorf/Sorted/HasErrors`, `diag.Diagnostic`, `diag.Error` (Task 1).
- Produces:
  - `PackageResult` gains `Diags []diag.Diagnostic` (alongside the existing `Files`, `Err`).
  - A jsx entry that reports into a bag: `func ResolveScripts(f *ast.File, bag *diag.Bag) bool` (returns false and records diagnostics on failure; replaces the `error`-returning form). The single returned-error form is removed; callers pass the package bag.
  - `analyze`'s resolve step records **all** `pkg.TypeErrors` into the package bag (Source `"types"`), positioned via each `types.Error`'s own fset (`e.Fset.Position(e.Pos)`), instead of returning on `pkg.Errors[0]`.

> **Transition rule (keep the suite green):** in this task, also set `PackageResult.Err` to a non-nil sentinel (`errors.New("codegen: diagnostics reported")`) whenever the bag `HasErrors()`, so existing non-corpus callers that branch on `Err != nil` keep working until Task 4 switches them to `Diags`. The corpus harness is migrated to `Diags` in this task (below).

- [ ] **Step 1: Write the failing test**

Create `internal/codegen/diag_wire_test.go`:

```go
package codegen

import (
	"os"
	"path/filepath"
	"testing"
)

// A file with TWO independent type errors must report BOTH (not just the first).
func TestAllTypeErrorsReported(t *testing.T) {
	dir := t.TempDir()
	// undefinedA and undefinedB are both unresolved identifiers -> two type errors.
	src := `package views

component X() {
	<div>{ undefinedA }</div>
	<span>{ undefinedB }</span>
}
`
	if err := os.WriteFile(filepath.Join(dir, "views.gsx"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := GeneratePackages(dir, []string{dir})
	if err != nil {
		t.Fatalf("GeneratePackages returned hard error: %v", err)
	}
	pr := out[mustAbs(t, dir)]
	if pr == nil {
		t.Fatal("no PackageResult for dir")
	}
	var msgs string
	for _, d := range pr.Diags {
		msgs += d.Message + "\n"
	}
	if !contains(msgsString(pr), "undefinedA") || !contains(msgsString(pr), "undefinedB") {
		t.Errorf("expected BOTH type errors, got diagnostics:\n%s", msgsString(pr))
	}
}

func msgsString(pr *PackageResult) string {
	var s string
	for _, d := range pr.Diags {
		s += d.Message + "\n"
	}
	return s
}
func contains(hay, needle string) bool { return len(hay) > 0 && (len(needle) == 0 || (indexOf(hay, needle) >= 0)) }
func indexOf(h, n string) int {
	for i := 0; i+len(n) <= len(h); i++ {
		if h[i:i+len(n)] == n {
			return i
		}
	}
	return -1
}
func mustAbs(t *testing.T, p string) string {
	t.Helper()
	a, err := filepath.Abs(p)
	if err != nil {
		t.Fatal(err)
	}
	return a
}
```

> If `GeneratePackages` keys its result map differently (it keys by absolute dir — see `batch.go`), `mustAbs` matches it. Adjust the lookup if the harness reveals a different key.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/codegen/ -run TestAllTypeErrorsReported`
Expected: FAIL — `PackageResult` has no `Diags`, and only the first type error is surfaced.

- [ ] **Step 3: Add `Diags` to `PackageResult` and a per-package `Bag`**

In `internal/codegen/batch.go`, extend the struct (around line 21):

```go
type PackageResult struct {
	Files map[string][]byte
	Diags []diag.Diagnostic // all diagnostics collected for this package (Slice 1: types + codegen + jsx + parser)
	Err   error             // transition sentinel: non-nil if any Error-severity diagnostic (until consumers read Diags)
}
```

Add `"github.com/gsxhq/gsx/internal/diag"` to imports. Create one `bag := diag.NewBag(fset)` per package (the parse loop already has the shared `fset`). Where parse errors are recorded today (`result[dir].Err = parseErr`), instead `bag.Add(diag.Diagnostic{Start: <resolved cursor pos>, Severity: diag.Error, Message: <parser error text>, Source: "parser"})` — the parser error string already begins `line:col:`; for Slice 1 wrap it as a single diagnostic (full parser-position fidelity is Slice 2; if the parser error carries no structured pos, store the message with a best-effort Start from the file). At the end of processing each package, set `result[dir].Diags = bag.Sorted()` and `if bag.HasErrors() { result[dir].Err = errors.New("codegen: diagnostics reported") }`.

- [ ] **Step 4: Surface ALL type errors (analyze.go)**

In `internal/codegen/analyze.go`, replace the first-error bail (lines ~89-90):

```go
if len(pkg.Errors) > 0 {
	return nil, nil, fmt.Errorf("codegen: type resolution failed: %s", pkg.Errors[0])
}
```

with collection of **all** type errors into the bag. Thread the package `*diag.Bag` into the resolve entry point. Prefer `pkg.TypeErrors` (`[]types.Error`, each with `.Pos token.Pos` and `.Fset`) so positions map through the generated `//line` directives back to `.gsx`:

```go
for _, e := range pkg.TypeErrors {
	p := e.Fset.Position(e.Pos) // resolves via //line back to .gsx
	bag.Add(diag.Diagnostic{Start: p, End: p, Severity: diag.Error, Message: e.Msg, Source: "types"})
}
// also fold in non-type pkg.Errors that lack a TypeError (load/list errors) as
// positionless or best-effort-positioned diagnostics.
if bag.HasErrors() {
	return nil, nil, errTypeResolution // sentinel; caller already has the bag
}
```

(Define a package-level `var errTypeResolution = errors.New("codegen: type resolution failed")` used only to signal "stop, don't emit"; the real detail lives in the bag.) Ensure the load config requests type info so `pkg.TypeErrors` is populated (it already loads types — verify `packages.NeedTypes|NeedTypesInfo` is set; add `NeedTypesInfo` if missing).

- [ ] **Step 5: Positioned jsx diagnostics (jsx.go)**

Change `internal/jsx.ResolveScripts` to take the bag and record positioned diagnostics:

```go
// ResolveScripts classifies <script>/data-island @{ } holes. It records any
// failure as a positioned diagnostic in bag and returns false; true if clean.
func ResolveScripts(f *ast.File, bag *diag.Bag) bool { … }
```

At the three former `fmt.Errorf("jsx: … at %d …", el.Pos()/h.interp.Pos())` sites, call `bag.Errorf(node.Pos(), node.End(), "js-context", "jsx: …")` (drop the raw `%d` offset — the position now lives in the diagnostic). Update both call sites (`batch.go:89`, `codegen.go:75`) to pass the bag and branch on the bool. (`internal/jsx` may import `internal/diag` — both are leaf-ish; confirm no cycle: `diag` imports only stdlib, so this is safe.)

- [ ] **Step 6: Migrate the corpus harness to `Diags`**

In `internal/corpus/batch.go` (lines ~117-118) and `internal/corpus/codegen.go` (line ~46), stop reading `pr.Err.Error()`. Instead render the package's diagnostics with a small local formatter producing the existing golden projection `line:col: message` (one per line), using `pr.Diags`:

```go
for _, d := range pr.Diags {
	fmt.Fprintf(&diagBuf, "%d:%d: %s\n", d.Start.Line, d.Start.Column, d.Message)
}
```

Keep `normalizeDiagPaths` (it strips the temp dir from any embedded paths). **Delete** `normalizeDiag` and its `diagOffsetRe` regex in `internal/corpus/corpus_test.go` (no more `"at N"`). Run the corpus with `-update` to rebaseline jsx golden cases (now real `line:col`) and any case whose codegen diagnostic ordering changed; review the diff to confirm positions are sane. (Codegen diagnostics are still positionless until Task 3 — their golden lines will be `0:0: codegen: …` for now; that is corrected in Task 3 when positions land. To avoid a churn-then-rechurn, prefer running `-update` for goldens at the END of Task 3; in Task 2 only the jsx goldens and the harness change need rebaselining — scope the `-update` to jsx cases by reviewing the diff.)

> Decision to avoid double-rebaseline: in Task 2, codegen-diagnostic goldens may temporarily show `0:0:` positions. If that is noisy, keep `pr.Err`-based rendering for codegen-only cases by formatting `0:0:`-position diagnostics as bare `message` (matching today's `codegen: …`). Simplest: format a diagnostic with `Start.Line == 0` as just `message` (no `line:col:` prefix) — so positionless codegen diagnostics render exactly as today, and only jsx/positioned ones gain `line:col`. This keeps Task 2's corpus churn limited to jsx.

- [ ] **Step 7: Run tests**

Run: `go test ./internal/codegen/ -run TestAllTypeErrorsReported -v` then `go test ./...`
Expected: the new test PASSES (both type errors reported); corpus green after the scoped jsx rebaseline.

- [ ] **Step 8: Commit**

```bash
git add internal/codegen/ internal/jsx/ internal/corpus/
git commit -m "feat(codegen): collect diagnostics into a per-package diag.Bag; surface all type errors; positioned jsx"
```

---

## Task 3: Codegen emit-site migration + component-boundary recovery

**Files:**
- Modify: `internal/codegen/emit.go` (thread `*diag.Bag` through `generateFile`→`genComponent`→emit chain; convert error sites; component recovery)
- Modify: `internal/codegen/batch.go`, `internal/codegen/codegen.go` (pass the bag into `generateFile`)
- Reference: `docs/superpowers/specs/codegen-diagnostic-position-audit.md` (the 55-site worklist with per-site node mapping)
- Test: `internal/codegen/diag_recovery_test.go` (new)

**Interfaces:**
- Consumes: `diag.Bag` (Task 1); `PackageResult.Diags` wiring (Task 2).
- Produces:
  - `generateFile(file *ast.File, resolved map[ast.Node]types.Type, table filterTable, structFields map[string]map[string]bool, fset *token.FileSet, cls *attrclass.Classifier, bag *diag.Bag, cssMin, jsMin func(string) (string, error)) ([]byte, bool)` — returns the generated bytes and an `ok` bool (false if any component failed). Diagnostics go to `bag`. (Replaces the `([]byte, error)` form.)
  - Emit helpers (`genComponent`, `emitRootElement`, `genNode`, `emitAttr`, `emitExprAttr`, `emitCSSInterp`, …) gain a `bag *diag.Bag` parameter and call `bag.Errorf(n.Pos(), n.End(), code, …)` instead of returning `fmt.Errorf("codegen:…")`.

- [ ] **Step 1: Write the failing test**

Create `internal/codegen/diag_recovery_test.go`:

```go
package codegen

import (
	"os"
	"path/filepath"
	"testing"
)

// Two components each with a distinct codegen error must BOTH be reported
// (component-boundary recovery), and each diagnostic must carry a .gsx position.
func TestComponentRecoveryReportsAllPositioned(t *testing.T) {
	dir := t.TempDir()
	// Two reserved-param errors in two components (a codegen-layer check, not types).
	src := `package views

component A(ctx string) {
	<div></div>
}

component B(children string) {
	<div></div>
}
`
	if err := os.WriteFile(filepath.Join(dir, "v.gsx"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := GeneratePackages(dir, []string{dir})
	if err != nil {
		t.Fatalf("hard error: %v", err)
	}
	pr := out[mustAbs(t, dir)]
	var lines int
	var positioned bool
	for _, d := range pr.Diags {
		if d.Source == "codegen" {
			lines++
			if d.Start.Line > 0 && d.Start.Column > 0 {
				positioned = true
			}
		}
	}
	if lines < 2 {
		t.Errorf("expected >=2 codegen diagnostics (one per component), got %d", lines)
	}
	if !positioned {
		t.Errorf("codegen diagnostics must carry .gsx positions")
	}
}
```

> `ctx` and `children` are reserved param names (see `codegen-diagnostic-position-audit.md` analyze.go:1167/1170). If those specific reservations changed, pick two currently-reserved names or two distinct codegen errors from the audit table — the test's point is *two components, both reported, both positioned*.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/codegen/ -run TestComponentRecoveryReportsAllPositioned`
Expected: FAIL — only the first component's error surfaces, and/or it is positionless.

- [ ] **Step 3: Thread the bag + recover at the component boundary**

In `internal/codegen/emit.go`, change `generateFile` to take `bag *diag.Bag` and return `([]byte, bool)`. Restructure the component loop for recovery — emit each component into a **temp buffer**, append only on success:

```go
case *ast.Component:
	var cbuf bytes.Buffer
	if genComponent(&cbuf, v, resolved, table, structFields, imports, fset, cls, bag) {
		body.Write(cbuf.Bytes())
	}
	// on failure: diagnostics already recorded in bag; skip this component, continue.
```

`genComponent` (and the helpers it calls) return `bool` (`true` = clean) instead of `error`, and record diagnostics via `bag`. At the end, `generateFile` returns `(body bytes, !bag.HasErrors())` — but note the bag spans the whole package, so prefer a per-call "did THIS file error" check: snapshot `len(bag.Sorted())` before/after, or have `genComponent` return its own ok and AND them.

> Recovery boundary is the **component**: within a component, the first error still unwinds that component (helpers return `false` up the chain). This delivers "report all errors across components" without per-node partial-buffer recovery (deferred). Document this in a comment.

- [ ] **Step 4: Convert every codegen error site (follow the audit)**

For each site in `codegen-diagnostic-position-audit.md`, replace `return fmt.Errorf("codegen: …", …)` with `bag.Errorf(node.Pos(), node.End(), "<code>", "<same message>", …); return false` using the **"node for position"** column from the audit table. Representative conversions:

`emit.go` (genNode, ~line 554):
```go
// before:
return fmt.Errorf("codegen: could not resolve type of pipeline %q", n.Expr)
// after (n is *ast.Interp):
bag.Errorf(n.Pos(), n.End(), "unresolved-pipeline", "could not resolve type of pipeline %q", n.Expr)
return false
```

`emit.go` (emitExprAttr, ~line 835 JS-context):
```go
bag.Errorf(a.Pos(), a.End(), "unsafe-js-context", "expr value in JS/event-handler context (%q) is unsafe; …", a.Name)
return false
```

`analyze.go` (reserved param, ~line 1167) — `c` is the `*ast.Component`:
```go
bag.Errorf(c.Pos(), c.End(), "reserved-param", "param name %q is reserved (ambient context)", name)
// return the appropriate zero values + signal failure for this component
```

Apply the audit's "node for position" for the package-level sites that have **no** AST node (`analyze.go:83/86/90` load/list errors): record them positionless via `bag.Add(diag.Diagnostic{Severity:diag.Error, Message:…, Source:"codegen"})` (Start zero → renders without `line:col`, per Task 2's positionless formatting). Drop the `codegen:` prefix from messages (the renderer adds `error[code]:`), OR keep it — choose once and apply uniformly; **recommended: drop `codegen:`** since `Source:"codegen"` + `severity[code]:` already convey origin. Keep the message wording otherwise identical so goldens diff cleanly.

- [ ] **Step 5: Update callers + rebaseline**

Update `batch.go` and `codegen.go` to call the new `generateFile(..., bag, ...)` and stop treating its result as `error`. Then rebaseline: `go test ./internal/corpus/ -update` (or the repo's update flag), and **review the diff** — codegen `diagnostics.golden` now carry `line:col` and may list multiple lines; confirm positions point at the right `.gsx` construct. Add a new corpus case with two errors in two components proving recovery (if not already covered by the unit test).

- [ ] **Step 6: Run tests**

Run: `go test ./internal/codegen/ -run TestComponentRecovery -v` then `go test ./...`
Expected: recovery test PASSES; full suite green with rebaselined goldens.

- [ ] **Step 7: Commit**

```bash
git add internal/codegen/ internal/corpus/
git commit -m "feat(codegen): positioned diagnostics + component-boundary recovery (report all errors)"
```

---

## Task 4: CLI rendering — rich/compact/JSON, `--json`, exit codes

**Files:**
- Modify: `gen/cache.go` (collect `PackageResult.Diags`; carry them out via `Result`)
- Modify: `gen/main.go` (`generate` parses `--json`; render via `internal/diag`; exit code; drop double-prefix)
- Test: `gen/diag_render_test.go` (new) + 1 JSON-shape corpus or gen golden

**Interfaces:**
- Consumes: `diag.RenderRich/RenderCompact/RenderJSON`, `diag.SourceProvider`, `PackageResult.Diags`.
- Produces: `gsx generate [--json]` rendering; non-zero exit iff any `Error` diagnostic.

- [ ] **Step 1: Write the failing test**

Create `gen/diag_render_test.go` — drive `runGenerate` on a temp dir with a known error and assert (a) default output contains `error[` + the file position, (b) `--json` output parses as a JSON array containing the code, (c) exit code non-zero.

```go
package gen

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func writeGSX(t *testing.T, dir, name, src string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestGenerateJSONDiagnostics(t *testing.T) {
	dir := t.TempDir()
	writeGSX(t, dir, "go.mod", "") // ensure module context as the harness expects; adjust to real setup
	writeGSX(t, dir, "v.gsx", "package views\n\ncomponent A(ctx string) {\n\t<div></div>\n}\n")
	var out, errb bytes.Buffer
	code := runGenerate([]string{dir}, &out, &errb, false, false, true /*noCache*/, nil, nil, "", nil, nil)
	if code == 0 {
		t.Fatalf("expected non-zero exit on diagnostic; out=%s err=%s", out.String(), errb.String())
	}
	// --json path:
	out.Reset(); errb.Reset()
	codeJSON := runGenerateJSON(t, dir) // helper that sets the --json flag through the dispatch
	_ = codeJSON
	var arr []map[string]any
	if err := json.Unmarshal(jsonOutputOf(t, dir), &arr); err != nil {
		t.Fatalf("json: %v", err)
	}
	if len(arr) == 0 || arr[0]["code"] != "reserved-param" {
		t.Errorf("expected reserved-param diagnostic in json, got %v", arr)
	}
}
```

> The exact `runGenerate` signature gains `cls`+`predLabel` from prior CLI work and now a way to select JSON; thread the `--json` bool through the dispatch (Step 3). Adjust the test helpers to the real signature once Step 3 fixes it. The assertions (non-zero exit; JSON array with `code`) are the contract.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./gen/ -run TestGenerateJSONDiagnostics`
Expected: FAIL — no `--json`, diagnostics not rendered structurally.

- [ ] **Step 3: Carry `Diags` out and render**

In `gen/cache.go`, collect each `PackageResult.Diags` into the `Result` (add a `Diags []diag.Diagnostic` field to `Result`; append per package). In `gen/main.go` `generate` dispatch: parse a `--json` flag; build a `diag.SourceProvider` that reads files from disk (`os.ReadFile`); then:

```go
diags := res.Diags // already sorted per-package; re-sort the merged set
sort.SliceStable(diags, /* filename→line→col */)
switch {
case jsonFlag:
	_ = diag.RenderJSON(stdout, diags)
case isTTY(stderr):
	diag.RenderRich(stderr, diags, srcProvider)
default:
	diag.RenderCompact(stderr, diags)
}
if anyError(diags) { return 1 }
```

`isTTY` = check `term.IsTerminal(fd)` via `golang.org/x/term` if already a dep, else a small `os.Stat`/`ModeCharDevice` check on the file. Drop the `for _, e := range res.Errs { fmt.Fprintf(stderr, "gsx: %v\n", e) }` block; keep `gsx:`-prefixed output only for operational (non-diagnostic) failures (I/O, bad args).

- [ ] **Step 4: Run tests + manual smoke**

Run: `go test ./gen/ ./...`
Manual: in a temp dir with a bad `.gsx`, `go run ./cmd/gsx generate .` shows a rich diagnostic; `go run ./cmd/gsx generate . --json` shows a JSON array; exit codes correct (`echo $status`).

- [ ] **Step 5: Add a JSON-shape golden**

Add one corpus (or gen) golden capturing the `--json` output for a representative error case (pins `range.start/end`, `severity`, `code`, `source`). Commit it.

- [ ] **Step 6: Commit**

```bash
git add gen/ internal/corpus/
git commit -m "feat(gen): render diagnostics (rich/compact/--json), exit codes; drop gsx: double-prefix"
```

---

## Task 5: Docs

**Files:**
- Modify: `docs/ROADMAP.md`

- [ ] **Step 1: Update ROADMAP**

In the CLI / `gen.Main` row, move `--json`/`diag` from pending to done; note: structured diagnostics (`internal/diag`) with rich/compact/JSON rendering, all-`go/types`-errors + component-boundary codegen recovery, `gsx generate --json`. Reference the spec/plan filenames. Add a line noting **parser error recovery is Slice 2 (pending)**. Note the `normalizeDiag` hack removal and that codegen diagnostics now carry `.gsx` positions (closes the `codegen-diagnostic-position-audit.md` gap).

- [ ] **Step 2: Commit**

```bash
git add docs/
git commit -m "docs: diagnostics foundation (internal/diag) — rich/--json, semantic recovery; parser recovery deferred"
```

---

## Self-Review

**Spec coverage:**
- §3 model (resolved `token.Position`, severity, code, help, source) → Task 1 ✓
- §4 `Bag` (Add/Errorf/HasErrors/Sorted) → Task 1 ✓
- §5 semantic recovery (all go/types errors; codegen accumulate+continue per component; per-package all-or-nothing write) → Tasks 2 (types) + 3 (codegen recovery) ✓ (write-safety preserved: package emits nothing when `HasErrors`)
- §6 migration (codegen gains positions per audit; jsx real positions; parser wrapped) → Tasks 3 (codegen) + 2 (jsx, parser-wrap) ✓
- §7 three renderers + SourceProvider + selection + exit codes → Task 1 (renderers) + Task 4 (selection/TTY/--json/exit) ✓
- §9 corpus (line:col projection; delete normalizeDiag; JSON-shape goldens; multi-error fixture) → Tasks 2 (harness+jsx rebaseline, delete normalizeDiag) + 3 (codegen rebaseline + multi-error) + 4 (JSON golden) ✓
- §10 testing → unit (Task 1), recovery/all-errors (Tasks 2-3), CLI/json (Task 4) ✓
- §11 LSP-readiness — properties realized by the model in Task 1 ✓

**Placeholder scan:** No "TBD"/"add error handling"/"similar to Task N". The audit-driven Step 3.4 cites the checked-in 55-site table rather than repeating 55 near-identical conversions — it shows the exact pattern + 3 representative conversions; the table is the per-site spec (legitimate, not a placeholder). Test helpers in Tasks 2/4 note where signatures must be matched to the real tree (the assertions are concrete).

**Type consistency:** `*diag.Bag`, `diag.Diagnostic{Start,End token.Position,…}`, `PackageResult.Diags []diag.Diagnostic`, `Result.Diags`, renderer signatures `Render*(w, diags[, src])`, `generateFile(...,bag,...) ([]byte,bool)`, `ResolveScripts(f, bag) bool` — used consistently across tasks. The `Source` field is set correctly per layer because Task 1 exposes **`Report(pos,end,sev,code,source,format,…)`** (explicit source) with `Errorf` as the `Error`+`"codegen"` convenience delegating to it: codegen → `Errorf`; jsx → `Report(...,"jsx",...)`; type errors → `Add` (pre-resolved from go/packages' fset). This was a gap caught in self-review and folded into Task 1 Step 3.
