# Value-form `if`/`switch` in `class`/`style` — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a value-producing `if`/`switch` expression form inside `class={…}` / `style={…}` composed lists, so exclusive class selection replaces the additive-map negation-default antipattern; fold in the class-map comma-wrap formatter fix and `(T,error)` auto-unwrap for both arms and plain parts.

**Architecture:** A new AST node (`ValueIf`/`ValueSwitch`/`ValueSwitchCase`/`ValueArm`) hangs off a `ClassPart.CF` field. The parser detects a segment beginning with `if`/`switch` inside a composed value and parses it (reusing `scanToBlockBrace`/`scanToCaseColon`), arms being single Go value expressions. Codegen lowers the value-form to a hoisted `_gsxvN` temp assigned by a generated Go `if`/`switch`, feeding the existing `gsx.Class(...)`/`gsx.StyleString(...)` part machinery. Arm and plain-part value expressions are type-harvested (mirroring `OrderedPair`) so the existing `tupleUnwrapType`+`hoistTuple` `(T,error)` unwrap applies uniformly. The printer gains a breakable comma between class parts and a multi-line layout for the value-form arms.

**Tech Stack:** Go; `golang.org/x/tools` for tooling; internal `pretty` printer; txtar corpus harness; sibling repos `tree-sitter-gsx` (JS grammar + C scanner) and `vscode-gsx` (TextMate YAML).

## Global Constraints

- Runtime (root package) stays **standard-library only**; codegen/parser/printer live under `internal/` and `parser/`, may use `golang.org/x/tools`.
- Pin Go to `GO_VERSION` in `.github/workflows/ci.yml` (currently **1.26.1**) — a different minor reintroduces gofmt drift.
- **Don't hand-edit `.x.go` or `*.golden`** — regenerate via `go test ./internal/corpus -run TestCorpus -update`, then verify without `-update`. Committing a new corpus case also requires the regenerated `internal/corpus/testdata/coverage.golden`.
- **Every syntax/codegen change ships a corpus case**; new syntax valid in multiple contexts needs a case **per context** (here: `class` and `style`).
- Run `make check` in the inner loop; `make ci` (uncached, `-count=1`) before merge.
- **No "simple heuristics"** in core logic — real implementations only.
- Feature work happens in a **git worktree** (use `superpowers:using-git-worktrees`); the plan doc itself lives on `main`.
- Any syntax change ships docs + `../tree-sitter-gsx` + `../vscode-gsx` updates (Tasks 9–11).
- Reuse the merged `(T,error)` helpers — `tupleUnwrapType(t types.Type) (types.Type, bool)` and `hoistTuple(b *bytes.Buffer, expr string, interpTemp *int) string` (`internal/codegen/emit.go:956-973`). Do not re-implement the hoist.

---

## Task 1: Formatter — make the class-map comma a break point

Self-contained, ships the original badge complaint independent of the new syntax. The `ClassAttr` part loop currently joins with a hard `pretty.Text(", ")` (no break candidate), so an overflowing composed list dumps every entry on one line.

**Files:**
- Modify: `internal/printer/printer.go:263-278` (the `*ast.ClassAttr` case in `attrDoc`)
- Test: `internal/printer/printer_test.go` (new `TestClassMapWraps`)

**Interfaces:**
- Consumes: `pretty.Group`, `pretty.Concat`, `pretty.Text`, `pretty.Line`, `pretty.Indent` (`internal/pretty/doc.go`); `wrapAttrValue(name string, sep, value pretty.Doc) pretty.Doc` (`printer.go:289`).
- Produces: no new exported API; behavioral change only.

- [ ] **Step 1: Write the failing test**

Add to `internal/printer/printer_test.go`:

```go
func TestClassMapWraps(t *testing.T) {
	// A composed class map wider than 80 cols must break one entry per line,
	// not weld every entry onto one indented line.
	src := `package p
component C(v int) {
	<span class={ "base-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "green-bbbbbbbbbbbbbbbbbbbbbbbb": v == 1, "gray-cccccccccccccccccccccccc": v != 1 }>x</span>
}`
	want := `package p

component C(v int) {
	<span
		class={
			"base-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			"green-bbbbbbbbbbbbbbbbbbbbbbbb": v == 1,
			"gray-cccccccccccccccccccccccc": v != 1,
		}
	>
		x
	</span>
}
`
	assertFormat(t, src, want)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/printer -run TestClassMapWraps -v`
Expected: FAIL — output has all three entries on one line (the `", "` joiner has no break).

- [ ] **Step 3: Make the comma a break point**

In `internal/printer/printer.go`, the `*ast.ClassAttr` case currently builds `parts` joined by `pretty.Text(", ")` and wraps with `wrapAttrValue(v.Name, pretty.Line, pretty.Concat(parts...))`. Replace the joiner so each separator is a breakable `Line` (flat → `", "`, broken → `","`+newline), and group the whole list so it collapses when it fits:

```go
	case *ast.ClassAttr:
		parts := make([]pretty.Doc, 0, len(v.Parts)*2)
		for i, part := range v.Parts {
			if i > 0 {
				parts = append(parts, pretty.Text(","), pretty.Line)
			}
			parts = append(parts, p.classPartDoc(part))
		}
		return wrapAttrValue(v.Name, pretty.Line, pretty.Group(pretty.Concat(parts...)))
```

Extract the per-part rendering (seed + stages + guard) into a helper so Task 3 can reuse it:

```go
// classPartDoc renders one composed class/style contribution: `expr`,
// `expr |> stage`, or `expr: cond`. (Value-form parts are handled in Task 3.)
func (p *printer) classPartDoc(part ast.ClassPart) pretty.Doc {
	seg := []pretty.Doc{fmtExprDoc(part.Expr)}
	for _, s := range part.Stages {
		seg = append(seg, pretty.Text(" |> "), pretty.Text(pipeStageStr(s)))
	}
	if part.Cond != "" {
		seg = append(seg, pretty.Text(": "), pretty.Text(fmtExpr(part.Cond)))
	}
	return pretty.Concat(seg...)
}
```

Note: `pretty.Line` flat-renders as a single space, so `","`+`Line` flat = `", "` (unchanged short-form). Broken = `","`+newline+indent → trailing comma per line, matching gofmt-style composite literals. The `want` above includes the trailing comma on the last entry because the broken `Line` is *between* entries and `wrapAttrValue`'s closing `sep` adds the final newline — verify the exact golden in Step 4 and adjust `want` to the real output (do not hand-fudge; if the printer emits no trailing comma on the last item, update `want`).

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/printer -run TestClassMapWraps -v`
Expected: PASS. If the only diff is a trailing comma on the last entry, correct `want` to match actual printer output (the printer is the source of truth here).

- [ ] **Step 5: Run the full printer suite (catch idempotence/faithfulness regressions)**

Run: `go test ./internal/printer -count=1`
Expected: PASS — existing class tests (`TestAttrKinds`, `TestDSFormMessageAcceptance`) still pass; short class maps still collapse to one line.

- [ ] **Step 6: Commit**

```bash
git add internal/printer/printer.go internal/printer/printer_test.go
git commit -m "fix(printer): break composed class/style map one entry per line on overflow"
```

---

## Task 2: AST + parser — parse value-form `if`/`switch` in class/style

Adds the nodes and the parser branch. Tested at the parser layer via a corpus `ast.golden` case (no codegen yet).

**Files:**
- Modify: `ast/ast.go` (new node types; `ClassPart.CF` field; `SetSpan`; `Inspect`)
- Modify: `ast/print.go` (dump the new nodes)
- Modify: `parser/attrs.go` (`splitComposed` → method; value-form branch)
- Create: `parser/valueform.go` (`parseValueIf`, `parseValueSwitch`, `parseValueArm`)
- Create: `internal/corpus/testdata/cases/control_flow/value_switch_ast.txtar`

**Interfaces:**
- Consumes: `scanToBlockBrace(src string, from int, keyword string) (int, bool)` (`parser/boundary.go:40`), `scanToCaseColon(src string, from int) (int, bool)` (`parser/boundary.go:88`), `goExprEnd(src string, open int) (int, bool)` (`parser/boundary.go:10`), `parsePipe(src string, base token.Pos) (string, []ast.PipeStage, error)` (`parser/pipe.go:180`), `(*parser).posAt(int) token.Pos`, `(*parser).errorf`.
- Produces (later tasks rely on these exact names/fields):
  - `ast.ValueArm{ span; Expr string; Stages []ast.PipeStage }`
  - `ast.ValueIf{ span; Cond string; CondPos token.Pos; Then *ValueArm; ElseIf *ValueIf; Else *ValueArm }`
  - `ast.ValueSwitch{ span; Tag string; Cases []*ValueSwitchCase }`
  - `ast.ValueSwitchCase{ span; List string; Default bool; Value *ValueArm }`
  - `ast.ClassPart` gains `CF *ValueCF` where `ast.ValueCF{ span; If *ValueIf; Switch *ValueSwitch }` (exactly one of `If`/`Switch` non-nil). When `CF != nil`, `Expr`/`Cond`/`Stages` are unused.

- [ ] **Step 1: Add the node types to `ast/ast.go`**

After the `ClassPart`/`ClassAttr` block (~`ast/ast.go:357-376`), add:

```go
// ValueArm is one produced value in a value-form if/switch inside a class/style
// list — a Go string expression with an optional pipeline. It is a Node (for
// type harvest + diagnostics) but neither Markup nor Attr.
type ValueArm struct {
	span
	Expr   string
	Stages []PipeStage
}

// ValueIf is the value-producing `if Cond { Then } [else if … | else { Else }]`
// usable inside class/style. Then is always set; the tail is either ElseIf
// (an `else if` chain) or Else (a final `else { … }`), or neither.
type ValueIf struct {
	span
	Cond    string
	CondPos token.Pos
	Then    *ValueArm
	ElseIf  *ValueIf
	Else    *ValueArm
}

// ValueSwitch is the value-producing `switch [Tag] { case … default … }`.
// Tag is "" for a tagless switch.
type ValueSwitch struct {
	span
	Tag   string
	Cases []*ValueSwitchCase
}

// ValueSwitchCase is one `case List:` / `default:` arm of a ValueSwitch. List is
// the raw Go case expression(s); Default is true for `default:` (List == "").
type ValueSwitchCase struct {
	span
	List    string
	Default bool
	Value   *ValueArm
}

// ValueCF is the value-form control-flow attached to a ClassPart. Exactly one of
// If/Switch is non-nil.
type ValueCF struct {
	span
	If     *ValueIf
	Switch *ValueSwitch
}
```

Add the `CF` field to `ClassPart`:

```go
type ClassPart struct {
	Expr   string
	Cond   string
	Stages []PipeStage
	CF     *ValueCF // when non-nil, value-form if/switch; Expr/Cond/Stages unused
}
```

- [ ] **Step 2: Register the new nodes in `SetSpan` (`ast/ast.go:32`)**

Add cases (each pointer node must be present or `SetSpan` silently no-ops):

```go
	case *ValueArm:
		v.span = s
	case *ValueIf:
		v.span = s
	case *ValueSwitch:
		v.span = s
	case *ValueSwitchCase:
		v.span = s
	case *ValueCF:
		v.span = s
```

- [ ] **Step 3: Walk the new nodes in `Inspect` (`ast/ast.go:416`)**

`ClassAttr` is currently a leaf. Change its handling so value-form parts are walked, and add the recursive cases:

```go
	case *ClassAttr:
		for i := range n.Parts {
			if cf := n.Parts[i].CF; cf != nil {
				Inspect(cf, f)
			}
		}
	case *ValueCF:
		if n.If != nil {
			Inspect(n.If, f)
		}
		if n.Switch != nil {
			Inspect(n.Switch, f)
		}
	case *ValueIf:
		Inspect(n.Then, f)
		if n.ElseIf != nil {
			Inspect(n.ElseIf, f)
		}
		if n.Else != nil {
			Inspect(n.Else, f)
		}
	case *ValueSwitch:
		for _, c := range n.Cases {
			Inspect(c, f)
		}
	case *ValueSwitchCase:
		Inspect(n.Value, f)
	case *ValueArm:
		// leaf
```

Remove `ClassAttr` from the "leaves" comment at `ast/ast.go:~471` and update the `Inspect` doc block (`ast/ast.go:404-415`) to list the new children.

- [ ] **Step 4: Dump the new nodes in `ast/print.go`**

Extend the `*ClassAttr` case (`ast/print.go:189-197`) to recurse into value-form parts, and add `fprintNode` cases:

```go
	case *ClassAttr:
		if _, err := fmt.Fprintf(w, "%sClassAttr name=%s\n", indent, n.Name); err != nil {
			return err
		}
		for _, part := range n.Parts {
			if part.CF != nil {
				if err := fprintNode(w, part.CF, depth+1); err != nil {
					return err
				}
				continue
			}
			if _, err := fmt.Fprintf(w, "%s  ClassPart expr=%q cond=%q\n", indent, part.Expr, part.Cond); err != nil {
				return err
			}
		}
	case *ValueCF:
		if n.If != nil {
			return fprintNode(w, n.If, depth)
		}
		return fprintNode(w, n.Switch, depth)
	case *ValueIf:
		if _, err := fmt.Fprintf(w, "%sValueIf cond=%q\n", indent, n.Cond); err != nil {
			return err
		}
		if err := fprintNode(w, n.Then, depth+1); err != nil {
			return err
		}
		if n.ElseIf != nil {
			if err := fprintNode(w, n.ElseIf, depth+1); err != nil {
				return err
			}
		}
		if n.Else != nil {
			if _, err := fmt.Fprintf(w, "%s  else:\n", indent); err != nil {
				return err
			}
			if err := fprintNode(w, n.Else, depth+1); err != nil {
				return err
			}
		}
	case *ValueSwitch:
		if _, err := fmt.Fprintf(w, "%sValueSwitch tag=%q\n", indent, n.Tag); err != nil {
			return err
		}
		for _, cc := range n.Cases {
			if err := fprintNode(w, cc, depth+1); err != nil {
				return err
			}
		}
	case *ValueSwitchCase:
		if _, err := fmt.Fprintf(w, "%sValueSwitchCase list=%q default=%v\n", indent, n.List, n.Default); err != nil {
			return err
		}
		return fprintNode(w, n.Value, depth+1)
	case *ValueArm:
		if _, err := fmt.Fprintf(w, "%sValueArm expr=%q\n", indent, n.Expr); err != nil {
			return err
		}
```

- [ ] **Step 5: Confirm the packages still build**

Run: `go build ./ast/... && go vet ./ast/...`
Expected: clean (no parser wiring yet, so nothing produces these nodes).

- [ ] **Step 6: Convert `splitComposed` to a parser method and branch on the keyword**

In `parser/attrs.go`, change `splitComposed(src string) ([]ast.ClassPart, error)` to a method that knows the base offset of `src` within `p.src`, so value-form nodes get real positions:

```go
func (p *parser) splitComposed(src string, base int) ([]ast.ClassPart, error) {
```

Update the call in `parseComposedAttr` (`parser/attrs.go:97`):

```go
	parts, err := p.splitComposed(p.src[p.i+1:end], p.i+1)
```

Inside the per-segment loop, **before** the `colon`/`parsePipe` handling, detect a leading control-flow keyword and parse the value-form instead. The existing depth tracking already keeps a whole `if … { … }` / `switch … { … }` inside one segment (its internal commas/colons are brace-protected at depth ≥ 1), so only the dispatch is new:

```go
		segSrc := src[segStart:segEnd]
		if kw := leadingKeyword(segSrc); kw == "if" || kw == "switch" {
			// A depth-0 colon in a value-form segment is a disallowed guard.
			for _, c := range colons {
				if c > segStart && c < segEnd {
					return nil, p.errorf(p.posAt(base+c), "a value-form %s in class/style takes no `: cond` guard", kw)
				}
			}
			cf, err := p.parseValueCF(base+segStart, kw)
			if err != nil {
				return nil, err
			}
			parts = append(parts, ast.ClassPart{CF: cf})
			continue
		}
```

Add the keyword sniff helper (mirrors `braceKeyword` but for a trimmed substring; only `if`/`switch` are value-forms — `for` is not a value):

```go
// leadingKeyword returns "if" or "switch" if seg's first token is that keyword
// (followed by a non-identifier byte), else "".
func leadingKeyword(seg string) string {
	s := strings.TrimLeft(seg, " \t\r\n")
	for _, kw := range [...]string{"if", "switch"} {
		if strings.HasPrefix(s, kw) && (len(s) == len(kw) || !isIdentByte(s[len(kw)])) {
			return kw
		}
	}
	return ""
}
```

(`isIdentByte` already exists in `parser/`.)

- [ ] **Step 7: Implement the value-form parsers in `parser/valueform.go`**

These mirror `parseIfTail`/`parseSwitchMarkup`/`parseCaseClause` but each arm body is a **single value expression** (extracted between its `{` and matching `}`, then `parsePipe`d), not markup. `at` is the absolute offset in `p.src` of the segment's first keyword char.

```go
package parser

import (
	"strings"

	"github.com/gsxhq/gsx/ast"
)

// parseValueCF parses one value-form `if`/`switch` contribution. at is the
// absolute offset in p.src of the leading keyword; kw is "if" or "switch".
func (p *parser) parseValueCF(at int, kw string) (*ast.ValueCF, error) {
	start := p.posAt(at)
	cf := &ast.ValueCF{}
	var end token.Pos
	switch kw {
	case "if":
		vi, e, err := p.parseValueIf(at)
		if err != nil {
			return nil, err
		}
		cf.If, end = vi, e
	case "switch":
		vs, e, err := p.parseValueSwitch(at)
		if err != nil {
			return nil, err
		}
		cf.Switch, end = vs, e
	}
	ast.SetSpan(cf, start, end)
	return cf, nil
}

// parseValueIf parses `if Cond { Arm } [else if … | else { Arm }]` starting at
// the `if` keyword (offset at). Returns the node and the offset-position one past
// its last `}`.
func (p *parser) parseValueIf(at int) (*ast.ValueIf, token.Pos, error) {
	start := p.posAt(at)
	condStart := at + len("if")
	braceOff, ok := scanToBlockBrace(p.src, condStart, "if")
	if !ok {
		return nil, 0, p.errorf(p.posAt(condStart), "expected `{` after `if` condition")
	}
	rawCond := p.src[condStart:braceOff]
	lead := len(rawCond) - len(strings.TrimLeft(rawCond, " \t\r\n"))
	n := &ast.ValueIf{Cond: strings.TrimSpace(rawCond), CondPos: p.posAt(condStart + lead)}
	arm, afterThen, err := p.parseValueArm(braceOff)
	if err != nil {
		return nil, 0, err
	}
	n.Then = arm
	end := afterThen
	// optional else / else if
	rest := strings.TrimLeft(p.src[afterThen:], " \t\r\n")
	if strings.HasPrefix(rest, "else") {
		elseAt := afterThen + (len(p.src[afterThen:]) - len(rest)) + len("else")
		r2 := strings.TrimLeft(p.src[elseAt:], " \t\r\n")
		switch {
		case strings.HasPrefix(r2, "if") && (len(r2) == 2 || !isIdentByte(r2[2])):
			ifAt := elseAt + (len(p.src[elseAt:]) - len(r2))
			ei, e2, err := p.parseValueIf(ifAt)
			if err != nil {
				return nil, 0, err
			}
			n.ElseIf, end = ei, e2
		case strings.HasPrefix(r2, "{"):
			braceAt := elseAt + (len(p.src[elseAt:]) - len(r2))
			ea, e2, err := p.parseValueArm(braceAt)
			if err != nil {
				return nil, 0, err
			}
			n.Else, end = ea, e2
		default:
			return nil, 0, p.errorf(p.posAt(elseAt), "expected `{` or `if` after `else`")
		}
	}
	ast.SetSpan(n, start, end)
	return n, end, nil
}

// parseValueArm parses `{ <go-value-expr> }` whose `{` is at offset braceOff.
// Returns the arm and the position one past the matching `}`.
func (p *parser) parseValueArm(braceOff int) (*ast.ValueArm, token.Pos, error) {
	closeOff, ok := goExprEnd(p.src, braceOff)
	if !ok {
		return nil, 0, p.errorf(p.posAt(braceOff), "unterminated `{` in value-form arm")
	}
	inner := p.src[braceOff+1 : closeOff]
	lead := len(inner) - len(strings.TrimLeft(inner, " \t\r\n"))
	seed, stages, err := parsePipe(strings.TrimSpace(inner), p.posAt(braceOff+1+lead))
	if err != nil {
		return nil, 0, err
	}
	if strings.TrimSpace(inner) == "" {
		return nil, 0, p.errorf(p.posAt(braceOff), "value-form arm must produce a value")
	}
	arm := &ast.ValueArm{Expr: seed, Stages: stages}
	ast.SetSpan(arm, p.posAt(braceOff), p.posAt(closeOff+1))
	return arm, p.posAt(closeOff + 1), nil
}

// parseValueSwitch parses `switch [Tag] { case List: { Arm } … default: { Arm } }`
// starting at the `switch` keyword (offset at).
func (p *parser) parseValueSwitch(at int) (*ast.ValueSwitch, token.Pos, error) {
	start := p.posAt(at)
	tagStart := at + len("switch")
	braceOff, ok := scanToBlockBrace(p.src, tagStart, "switch")
	if !ok {
		return nil, 0, p.errorf(p.posAt(tagStart), "expected `{` after `switch`")
	}
	n := &ast.ValueSwitch{Tag: strings.TrimSpace(p.src[tagStart:braceOff])}
	i := braceOff + 1 // past switch-body `{`
	for {
		r := strings.TrimLeft(p.src[i:], " \t\r\n")
		if strings.HasPrefix(r, "}") {
			closeAt := i + (len(p.src[i:]) - len(r))
			end := p.posAt(closeAt + 1)
			ast.SetSpan(n, start, end)
			return n, end, nil
		}
		caseAt := i + (len(p.src[i:]) - len(r))
		cc, after, err := p.parseValueSwitchCase(caseAt)
		if err != nil {
			return nil, 0, err
		}
		n.Cases = append(n.Cases, cc)
		i = after
	}
}

// parseValueSwitchCase parses one `case List: { Arm }` or `default: { Arm }`
// starting at the keyword (offset at). Returns the node and the offset one past
// the arm's `}`.
func (p *parser) parseValueSwitchCase(at int) (*ast.ValueSwitchCase, int, error) {
	start := p.posAt(at)
	cc := &ast.ValueSwitchCase{}
	r := p.src[at:]
	var braceAt int
	switch {
	case strings.HasPrefix(r, "case") && (len(r) == 4 || !isIdentByte(r[4])):
		listStart := at + len("case")
		colonOff, ok := scanToCaseColon(p.src, listStart)
		if !ok {
			return nil, 0, p.errorf(p.posAt(listStart), "expected `:` in `case`")
		}
		cc.List = strings.TrimSpace(p.src[listStart:colonOff])
		rest := strings.TrimLeft(p.src[colonOff+1:], " \t\r\n")
		braceAt = colonOff + 1 + (len(p.src[colonOff+1:]) - len(rest))
	case strings.HasPrefix(r, "default") && (len(r) == 7 || !isIdentByte(r[7])):
		cc.Default = true
		colon := strings.IndexByte(p.src[at:], ':')
		if colon < 0 {
			return nil, 0, p.errorf(p.posAt(at), "expected `:` after `default`")
		}
		rest := strings.TrimLeft(p.src[at+colon+1:], " \t\r\n")
		braceAt = at + colon + 1 + (len(p.src[at+colon+1:]) - len(rest))
	default:
		return nil, 0, p.errorf(p.posAt(at), "expected `case` or `default` in value-form `switch`")
	}
	if braceAt >= len(p.src) || p.src[braceAt] != '{' {
		return nil, 0, p.errorf(p.posAt(braceAt), "expected `{` for case value")
	}
	arm, end, err := p.parseValueArm(braceAt)
	if err != nil {
		return nil, 0, err
	}
	cc.Value = arm
	ast.SetSpan(cc, start, end)
	return cc, p.offsetOf(end), nil
}
```

If `(*parser).offsetOf(token.Pos) int` does not exist, add it next to `posAt` (inverse: `int(pos) - p.base` using the same arithmetic `posAt` uses). Confirm `posAt`'s formula in `parser/parser.go` and mirror it.

- [ ] **Step 8: Write the parser-layer corpus case (`ast.golden`)**

Create `internal/corpus/testdata/cases/control_flow/value_switch_ast.txtar`:

```
-- input.gsx --
package views

component Badge(variant int) {
	<span class={ "base", switch variant {
	case 1:
		{ "green" }
	case 2, 3:
		{ "amber" }
	default:
		{ "gray" }
	}, "extra": variant > 0 }>x</span>
}
```

(No `invoke`/`render.golden` — this is a parser-layer `ast.golden` case, so the harness skips codegen and snapshots the AST dump.)

- [ ] **Step 9: Generate and verify the AST golden**

Run: `go test ./internal/corpus -run TestCorpus -update`
Then inspect the written `ast.golden` section: it must show `ClassAttr` → a `ClassPart` for `"base"`, a `ValueCF`/`ValueSwitch` with three `ValueSwitchCase`s (`list="1"`, `list="2, 3"`, `default=true`) each holding a `ValueArm`, then a `ClassPart expr="\"extra\"" cond="variant > 0"`.

Run: `go test ./internal/corpus -run TestCorpus -count=1`
Expected: PASS without `-update` (golden is stable, `coverage.golden` updated).

- [ ] **Step 10: Add a parse-error corpus case for the disallowed guard**

Create `internal/corpus/testdata/cases/control_flow/value_form_guard_rejected.txtar` with input:

```
-- input.gsx --
package views

component C(v int) {
	<span class={ if v > 0 { "a" } else { "b" }: v > 0 }>x</span>
}
```

Run `-update`, confirm the `diagnostics.golden` records the "value-form `if` … takes no `: cond` guard" error at the guard colon. Verify without `-update`.

- [ ] **Step 11: Commit**

```bash
git add ast/ast.go ast/print.go parser/attrs.go parser/valueform.go internal/corpus/testdata/cases/control_flow/value_switch_ast.txtar internal/corpus/testdata/cases/control_flow/value_form_guard_rejected.txtar internal/corpus/testdata/coverage.golden
git commit -m "feat(parser): value-form if/switch in class/style composed values"
```

---

## Task 3: Printer — lay out the value-form arms

Make `gsx fmt` print a value-form part with arms one-per-line when broken and collapsed when short, mirroring the markup `switch`/`if` builders.

**Files:**
- Modify: `internal/printer/printer.go` (`classPartDoc` from Task 1; new `valueCFDoc`/`valueIfDoc`/`valueSwitchDoc`/`valueArmDoc`)
- Test: `internal/printer/printer_test.go` (`TestValueFormSwitchLayout`, `TestValueFormIfInline`)

**Interfaces:**
- Consumes: `ast.ValueCF/ValueIf/ValueSwitch/ValueSwitchCase/ValueArm` (Task 2); `pretty.{Group,Concat,Text,Line,Indent,HardLine}`; `fmtExpr`, `fmtExprDoc`, `pipeStageStr` (`printer.go`).
- Produces: `(*printer).valueCFDoc(*ast.ValueCF) pretty.Doc` consumed only here.

- [ ] **Step 1: Write the failing tests**

```go
func TestValueFormSwitchLayout(t *testing.T) {
	src := `package p
component C(v int) {
	<span class={ "base", switch v { case 1: { "green-aaaaaaaaaaaaaaaaaaaaaaaaaaaa" } default: { "gray-bbbbbbbbbbbbbbbbbbbbbbbb" } } }>x</span>
}`
	want := `package p

component C(v int) {
	<span
		class={
			"base",
			switch v {
			case 1:
				"green-aaaaaaaaaaaaaaaaaaaaaaaaaaaa"
			default:
				"gray-bbbbbbbbbbbbbbbbbbbbbbbb"
			},
		}
	>
		x
	</span>
}
`
	assertFormat(t, src, want)
}

func TestValueFormIfInline(t *testing.T) {
	src := `package p
component C(b bool) {
	<i class={ "x", if b { "on" } else { "off" } }>y</i>
}`
	want := `package p

component C(b bool) {
	<i class={ "x", if b { "on" } else { "off" } }>y</i>
}
`
	assertFormat(t, src, want)
}
```

- [ ] **Step 2: Run to verify they fail**

Run: `go test ./internal/printer -run 'TestValueForm' -v`
Expected: FAIL (panic or empty output — `classPartDoc` ignores `CF`).

- [ ] **Step 3: Render the value-form in `classPartDoc`**

At the top of `classPartDoc` (Task 1), handle the value-form before the plain seed/guard path:

```go
func (p *printer) classPartDoc(part ast.ClassPart) pretty.Doc {
	if part.CF != nil {
		return p.valueCFDoc(part.CF)
	}
	seg := []pretty.Doc{fmtExprDoc(part.Expr)}
	// … unchanged …
}
```

Add the builders (mirror `switchMarkup`/`ifChain` at `printer.go:390-511`, but arms are a single value Doc, and the whole thing is `Group`ed so a short form stays inline):

```go
func (p *printer) valueCFDoc(cf *ast.ValueCF) pretty.Doc {
	if cf.If != nil {
		return pretty.Group(p.valueIfChain(cf.If))
	}
	return pretty.Group(p.valueSwitchDoc(cf.Switch))
}

func (p *printer) valueIfChain(i *ast.ValueIf) pretty.Doc {
	parts := []pretty.Doc{
		pretty.Text("if "), pretty.Text(fmtExpr(i.Cond)),
		pretty.Text(" {"), p.valueArmBody(i.Then), pretty.Text("}"),
	}
	switch {
	case i.ElseIf != nil:
		parts = append(parts, pretty.Text(" else "), p.valueIfChain(i.ElseIf))
	case i.Else != nil:
		parts = append(parts, pretty.Text(" else {"), p.valueArmBody(i.Else), pretty.Text("}"))
	}
	return pretty.Concat(parts...)
}

// valueArmBody renders ` <expr> ` flat, or newline-indented when the enclosing
// Group breaks (Line = space when flat, newline+indent when broken).
func (p *printer) valueArmBody(a *ast.ValueArm) pretty.Doc {
	return pretty.Concat(pretty.Indent(pretty.Concat(pretty.Line, p.valueArmDoc(a))), pretty.Line)
}

func (p *printer) valueArmDoc(a *ast.ValueArm) pretty.Doc {
	seg := []pretty.Doc{fmtExprDoc(a.Expr)}
	for _, s := range a.Stages {
		seg = append(seg, pretty.Text(" |> "), pretty.Text(pipeStageStr(s)))
	}
	return pretty.Concat(seg...)
}

func (p *printer) valueSwitchDoc(s *ast.ValueSwitch) pretty.Doc {
	head := []pretty.Doc{pretty.Text("switch")}
	if s.Tag != "" {
		head = append(head, pretty.Text(" "), pretty.Text(fmtExpr(s.Tag)))
	}
	head = append(head, pretty.Text(" {"))
	cases := make([]pretty.Doc, 0, len(s.Cases))
	for _, c := range s.Cases {
		label := pretty.Text("default:")
		if !c.Default {
			label = pretty.Concat(pretty.Text("case "), pretty.Text(fmtExpr(c.List)), pretty.Text(":"))
		}
		cases = append(cases,
			pretty.HardLine, label,
			pretty.Indent(pretty.Concat(pretty.HardLine, p.valueArmDoc(c.Value))))
	}
	return pretty.Concat(pretty.Concat(head...), pretty.Indent(pretty.Concat(cases...)), pretty.HardLine, pretty.Text("}"))
}
```

Note: `valueSwitchDoc` uses `HardLine` (a switch always breaks across lines, like `switchMarkup`); `valueIfChain` uses `Line` so a short `if b { "on" } else { "off" }` stays inline. `fmtExpr(c.List)` formats the case list (e.g. `2, 3`) — verify it does not choke on a comma list; if it does, emit `pretty.Text(c.List)` verbatim instead.

- [ ] **Step 4: Run the tests**

Run: `go test ./internal/printer -run 'TestValueForm' -v`
Expected: PASS. If the golden differs only in trailing-comma/indent specifics, correct `want` to the printer's actual output.

- [ ] **Step 5: Re-run the corpus AST case through fmt (idempotence)**

Run: `go test ./internal/printer -count=1 && go test ./internal/corpus -run TestCorpus -count=1`
Expected: PASS — and `gsx fmt` is idempotent on the Task 2 corpus input.

- [ ] **Step 6: Commit**

```bash
git add internal/printer/printer.go internal/printer/printer_test.go
git commit -m "feat(printer): lay out value-form if/switch arms in class/style"
```

---

## Task 4: Codegen — lower the value-form (non-tuple arms)

Emit the hoisted `_gsxvN` temp assigned by a generated Go `if`/`switch`, then pass it as the part `expr` into `gsx.Class(...)` / `gsx.StyleString(...)`. This delivers the core feature for plain (string) arms. Tuple unwrap is Task 5.

**Files:**
- Modify: `internal/codegen/emit.go` (`emitClassAttr`, `emitRootComposedClass`, `emitStyleAttr`, `rootStyleString`, `classEntryExpr`, and a new `hoistValueCF`)
- Create: `internal/corpus/testdata/cases/class/value_switch.txtar`, `cases/style/value_switch.txtar`, `cases/class/value_if_else.txtar`

**Interfaces:**
- Consumes: `ast.ValueCF/ValueIf/ValueSwitch/ValueArm`; `lowerClassPartSeed(p ast.ClassPart, table filterTable) (string, map[string]string, error)` (`emit.go:1475`) — reuse for arm lowering by wrapping each arm in a synthetic `ast.ClassPart{Expr: arm.Expr, Stages: arm.Stages}`; `styleDeclExpr(expr string, piped bool) string` (`emit.go:1535`); `interpTemp *int`.
- Produces: `hoistValueCF(b *bytes.Buffer, cf *ast.ValueCF, table filterTable, imports map[string]bool, interpTemp *int, style bool, bag *diag.Bag) (tmp string, ok bool)` — emits the Go statement block, returns the temp name to splice as the part expr.

- [ ] **Step 1: Write the corpus tests first (they fail at codegen)**

`internal/corpus/testdata/cases/class/value_switch.txtar`:

```
-- input.gsx --
package views

component Badge(variant int) {
	<span class={ "base", switch variant {
	case 1:
		{ "green" }
	case 2, 3:
		{ "amber" }
	default:
		{ "gray" }
	} }>x</span>
}
-- invoke --
Badge(BadgeProps{Variant: 3})
-- render.golden --
<span class="base amber">x</span>
```

`internal/corpus/testdata/cases/style/value_switch.txtar`:

```
-- input.gsx --
package views

component Box(tone int) {
	<div style={ "padding: 4px", switch tone {
	case 1:
		{ "color: red" }
	default:
		{ "color: gray" }
	} }>x</div>
}
-- invoke --
Box(BoxProps{Tone: 1})
-- render.golden --
<div style="padding: 4px; color: red">x</div>
```

`internal/corpus/testdata/cases/class/value_if_else.txtar`:

```
-- input.gsx --
package views

component Toggle(on bool) {
	<i class={ "ico", if on { "ico-on" } else { "ico-off" } }>y</i>
}
-- invoke --
Toggle(ToggleProps{On: false})
-- render.golden --
<i class="ico ico-off">y</i>
```

- [ ] **Step 2: Run to verify codegen fails**

Run: `go test ./internal/corpus -run TestCorpus -count=1`
Expected: FAIL — codegen does not yet handle `ClassPart.CF`; the part expr is empty/invalid Go.

- [ ] **Step 3: Implement `hoistValueCF`**

Add to `internal/codegen/emit.go`. It declares `var _gsxvN string`, emits a Go `if`/`switch` whose arms assign the lowered arm expression (CSS-wrapped when `style`), and returns the temp. (Tuple unwrap is added in Task 5; here arms are lowered verbatim.)

```go
// hoistValueCF emits `var _gsxvN string; <if|switch> { … _gsxvN = <arm> … }`
// before the class/style part list and returns the temp name. style=true wraps
// each arm value with styleDeclExpr (CSS-value filtering for dynamic arms).
func hoistValueCF(b *bytes.Buffer, cf *ast.ValueCF, table filterTable, imports map[string]bool, interpTemp *int, style bool, bag *diag.Bag) (string, bool) {
	tmp := fmt.Sprintf("_gsxv%d", *interpTemp)
	*interpTemp++
	fmt.Fprintf(b, "\t\tvar %s string\n", tmp)
	armExpr := func(a *ast.ValueArm) (string, bool) {
		expr, used, err := lowerClassPartSeed(ast.ClassPart{Expr: a.Expr, Stages: a.Stages}, table)
		if err != nil {
			bag.Errorf(a.Pos(), a.End(), "unresolved-pipeline", "%s", strings.TrimPrefix(err.Error(), "codegen: "))
			return "", false
		}
		for _, path := range used {
			imports[path] = true
		}
		if style {
			expr = styleDeclExpr(expr, len(a.Stages) > 0)
		}
		return expr, true
	}
	if cf.If != nil {
		return tmp, emitValueIf(b, cf.If, tmp, armExpr)
	}
	return tmp, emitValueSwitch(b, cf.Switch, tmp, armExpr)
}

func emitValueIf(b *bytes.Buffer, vi *ast.ValueIf, tmp string, armExpr func(*ast.ValueArm) (string, bool)) bool {
	e, ok := armExpr(vi.Then)
	if !ok {
		return false
	}
	fmt.Fprintf(b, "\t\tif %s {\n\t\t\t%s = %s\n\t\t}", vi.Cond, tmp, e)
	switch {
	case vi.ElseIf != nil:
		b.WriteString(" else ")
		// nested if without the leading "if " (emitValueIf writes it)
		if !emitValueIf(b, vi.ElseIf, tmp, armExpr) {
			return false
		}
	case vi.Else != nil:
		ee, ok := armExpr(vi.Else)
		if !ok {
			return false
		}
		fmt.Fprintf(b, " else {\n\t\t\t%s = %s\n\t\t}", tmp, ee)
	}
	b.WriteString("\n")
	return true
}

func emitValueSwitch(b *bytes.Buffer, vs *ast.ValueSwitch, tmp string, armExpr func(*ast.ValueArm) (string, bool)) bool {
	fmt.Fprintf(b, "\t\tswitch %s {\n", vs.Tag)
	for _, c := range vs.Cases {
		if c.Default {
			b.WriteString("\t\tdefault:\n")
		} else {
			fmt.Fprintf(b, "\t\tcase %s:\n", c.List)
		}
		e, ok := armExpr(c.Value)
		if !ok {
			return false
		}
		fmt.Fprintf(b, "\t\t\t%s = %s\n", tmp, e)
	}
	b.WriteString("\t\t}\n")
	return true
}
```

Note the `else `/nested-if seam: `emitValueIf` always writes its own leading `if `, so the `else ` prefix before a recursive call produces `} else if … {`. Verify the generated Go is gofmt-clean in Step 5 (the corpus golden runs through `go/format`).

- [ ] **Step 4: Splice the temp into each part-list builder**

In `emitClassAttr`, `emitRootComposedClass`, `emitStyleAttr`, `rootStyleString`, and `classEntryExpr`, handle `p.CF != nil` before the existing `classPartExpr` path. The value-form must hoist **before** the `_gsxgw.Class(...)` / `_gsxgw.Style(...)` write (or before the returned join expression). Pattern for `emitClassAttr` (apply the analogous change to the others):

```go
func emitClassAttr(b *bytes.Buffer, a *ast.ClassAttr, table filterTable, imports map[string]bool, interpTemp *int, bag *diag.Bag, mergeExpr string) bool {
	parts := make([]string, 0, len(a.Parts))
	for _, p := range a.Parts {
		if p.CF != nil {
			tmp, ok := hoistValueCF(b, p.CF, table, imports, interpTemp, false, bag)
			if !ok {
				return false
			}
			parts = append(parts, fmt.Sprintf("gsx.Class(%s)", tmp))
			continue
		}
		expr, ok := classPartExpr(p, a, table, imports, bag)
		if !ok {
			return false
		}
		if p.Cond == "" {
			parts = append(parts, fmt.Sprintf("gsx.Class(%s)", expr))
		} else {
			parts = append(parts, fmt.Sprintf("gsx.ClassIf(%s, %s)", expr, strings.TrimSpace(p.Cond)))
		}
	}
	fmt.Fprintf(b, "\t\t_gsxgw.S(%s)\n", strconv.Quote(" "+a.Name+`="`))
	fmt.Fprintf(b, "\t\t_gsxgw.Class(%s, %s)\n", mergeExpr, strings.Join(parts, ", "))
	fmt.Fprintf(b, "\t\t_gsxgw.S(%s)\n", strconv.Quote(`"`))
	return true
}
```

`emitClassAttr`/`emitStyleAttr` currently take no `interpTemp` — thread the existing per-component `interpTemp *int` into them (and into `emitRootComposedClass`/`rootStyleString`/`classEntryExpr`) from their callers in `genNode`/the root-attr path. For `classEntryExpr` (which returns a string and has no buffer at its call site), hoist into the same `b` used by `genChildComponent` before the Node-call write, matching how child-prop tuples already hoist there (`emit.go:2095-2149`); pass `b` and `interpTemp` through.

For `style=true` (the `emitStyleAttr`/`rootStyleString` calls) pass `true` so arms get `styleDeclExpr` wrapping, and wrap the temp as a plain `gsx.Class(tmp)` (the arm value is already CSS-filtered inside the hoist).

- [ ] **Step 5: Generate goldens and verify render**

Run: `go test ./internal/corpus -run TestCorpus -update`
Inspect `value_switch.txtar`'s `generated.x.go.golden`: a `var _gsxv0 string` + `switch variant { case 1: _gsxv0 = "green" … }` before `_gsxgw.Class(_gsxmerge, gsx.Class("base"), gsx.Class(_gsxv0))`. Confirm it is gofmt-clean (the golden is post-`go/format`).

Run: `go test ./internal/corpus -run TestCorpus -count=1`
Expected: PASS — `Badge(Variant:3)` → `class="base amber"`, style → `style="padding: 4px; color: red"`, toggle → `class="ico ico-off"`.

- [ ] **Step 6: Run the full codegen + check suite**

Run: `make check`
Expected: PASS (no regressions in existing class/style/control-flow cases).

- [ ] **Step 7: Commit**

```bash
git add internal/codegen/emit.go internal/corpus/testdata/cases/class/value_switch.txtar internal/corpus/testdata/cases/style/value_switch.txtar internal/corpus/testdata/cases/class/value_if_else.txtar internal/corpus/testdata/coverage.golden
git commit -m "feat(codegen): lower value-form if/switch in class/style to a hoisted temp"
```

---

## Task 5: Codegen — `(T,error)` auto-unwrap in value-form arms

Harvest each arm's value-expression type and apply the standard `tupleUnwrapType`+`hoistTuple` unwrap so an arm returning `(string,error)` works.

**Files:**
- Modify: `internal/codegen/analyze.go` (`collectExprs`/`componentExprs` + `emitProbes` to harvest `ValueArm` exprs)
- Modify: `internal/codegen/emit.go` (`hoistValueCF`'s `armExpr` to consult `resolved` and unwrap)
- Create: `internal/corpus/testdata/cases/class/value_switch_tuple.txtar`, `cases/class/value_arm_tuple_rejected.txtar`

**Interfaces:**
- Consumes: `resolved map[ast.Node]types.Type`; `tupleUnwrapType`, `hoistTuple` (`emit.go:956-973`); the harvest lockstep (`collectExprs` `analyze.go:1450`, `emitProbes` `analyze.go:687`, `harvest` `analyze.go:1048`).
- Produces: `resolved[arm]` populated for every `*ast.ValueArm`; `hoistValueCF` now takes `resolved` and `bag`.

- [ ] **Step 1: Write the tuple corpus case (fails: arm type unknown → emitted verbatim → Go multiple-value error)**

`internal/corpus/testdata/cases/class/value_switch_tuple.txtar`:

```
-- input.gsx --
package views

component Badge(variant int) {
	<span class={ "base", switch variant {
	case 1:
		{ cls(variant) }
	default:
		{ "gray" }
	} }>x</span>
}

func cls(v int) (string, error) { return "green", nil }
-- invoke --
Badge(BadgeProps{Variant: 1})
-- render.golden --
<span class="base green">x</span>
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/corpus -run TestCorpus -count=1`
Expected: FAIL — generated Go has `_gsxv0 = cls(variant)` (multiple-value in single-value assignment).

- [ ] **Step 3: Harvest `ValueArm` expression types**

In `collectExprs` (`analyze.go:1450`), where the ordered node list is built, add each `ClassAttr` part's value-form arms (in source order: `Then`, then `ElseIf` chain, then `Else`; for switch, each case's `Value`). Add a helper:

```go
// valueFormArms returns the arm value-expression nodes of a value-form part in
// source order, for type-harvest alignment.
func valueFormArms(cf *ast.ValueCF) []ast.Node {
	var out []ast.Node
	if cf.If != nil {
		var walk func(vi *ast.ValueIf)
		walk = func(vi *ast.ValueIf) {
			out = append(out, vi.Then)
			if vi.ElseIf != nil {
				walk(vi.ElseIf)
			}
			if vi.Else != nil {
				out = append(out, vi.Else)
			}
		}
		walk(cf.If)
		return out
	}
	for _, c := range cf.Switch.Cases {
		out = append(out, c.Value)
	}
	return out
}
```

Wherever `collectExprs` walks element attrs, add value-form arms to the node list. In `emitProbes` (`analyze.go:687`), at the same point it currently emits the liveness `_ = (expr)` for class parts (`analyze.go:866-872`, `walkLivenessAttrExprs`), emit a harvested `_gsxuse(<arm-expr>)` probe **per arm in the same order** — using the lowered seed (apply `lowerClassPartSeed` to the synthetic `ast.ClassPart{Expr, Stages}` so a piped arm harvests the pipeline result type). Keep the non-value-form parts on the existing liveness path. The k-th `_gsxuse` ↔ k-th node alignment in `harvest` (`analyze.go:1067-1090`) then populates `resolved[arm]` automatically, exactly as for `OrderedPair` (`analyze.go:820-828`, `1472-1478`).

- [ ] **Step 4: Unwrap in `hoistValueCF`**

Change `hoistValueCF`'s signature to take `resolved map[ast.Node]types.Type`, and make `armExpr` unwrap a tuple before assignment:

```go
	armExpr := func(a *ast.ValueArm) (string, bool) {
		expr, used, err := lowerClassPartSeed(ast.ClassPart{Expr: a.Expr, Stages: a.Stages}, table)
		if err != nil {
			bag.Errorf(a.Pos(), a.End(), "unresolved-pipeline", "%s", strings.TrimPrefix(err.Error(), "codegen: "))
			return "", false
		}
		for _, path := range used {
			imports[path] = true
		}
		if t := resolved[a]; t != nil {
			if _, isTuple := t.(*types.Tuple); isTuple {
				if _, ok := tupleUnwrapType(t); !ok {
					bag.Errorf(a.Pos(), a.End(), "invalid-tuple", "value-form arm %q returns %s; only (T, error) is supported", a.Expr, t)
					return "", false
				}
				expr = hoistTuple(b, expr, interpTemp) // emits tmp,_gsxerr:=…;if…{return _gsxerr} into b
			}
		}
		if style {
			expr = styleDeclExpr(expr, len(a.Stages) > 0)
		}
		return expr, true
	}
```

Important ordering: `hoistTuple` writes statements into `b`. Those must land **before** the arm's `_gsxvN = …` line but **inside** the correct `case`/`if` block. Since `armExpr` is called while emitting each arm's body (Step 3 of Task 4 calls it right before writing `tmp = e`), the hoist statements naturally precede the assignment within the same block — but they are written to `b` at the point `armExpr` runs. Restructure `emitValueSwitch`/`emitValueIf` so `armExpr` is invoked *after* the `case …:` / `if … {` line is written and *before* the `tmp = e` line, so the hoist lands inside the block. Adjust the two emit helpers accordingly (call `armExpr` between writing the case label and the assignment).

- [ ] **Step 5: Add the non-`(T,error)` rejection case**

`internal/corpus/testdata/cases/class/value_arm_tuple_rejected.txtar`:

```
-- input.gsx --
package views

component C(v int) {
	<span class={ switch v {
	case 1:
		{ bad(v) }
	default:
		{ "x" }
	} }>y</span>
}

func bad(v int) (int, string) { return 0, "" }
```

(No `invoke` — error case.) Expect a `diagnostics.golden` "value-form arm … only (T, error) is supported" pointed at `bad(v)`.

- [ ] **Step 6: Generate, verify**

Run: `go test ./internal/corpus -run TestCorpus -update`
Inspect `value_switch_tuple.txtar` golden: `case 1:` block contains `_gsxv1, _gsxerr := cls(variant); if _gsxerr != nil { return _gsxerr }; _gsxv0 = _gsxv1`.

Run: `go test ./internal/corpus -run TestCorpus -count=1`
Expected: PASS — render `class="base green"`; rejection case reports the clean diagnostic.

- [ ] **Step 7: Confirm harvest alignment didn't shift existing goldens**

Run: `make check`
Expected: PASS. If any unrelated golden's `_gsxvN` indices shifted, that means the probe order changed harvest alignment — re-verify `collectExprs`/`emitProbes` add arms only for value-form parts and in the same relative position. Regenerate and review the diff; only value-form cases should change.

- [ ] **Step 8: Commit**

```bash
git add internal/codegen/analyze.go internal/codegen/emit.go internal/corpus/testdata/cases/class/value_switch_tuple.txtar internal/corpus/testdata/cases/class/value_arm_tuple_rejected.txtar internal/corpus/testdata/coverage.golden
git commit -m "feat(codegen): (T,error) auto-unwrap in value-form class/style arms"
```

---

## Task 6: Codegen — `(T,error)` auto-unwrap for plain class/style parts

Close the `uniform-tuple-unwrap` class/style non-goal: a plain `class={ f() }` with `f() (string,error)` now unwraps. Requires harvesting plain-part Expr types (today they are liveness-only, treated as always-string).

**Files:**
- Modify: `internal/codegen/analyze.go` (harvest plain `ClassPart.Expr` types alongside the value-form arms)
- Modify: `internal/codegen/emit.go` (`classPartExpr`/the part-list builders: unwrap when the part type is a tuple)
- Create: `internal/corpus/testdata/cases/class/part_tuple.txtar`, `cases/style/part_tuple.txtar`, `cases/class/multi_part_tuple_order.txtar`

**Interfaces:**
- Consumes: the harvest machinery (now extended in Task 5); `tupleUnwrapType`, `hoistTuple`.
- Produces: `resolved[<plain ClassPart>]` populated. Because `ClassPart` is a plain value (not a Node), promote it minimally so it can key `resolved` — give it a `span` and `Pos()/End()`, register in `SetSpan`, and walk it in `Inspect` by pointer-to-slice-element (mirror `OrderedPair`, `ast/ast.go:383-387` + `Inspect` `&n.Pairs[i]`).

- [ ] **Step 1: Promote `ClassPart` to a Node**

In `ast/ast.go`, embed `span` in `ClassPart` and add a marker-free `Node` (like `CaseClause`/`OrderedPair`):

```go
type ClassPart struct {
	span
	Expr   string
	Cond   string
	Stages []PipeStage
	CF     *ValueCF
}
```

Add `case *ClassPart: v.span = s` to `SetSpan`. In `Inspect`, change the `ClassAttr` case to walk each part by pointer (so a part is a `resolved` key) and recurse its `CF`:

```go
	case *ClassAttr:
		for i := range n.Parts {
			Inspect(&n.Parts[i], f)
		}
	case *ClassPart:
		if n.CF != nil {
			Inspect(n.CF, f)
		}
```

In the parser (`splitComposed`), set each non-value-form part's span: `part.span` via `ast.SetSpan(&parts[len(parts)-1], p.posAt(base+segStart), p.posAt(base+segEnd))` (or set during construction). Verify `ast/print.go` still compiles (it ranges parts by value — switch to indexing if it needs the pointer; the dump output is unaffected).

- [ ] **Step 2: Write the plain-part tuple corpus cases (fail first)**

`internal/corpus/testdata/cases/class/part_tuple.txtar`:

```
-- input.gsx --
package views

component C(v int) {
	<span class={ "base", cls(v) }>x</span>
}

func cls(v int) (string, error) { return "green", nil }
-- invoke --
C(CProps{V: 1})
-- render.golden --
<span class="base green">x</span>
```

`internal/corpus/testdata/cases/style/part_tuple.txtar` (analogous, `style={ "padding: 4px", sty(v) }` with `sty(int) (string,error)` returning `"color: red"`, render `style="padding: 4px; color: red"`).

`internal/corpus/testdata/cases/class/multi_part_tuple_order.txtar` — two tuple parts `{ a(), b() }` plus a non-tuple, asserting source-order hoisting (both functions append to a shared `[]string` via a package var, render asserts order). Keep it pure: use two funcs returning distinct constant classes and assert the rendered class order matches source order.

- [ ] **Step 3: Run to verify failure**

Run: `go test ./internal/corpus -run TestCorpus -count=1`
Expected: FAIL — `gsx.Class(cls(v))` is a Go multiple-value error.

- [ ] **Step 4: Harvest plain-part types**

Extend Task 5's `collectExprs`/`emitProbes` change: for a non-value-form `ClassPart` whose `Cond == ""` (an unconditional value contribution), emit a harvested `_gsxuse(<lowered-seed>)` probe and add the part node to the ordered list — replacing its liveness `_ = (expr)` with the harvested probe (the probe still satisfies liveness). Guarded parts (`Cond != ""`) keep the liveness path and are **not** unwrapped (a guarded part is `gsx.ClassIf(expr, cond)`; unwrapping a guarded tuple is out of scope — document inline). Keep alignment deterministic: emit value-form arm probes and plain-part probes in the exact source order `collectExprs` lists them.

- [ ] **Step 5: Unwrap in the part-list builders**

In `emitClassAttr`/`emitRootComposedClass`/`emitStyleAttr`/`rootStyleString`/`classEntryExpr`, for an unconditional non-value-form part, consult `resolved[&part]`; if it's a tuple, hoist before the list write and pass the temp:

```go
		if p.Cond == "" {
			expr := lowered // from classPartExpr
			if t := resolved[&a.Parts[i]]; t != nil {
				if _, isTuple := t.(*types.Tuple); isTuple {
					if _, ok := tupleUnwrapType(t); !ok {
						bag.Errorf(p.Pos(), p.End(), "invalid-tuple", "class/style part %q returns %s; only (T, error) is supported", p.Expr, t)
						return false
					}
					expr = hoistTuple(b, expr, interpTemp)
				}
			}
			parts = append(parts, fmt.Sprintf("gsx.Class(%s)", expr))
		}
```

Use the loop index `i` (range over `a.Parts` by index) so `&a.Parts[i]` matches the harvested node identity. For `classEntryExpr` (returns a string, hoists into the child-component buffer), follow the same "hoist-all-when-any" structure already used for child-prop tuples (`emit.go:2095-2149`) so source order is preserved when multiple parts are tuples.

- [ ] **Step 6: Generate, verify**

Run: `go test ./internal/corpus -run TestCorpus -update`
Run: `go test ./internal/corpus -run TestCorpus -count=1`
Expected: PASS — plain-part tuples render; multi-part order preserved.

- [ ] **Step 7: Full suite — watch for golden index shifts**

Run: `make check`
Expected: PASS. Existing class/style cases with non-tuple parts: generated output unchanged (non-tuple parts still emit verbatim; only the skeleton changed, not the output). If any non-tuple golden changed, the harvest probe altered emission — investigate before regenerating.

- [ ] **Step 8: Commit**

```bash
git add ast/ast.go internal/codegen/analyze.go internal/codegen/emit.go parser/attrs.go internal/corpus/testdata/cases/class/part_tuple.txtar internal/corpus/testdata/cases/style/part_tuple.txtar internal/corpus/testdata/cases/class/multi_part_tuple_order.txtar internal/corpus/testdata/coverage.golden
git commit -m "feat(codegen): (T,error) auto-unwrap for plain class/style parts"
```

---

## Task 7: Edge & negative corpus coverage

Pin the remaining behaviors the spec calls out, so future changes can't silently regress them.

**Files:**
- Create: `cases/class/value_if_no_else.txtar`, `cases/class/value_switch_no_default.txtar`, `cases/class/value_switch_tagless.txtar`, `cases/class/value_arm_pipeline.txtar`
- Create (runtime): `error_propagation_test.go` in the root `gsx`/codegen test package

- [ ] **Step 1: `if` without `else` == additive guard**

`value_if_no_else.txtar`: `class={ "base", if on { "extra" } }`, invoke with `on:false`, render `<… class="base">` (empty contribution when false — equivalent to `"extra": on`). Add a second invoke variant or a sibling case with `on:true` → `class="base extra"`.

- [ ] **Step 2: `switch` with no matching case and no default → empty contribution**

`value_switch_no_default.txtar`: `class={ "base", switch v { case 1: { "one" } } }`, invoke `v:2`, render `class="base"`.

- [ ] **Step 3: Tagless switch**

`value_switch_tagless.txtar`: `class={ switch { case v > 0: { "pos" } default: { "nonpos" } } }`, invoke `v:1`, render `class="pos"`. Confirms `Tag == ""` lowers to Go `switch {` (boolean cases).

- [ ] **Step 4: Pipeline inside an arm**

`value_arm_pipeline.txtar`: an arm using a registered class filter, e.g. `{ base |> tw }` (use an existing filter from `cases/pipelines/`), render asserts the piped result. Confirms `lowerClassPartSeed` handles arm stages.

- [ ] **Step 5: Runtime error propagation**

In the root test package, add a unit test that renders a component whose value-form arm returns a non-nil error and asserts `Render` returns it and output halts (corpus uses the nil-error path, so this must be a Go unit test). Mirror the existing `(T,error)` propagation test added by `uniform-tuple-unwrap` (`grep -rn "return _gsxerr" --include=*_test.go` for the pattern).

- [ ] **Step 6: Generate, verify, commit**

```bash
go test ./internal/corpus -run TestCorpus -update
go test ./internal/corpus -run TestCorpus -count=1
go test ./... -count=1
git add internal/corpus/testdata/cases/class/value_*.txtar internal/corpus/testdata/coverage.golden <runtime test file>
git commit -m "test(corpus): value-form edge cases (no-else, no-default, tagless, pipeline, error-propagation)"
```

---

## Task 8: Validate on `ds/badge/badge.gsx` (one-learning migration)

Rewrite the real component that motivated this, proving the feature end-to-end and deleting the negation default + unwrappable line.

**Files:**
- Modify: `~/work/one-learning-gsx/ds/badge/badge.gsx`

**Interfaces:**
- Consumes: the feature shipped in Tasks 2–6, built into the `gsx` binary the one-learning worktree uses (`go run ./cmd/gsx` from this repo, or the worktree's pinned build).

- [ ] **Step 1: Rewrite the Badge class map as a switch**

Replace the six-entry additive map (with the `variant != Green && …` default) by:

```gsx
component Badge(variant Variant) {
	<span class={
		"inline-flex items-center rounded-md px-2 py-1 text-xs font-medium ring-1 ring-inset",
		switch variant {
		case Green:
			{ "bg-green-50 text-green-700 ring-green-600/20 dark:bg-green-500/10 dark:text-green-400 dark:ring-green-500/30" }
		case Yellow:
			{ "bg-yellow-50 text-yellow-700 ring-yellow-600/20 dark:bg-yellow-500/10 dark:text-yellow-400 dark:ring-yellow-500/30" }
		case Red:
			{ "bg-red-50 text-red-700 ring-red-600/20 dark:bg-red-500/10 dark:text-red-400 dark:ring-red-500/30" }
		case Blue:
			{ "bg-blue-50 text-blue-700 ring-blue-600/20 dark:bg-blue-500/10 dark:text-blue-400 dark:ring-blue-500/30" }
		case Purple:
			{ "bg-purple-50 text-purple-700 ring-purple-600/20 dark:bg-purple-500/10 dark:text-purple-400 dark:ring-purple-500/30" }
		default:
			{ "bg-gray-50 text-gray-700 ring-gray-600/20 dark:bg-gray-500/10 dark:text-gray-400 dark:ring-gray-500/30" }
		}
	}>
		{ children }
	</span>
}
```

- [ ] **Step 2: Generate and format**

Run (from the gsx repo, against the worktree): `go run ./cmd/gsx generate ~/work/one-learning-gsx/ds/badge && go run ./cmd/gsx fmt ~/work/one-learning-gsx/ds/badge/badge.gsx`
Expected: generates `badge.x.go`, fmt is idempotent (the switch arms lay out one per line, no over-long line).

- [ ] **Step 3: Verify DOM-equivalence with the existing harness**

Run the one-learning migration's DOM-equivalence test for badge (the harness referenced in the migration memory). Expected: PASS for all six variants + unknown→gray.

- [ ] **Step 4: Commit (in the one-learning-gsx worktree)**

```bash
cd ~/work/one-learning-gsx && git add ds/badge/badge.gsx && git commit -m "ds/badge: exclusive class selection via value-form switch"
```

---

## Task 9: tree-sitter-gsx grammar

**Files:**
- Modify: `~/personal/gsxhq/tree-sitter-gsx/grammar.js`
- Possibly modify: `~/personal/gsxhq/tree-sitter-gsx/src/scanner.c` (external `go_interp_text` refuses leading `if/switch`)
- Create/modify: `~/personal/gsxhq/tree-sitter-gsx/test/corpus/control_flow.txt`
- Modify: `~/personal/gsxhq/tree-sitter-gsx/queries/highlights.scm`

**Interfaces:**
- Consumes: existing `control_flow` (`grammar.js:84-98`), `_hole_body`/`pipeline` (`:67-69`), `expr_attribute` (`:108-111`), external `go_interp_text`/`go_cond_text` scanner tokens.

- [ ] **Step 1: Add a value-form rule usable inside class/style values**

The composed `class={…}` value currently parses as `expr_attribute` → `_hole_body` → `go_interp_text` (the whole list swallowed). Add a `value_control_flow` rule mirroring `control_flow` but whose arm bodies are `{ <value> }` holes, and allow it as an alternative within the composed value. Because the scanner's `go_interp_text` deliberately refuses a leading `if/switch`, a value-form starting a segment will not be swallowed — but a value-form **after** a comma is inside the swallowed text today. Decide the minimal grammar: either (a) parse `class`/`style` values with a dedicated `composed_value` rule that splits on commas and accepts `value_control_flow | go_interp_text` per segment, or (b) keep it coarse and only structurally recognize a value-form when it is the whole value. Prefer (a) for fidelity; (b) is acceptable for highlighting-only. Document the choice in the grammar comment.

- [ ] **Step 2: Add corpus tests**

Append to `test/corpus/control_flow.txt` (tree-sitter `=== / --- / S-expr` format) cases for `class={ "x", switch v { case 1: { "a" } default: { "b" } } }` and the `if/else` value-form, with expected S-expressions.

- [ ] **Step 3: Generate + test**

Run: `cd ~/personal/gsxhq/tree-sitter-gsx && npm run gen 2>/dev/null || npx tree-sitter generate; npx tree-sitter test`
Expected: new cases PASS; existing corpus unaffected.

- [ ] **Step 4: Highlight query + commit**

Add a `highlights.scm` rule scoping `if/switch/case/default` inside the value-form as `@keyword`. Run `npx tree-sitter test`. Commit.

```bash
git add grammar.js src/scanner.c queries/highlights.scm test/corpus/control_flow.txt
git commit -m "feat: value-form if/switch in class/style attribute values"
```

---

## Task 10: vscode-gsx highlighting

**Files:**
- Modify: `~/personal/gsxhq/vscode-gsx/syntaxes/gsx.tmLanguage.src.yaml`
- Regenerate: `gsx.tmLanguage.json` (via `npm run gen:grammar`)

**Interfaces:**
- Consumes: `#attribute` (`:95-105`), `#interp` (`:56-63`) repository entries.

- [ ] **Step 1: Add a class/style value scope**

In the `#attribute` repository entry, add a pattern (before the generic `#interp` fallback) matching `(class|style)\s*=\s*\{` that opens a `meta.embedded.block.go.gsx`-style region delegating to `source.go` (so `if/switch/case/default/else` highlight as Go keywords), giving the composed list its own scope name for future querying. The grammar is intentionally coarse — do not attempt to structurally parse the list; rely on `source.go` for the arm keywords.

- [ ] **Step 2: Regenerate and sanity-check**

Run: `cd ~/personal/gsxhq/vscode-gsx && npm run gen:grammar`
Open a `.gsx` sample with a value-form switch in the Extension Development Host (or run any tmLanguage fixture tests under `test/`) and confirm the arms colorize.

- [ ] **Step 3: Commit**

```bash
git add syntaxes/gsx.tmLanguage.src.yaml syntaxes/gsx.tmLanguage.json
git commit -m "feat: highlight value-form if/switch in class/style values"
```

---

## Task 11: Documentation

**Files:**
- Modify: `docs/guide/` (the class/style / control-flow page; create a "value-form if/switch" section)
- Modify: `docs/ROADMAP.md` (mark the feature shipped)

- [ ] **Step 1: Document the feature**

In the relevant `docs/guide/` page, add a section covering: the value-form `if`/`switch` inside `class`/`style`; braced arms; exclusivity vs. the additive map; the `if`-without-`else` == additive-guard equivalence; no-match → empty contribution; `(T,error)` arms/parts unwrap; the not-in-scope list (general attrs use cond-attrs; markup children use markup control-flow; no guard on a value-form part; pipe stages on the result deferred). Use the badge switch as the worked example.

- [ ] **Step 2: Roadmap + verify docs build (only if docs CI applies)**

Update `docs/ROADMAP.md`. If editing `docs/guide/**`, the VitePress `docs` CI job applies (clones `gsxhq/gsxhq.github.io`); a local prose review is sufficient unless you touched build config.

- [ ] **Step 3: Commit**

```bash
git add docs/guide docs/ROADMAP.md
git commit -m "docs: value-form if/switch in class/style"
```

---

## Final verification

- [ ] **Run the authoritative CI mirror**

Run: `make ci`
Expected: PASS — build/vet/test both modules, examples drift clean, `gofmt` + `gsx fmt` clean, corpus + coverage.golden consistent.

- [ ] **Independent adversarial review** (per CLAUDE.md): one reviewer who builds throwaway probe programs (e.g. a value-form with a tuple arm + a guarded plain-part tuple in the same `class`, a tagless switch, an `else if` chain, a piped arm) and diffs generated Go + rendered HTML against hand-derived expectations — not just a diff read — before merging the subsystem.

---

## Self-review notes (spec coverage)

- Exclusive selection / braced arms / `switch` + `if/else if/else` → Tasks 2–4.
- Scope = class/style only; disallowed guard; out-of-scope positions → Tasks 2 (guard reject), 7, 11 (docs).
- `if` without `else` == additive guard; no-match → empty → Task 7.
- Alloc-free temp-hoist lowering (not IIFE) → Task 4 (`hoistValueCF` emits `var _gsxvN`).
- `(T,error)` unwrap for arms AND plain parts; non-`(T,error)` rejection; multi-tuple source order; pipeline-into-arm; error propagation → Tasks 5, 6, 7.
- Formatter: comma wrap + arm layout → Tasks 1, 3.
- Corpus per context (class + style) → Tasks 4, 5, 6, 7.
- Downstream tree-sitter / vscode / docs → Tasks 9, 10, 11.
- Real-world validation → Task 8.
