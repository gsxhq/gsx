# Control-Flow Body Whitespace Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make control-flow block bodies (`{ if }`/`{ else }`/`{ for }`/`{ switch }`) preserve interior whitespace per the JSX rule while trimming whitespace immediately inside the braces, so `{ env } - { bar }` keeps the spaces around the hyphen.

**Architecture:** `parseMarkupUntilClose` currently strips leading whitespace from every text run inside a block body (via a per-iteration `p.skipSpace()`). It is shared with `name={…}` markup-attribute slots, whose consumers panic on whitespace text nodes. Add a `preserveWS` parameter so control-flow bodies keep interior whitespace (deferring to `wsnorm`, the existing React/Babel JSXText port) while attribute slots keep today's behavior; then trim the two brace-interior edges of a control-flow body with a small `trimBodyEdges` helper.

**Tech Stack:** Go 1.26.1, `github.com/gsxhq/gsx` (parser + `internal/wsnorm` + `internal/corpus` txtar harness), Node/React (dev-time oracle only, not in CI).

## Global Constraints

- Runtime (root package) stays standard-library only; this change is entirely in `parser/` + test corpora.
- `wsnorm` is the single source of whitespace truth for interior text; do NOT add a second whitespace rule there — the parser change only decides which raw text nodes reach it.
- Every syntax/codegen behavior ships txtar corpus coverage; regenerate goldens with `-update`, then verify WITHOUT `-update`.
- Do not hand-edit `.x.go` or `*.golden` files — regenerate them.
- Pin Go to `GO_VERSION` in `.github/workflows/ci.yml` (1.26.1).
- React is a dev-time oracle only; no node/React dependency may enter CI or `make ci`.
- Commit after each task.

---

### Task 1: Parser fix — preserve interior whitespace, trim brace-interior edges

**Files:**
- Modify: `parser/markup.go` (`parseMarkupUntilClose` ~line 248; `parseControlBody` ~line 281; `parseCaseBody` ~line 472)
- Test: `parser/blockbody_ws_test.go` (create)

**Interfaces:**
- Produces: `parseMarkupUntilCloseWS(what string, preserveWS bool) ([]ast.Markup, error)` — the whitespace-aware core. `parseMarkupUntilClose(what string)` keeps its signature, delegating with `preserveWS=false`. `trimBodyEdges(nodes []ast.Markup) []ast.Markup` — strips leading whitespace of the first node and trailing whitespace of the last node when they are `*ast.Text`, dropping emptied nodes.
- Consumes: existing `p.skipSpace()`, `p.eof()`, `p.peek()`, `p.atWord()`, `p.parseElement()`, `p.parseBraceNode()`, `p.parseTextCtx(true)`, `ast.Text`, `ast.Markup`, `ast.SwitchMarkup`, `ast.CaseClause`.
- Note: control-flow bodies (`if`/`else`/`for`) go through `parseControlBody`; switch case bodies go through the separate `parseCaseBody`, which needs the lookahead-restore treatment (it terminates on `case`/`default` keywords, not just `}`). Both apply `trimBodyEdges`.

- [ ] **Step 1: Write the failing tests**

Create `parser/blockbody_ws_test.go`:

```go
package parser

import (
	"testing"

	"github.com/gsxhq/gsx/ast"
)

// firstIfThen parses src and returns the Then body of the first IfMarkup found
// anywhere in the last-declared component's body.
func firstIfThen(t *testing.T, src string) []ast.Markup {
	t.Helper()
	f := parseStringT(t, src)
	comp := f.Decls[len(f.Decls)-1].(*ast.Component)
	var found *ast.IfMarkup
	var walk func(nodes []ast.Markup)
	walk = func(nodes []ast.Markup) {
		for _, n := range nodes {
			switch v := n.(type) {
			case *ast.IfMarkup:
				if found == nil {
					found = v
				}
			case *ast.Element:
				walk(v.Children)
			}
		}
	}
	walk(comp.Body)
	if found == nil {
		t.Fatal("no IfMarkup found")
	}
	return found.Then
}

// TestBlockBodyPreservesInteriorWhitespace: the space run between two holes
// inside a control-flow body survives to wsnorm (this is the bug fix).
func TestBlockBodyPreservesInteriorWhitespace(t *testing.T) {
	then := firstIfThen(t, `package p
component X(a, b string) { <div>{ if true { {a} - {b} } }</div> }
`)
	if len(then) != 3 {
		t.Fatalf("Then has %d nodes, want 3: %#v", len(then), then)
	}
	txt, ok := then[1].(*ast.Text)
	if !ok || txt.Value != " - " {
		t.Fatalf("interior text = %#v, want *ast.Text %q", then[1], " - ")
	}
}

// TestBlockBodyTrimsInlineEdges: inline whitespace immediately inside the braces
// around a lone element is trimmed.
func TestBlockBodyTrimsInlineEdges(t *testing.T) {
	then := firstIfThen(t, `package p
component X() { <div>{ if true { <span/> } }</div> }
`)
	if len(then) != 1 {
		t.Fatalf("Then has %d nodes, want 1 (edges trimmed): %#v", len(then), then)
	}
	if _, ok := then[0].(*ast.Element); !ok {
		t.Fatalf("Then[0] = %#v, want *ast.Element", then[0])
	}
}

// TestBlockBodyWhitespaceOnlyIsEmpty: a whitespace-only body trims to nothing.
func TestBlockBodyWhitespaceOnlyIsEmpty(t *testing.T) {
	then := firstIfThen(t, `package p
component X() { <div>{ if true {    } }</div> }
`)
	if len(then) != 0 {
		t.Fatalf("Then has %d nodes, want 0: %#v", len(then), then)
	}
}

// TestBlockBodyEdgeTrimKeepsInteriorLeadingSpace: trailing edge trimmed, but the
// interior space after the hole is kept.
func TestBlockBodyEdgeTrimKeepsInteriorLeadingSpace(t *testing.T) {
	then := firstIfThen(t, `package p
component X(a string) { <div>{ if true { {a} - } }</div> }
`)
	if len(then) != 2 {
		t.Fatalf("Then has %d nodes, want 2: %#v", len(then), then)
	}
	txt, ok := then[1].(*ast.Text)
	if !ok || txt.Value != " -" {
		t.Fatalf("interior text = %#v, want *ast.Text %q (trailing edge trimmed)", then[1], " -")
	}
}

// firstSwitchCaseBody returns the Body of the first case clause of the first
// SwitchMarkup found in the last-declared component.
func firstSwitchCaseBody(t *testing.T, src string) []ast.Markup {
	t.Helper()
	f := parseStringT(t, src)
	comp := f.Decls[len(f.Decls)-1].(*ast.Component)
	var found *ast.SwitchMarkup
	var walk func(nodes []ast.Markup)
	walk = func(nodes []ast.Markup) {
		for _, n := range nodes {
			switch v := n.(type) {
			case *ast.SwitchMarkup:
				if found == nil {
					found = v
				}
			case *ast.Element:
				walk(v.Children)
			}
		}
	}
	walk(comp.Body)
	if found == nil || len(found.Cases) == 0 {
		t.Fatal("no SwitchMarkup/case found")
	}
	return found.Cases[0].Body
}

// TestSwitchCaseBodyPreservesInteriorWhitespace: interior whitespace in a switch
// case body survives (fixed via parseCaseBody lookahead-restore). Default-only
// avoids the pre-existing keyword-swallow limitation.
func TestSwitchCaseBodyPreservesInteriorWhitespace(t *testing.T) {
	body := firstSwitchCaseBody(t, `package p
component X(k string) { <div>{ switch k {
	default:
		hi - {k} - bye
	} }</div> }
`)
	if len(body) != 3 {
		t.Fatalf("case body has %d nodes, want 3: %#v", len(body), body)
	}
	lead, ok := body[0].(*ast.Text)
	if !ok || lead.Value != "hi - " {
		t.Fatalf("body[0] = %#v, want *ast.Text %q", body[0], "hi - ")
	}
	trail, ok := body[2].(*ast.Text)
	if !ok || trail.Value != " - bye" {
		t.Fatalf("body[2] = %#v, want *ast.Text %q (interior space kept, edge trimmed)", body[2], " - bye")
	}
}

// TestTrimBodyEdges exercises the helper directly.
func TestTrimBodyEdges(t *testing.T) {
	span := &ast.Element{Tag: "span"}
	sig := func(nodes []ast.Markup) string {
		s := ""
		for _, n := range nodes {
			switch v := n.(type) {
			case *ast.Text:
				s += "T(" + v.Value + ")"
			case *ast.Element:
				s += "<" + v.Tag + ">"
			}
		}
		return s
	}
	cases := []struct {
		name string
		in   []ast.Markup
		want string
	}{
		{"trim both inline", []ast.Markup{&ast.Text{Value: " "}, span, &ast.Text{Value: " "}}, "<span>"},
		{"keep interior", []ast.Markup{span, &ast.Text{Value: " - "}, span}, "<span>T( - )<span>"},
		{"trim leading only, keep core", []ast.Markup{&ast.Text{Value: "  x"}}, "T(x)"},
		{"whitespace only -> empty", []ast.Markup{&ast.Text{Value: "  \n "}}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := sig(trimBodyEdges(c.in))
			if got != c.want {
				t.Fatalf("trimBodyEdges = %q, want %q", got, c.want)
			}
		})
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./parser/ -run 'TestBlockBody|TestSwitchCaseBody|TestTrimBodyEdges' -v`
Expected: compile error `undefined: trimBodyEdges` (whole-package compile failure is the expected "fail" here). Once `trimBodyEdges` exists but before the loop changes, `TestBlockBodyPreservesInteriorWhitespace` and `TestSwitchCaseBodyPreservesInteriorWhitespace` fail (current parser yields `Text "- "`/`"- bye"`, not `" - "`/`" - bye"`).

- [ ] **Step 3: Implement the parser change**

In `parser/markup.go`, replace the head of `parseMarkupUntilClose`:

```go
func (p *parser) parseMarkupUntilClose(what string) ([]ast.Markup, error) {
	return p.parseMarkupUntilCloseWS(what, false)
}

// parseMarkupUntilCloseWS is parseMarkupUntilClose with control over inter-node
// whitespace. When preserveWS is false (markup-attribute slots) leading
// whitespace before each node is skipped, as before. When true (control-flow
// bodies) whitespace falls into parseTextCtx and becomes a text node for wsnorm;
// parseControlBody then trims the brace-interior edges via trimBodyEdges.
func (p *parser) parseMarkupUntilCloseWS(what string, preserveWS bool) ([]ast.Markup, error) {
	var nodes []ast.Markup
	for {
		if !preserveWS {
			p.skipSpace()
		}
		if p.eof() {
			return nil, p.errorf(p.pos(), "unterminated %s, expected `}`", what)
		}
		switch {
		case p.peek() == '}':
			p.i++ // consume the closing brace
			return nodes, nil
		case p.peek() == '<':
			el, err := p.parseElement()
			if err != nil {
				return nil, err
			}
			nodes = append(nodes, el)
		case p.peek() == '{':
			node, skipped, err := p.parseBraceNode()
			if err != nil {
				return nil, err
			}
			if !skipped {
				nodes = append(nodes, node)
			}
		default:
			nodes = append(nodes, p.parseTextCtx(true))
		}
	}
}
```

Then replace `parseControlBody` and add `trimBodyEdges`:

```go
// parseControlBody parses a control-flow body: markup until the matching '}'.
// The cursor must be just past the opening '{'. Interior whitespace is preserved
// for wsnorm; whitespace immediately inside the braces is trimmed.
func (p *parser) parseControlBody() ([]ast.Markup, error) {
	nodes, err := p.parseMarkupUntilCloseWS("control-flow body", true)
	if err != nil {
		return nil, err
	}
	return trimBodyEdges(nodes), nil
}

// trimBodyEdges strips whitespace immediately inside the control-flow body
// braces: the leading whitespace of the first node and the trailing whitespace
// of the last node, when those nodes are Text. This mirrors how gsx trims the
// interior of `{ expr }` and `{{ code }}`. Interior whitespace between nodes is
// left for wsnorm's JSX rule. An emptied edge Text node is dropped.
func trimBodyEdges(nodes []ast.Markup) []ast.Markup {
	if len(nodes) > 0 {
		if t, ok := nodes[0].(*ast.Text); ok {
			t.Value = strings.TrimLeft(t.Value, " \t\r\n")
			if t.Value == "" {
				nodes = nodes[1:]
			}
		}
	}
	if len(nodes) > 0 {
		if t, ok := nodes[len(nodes)-1].(*ast.Text); ok {
			t.Value = strings.TrimRight(t.Value, " \t\r\n")
			if t.Value == "" {
				nodes = nodes[:len(nodes)-1]
			}
		}
	}
	return nodes
}
```

(`strings` is already imported in `parser/markup.go`.)

Finally, apply the lookahead-restore to `parseCaseBody` (switch case arms — a
separate loop that terminates on `case`/`default` keywords, not just `}`). Replace
its head:

```go
func (p *parser) parseCaseBody() ([]ast.Markup, error) {
	var nodes []ast.Markup
	for {
		// Look past whitespace to detect a terminator without destroying interior
		// whitespace: if the next token is a terminator, the skipped whitespace was
		// the case body's trailing edge (trimmed by trimBodyEdges); otherwise
		// restore so the whitespace becomes part of the following text node.
		save := p.i
		p.skipSpace()
		if p.eof() {
			return nil, p.errorf(p.pos(), "unterminated `case` body")
		}
		if p.peek() == '}' || p.atWord("case") || p.atWord("default") {
			return trimBodyEdges(nodes), nil
		}
		p.i = save
		switch {
		case p.peek() == '<':
			// ... existing element case, unchanged ...
```

Keep the rest of `parseCaseBody`'s `switch` body (the `<`/`{`/default arms) exactly
as-is; only the loop head changes (the old `p.skipSpace()` + `if terminator {
return nodes }` becomes the save/restore above, and the terminator return now wraps
`trimBodyEdges`).

- [ ] **Step 4: Run the new tests to verify they pass**

Run: `go test ./parser/ -run 'TestBlockBody|TestSwitchCaseBody|TestTrimBodyEdges' -v`
Expected: PASS (all six tests).

- [ ] **Step 5: Run the full parser suite for regressions**

Run: `go test ./parser/`
Expected: `ok  github.com/gsxhq/gsx/parser`. The existing `TestParseIf*` tests pass unchanged — their bodies are single-line lone-element (no interior whitespace), so edge-trimming yields the same structure the old skip-space produced.

- [ ] **Step 6: Commit**

```bash
git add parser/markup.go parser/blockbody_ws_test.go
git commit -m "fix(parser): preserve interior whitespace in control-flow bodies

parseMarkupUntilClose and parseCaseBody skipped leading whitespace of every
text run inside a control-flow / switch-case body, eating
semantically-significant spaces (e.g. the space in { env } - { bar }). Scope
the skip to markup-attribute slots via a preserveWS param; if/else/for
bodies preserve interior whitespace for wsnorm and trim only brace-interior
edges via trimBodyEdges; switch case bodies use a lookahead-restore to keep
keyword-terminator detection while preserving interior whitespace.

Claude-Session: https://claude.ai/code/session_01NHMAaMsoZwgjNVvR3GLxAa"
```

---

### Task 2: Corpus coverage — interior scenarios per context, edges, and layout regression

**Files:**
- Create: `internal/corpus/testdata/cases/whitespace/element_baseline.txtar`
- Create: `internal/corpus/testdata/cases/whitespace/interior_if_else.txtar`
- Create: `internal/corpus/testdata/cases/whitespace/interior_for.txtar`
- Create: `internal/corpus/testdata/cases/whitespace/interior_switch.txtar`
- Create: `internal/corpus/testdata/cases/whitespace/switch_two_arm.txtar`
- Create: `internal/corpus/testdata/cases/whitespace/edges.txtar`
- Create: `internal/corpus/testdata/cases/whitespace/layout_title.txtar`
- Regenerate: `internal/corpus/testdata/coverage.golden` (via `-update`)
- Dev-only (scratchpad, NOT committed): a Node/React differential script

**Interfaces:**
- Consumes: Task 1's parser behavior (interior preserved, edges trimmed).
- Produces: pinned `render.golden` + `generated.x.go.golden` for each case.

- [ ] **Step 1: Create the element-body baseline case (the interior oracle)**

`internal/corpus/testdata/cases/whitespace/element_baseline.txtar`:

```
Element-body whitespace baseline. These renders are the oracle that control-flow
bodies must match for INTERIOR whitespace (validated against React
renderToStaticMarkup in dev; see plan). Element bodies already behave correctly;
this pins the reference.
-- input.gsx --
package views

component Base(a, b string) {
	<div id="s1">{a} - end</div>
	<div id="s3">{a} {b}</div>
	<div id="s4">{a}   -   {b}</div>
}
-- invoke --
Base("x", "y")
-- diagnostics.golden --
-- render.golden --
-- generated.x.go.golden --
```

- [ ] **Step 2: Create the if/else interior case**

`internal/corpus/testdata/cases/whitespace/interior_if_else.txtar`:

```
Interior whitespace inside { if } / { else } bodies must match the element-body
baseline: the space run between two holes is preserved/collapsed by the JSX rule.
Multiline (nl) drops the newline-adjacent space, same as an element body.
-- input.gsx --
package views

component Sep(a, b string) {
	<div id="if">{ if true { {a} - {b} } }</div>
	<div id="multi">{ if true { {a}   -   {b} } }</div>
	<div id="else">{ if false { <span/> } else { {a} - {b} } }</div>
	<div id="nl">{ if true {
		{a}
		- {b}
	} }</div>
	<div id="nest">{ if true { A - { if true { B } } } }</div>
}
-- invoke --
Sep("x", "y")
-- diagnostics.golden --
-- render.golden --
-- generated.x.go.golden --
```

- [ ] **Step 3: Create the for interior case**

`internal/corpus/testdata/cases/whitespace/interior_for.txtar`:

```
Interior whitespace inside a { for } body (between holes) is preserved. The
whitespace is in the FOR body itself, not inside a nested element.
-- input.gsx --
package views

component List(items []string) {
	<div>{ for i, it := range items { {i}: {it} } }</div>
}
-- invoke --
List([]string{"a", "b"})
-- diagnostics.golden --
-- render.golden --
-- generated.x.go.golden --
```

- [ ] **Step 4: Create the switch interior case**

`internal/corpus/testdata/cases/whitespace/interior_switch.txtar`:

```
Interior whitespace inside a { switch } case body is preserved. Uses a single
`default` arm to avoid the pre-existing keyword-swallow limitation (a plain text
run before another `case`/`default` swallows it — out of scope, see spec).
-- input.gsx --
package views

component Kind(k string) {
	<div>{ switch k {
	default:
		hello - {k} - world
	} }</div>
}
-- invoke --
Kind("a")
-- diagnostics.golden --
-- render.golden --
-- generated.x.go.golden --
```

Also add `internal/corpus/testdata/cases/whitespace/switch_two_arm.txtar`,
exercising the `case`/`default` terminator with element-terminated arms (each arm
ends in `<br/>`, so the keyword is detected without swallow):

```
Two-arm switch: interior whitespace kept, keyword terminator detected because
each arm ends with an element before the next case/default.
-- input.gsx --
package views

component Pick(k string) {
	<div>{ switch k {
	case "a":
		one - {k} <br/>
	default:
		two - {k} <br/>
	} }</div>
}
-- invoke --
Pick("a")
-- diagnostics.golden --
-- render.golden --
-- generated.x.go.golden --
```

- [ ] **Step 5: Create the edges case**

`internal/corpus/testdata/cases/whitespace/edges.txtar`:

```
Whitespace immediately inside control-flow braces is trimmed (syntactic, like the
interior of { expr }). E4: a separator space that must survive belongs in the
PARENT context (after the closing }), not at the block's inner edge.
-- input.gsx --
package views

component Edges(env, title string) {
	<div id="e1">{ if true { <span>x</span> } }</div>
	<div id="e2">{ if true {    } }</div>
	<div id="e3">{ if true {
		<span>x</span>
	} }</div>
	<div id="e4">{ if true { {env} - } } {title}</div>
}
-- invoke --
Edges("dev", "Home")
-- diagnostics.golden --
-- render.golden --
-- generated.x.go.golden --
```

- [ ] **Step 6: Create the layout.gsx title regression case**

`internal/corpus/testdata/cases/whitespace/layout_title.txtar`:

```
Regression for one-learning/ui/layout.gsx: the conditional environment prefix.
Written the idiomatic Option-B way — the separator's trailing space lives in the
parent (title body) after the closing }, so it survives.
-- input.gsx --
package views

component Title(env, page string, isProd bool) {
	<title>{ if !isProd { {env} - } } {page} - One Learning</title>
}
-- invoke --
Title("development", "Team Management", false)
-- diagnostics.golden --
-- render.golden --
-- generated.x.go.golden --
```

- [ ] **Step 7: Generate goldens**

Run: `go test ./internal/corpus -run TestCorpus -update`
Expected: exit 0; the six new `.txtar` files now have populated `render.golden` and `generated.x.go.golden` sections, and `coverage.golden` is rewritten.

- [ ] **Step 8: Verify the generated renders match expectations**

Run: `grep -A1 'render.golden' internal/corpus/testdata/cases/whitespace/*.txtar`
Confirm these render bodies (whitespace is significant):
- `element_baseline`: `<div id="s1">x - end</div><div id="s3">x y</div><div id="s4">x - y</div>`
- `interior_if_else`: `<div id="if">x - y</div><div id="multi">x - y</div><div id="else">x - y</div><div id="nl">x- y</div><div id="nest">A - B</div>`
- `interior_for`: `<div>0: a1: b</div>` (the `: ` interior space kept each iteration)
- `interior_switch`: `<div>hello - a - world</div>`
- `switch_two_arm`: `<div>one - a <br></div>` (case "a" arm; confirm `<br/>` renders as `<br>`)
- `edges`: `<div id="e1"><span>x</span></div><div id="e2"></div><div id="e3"><span>x</span></div><div id="e4">dev - Home</div>`
- `layout_title`: `<title>development - Team Management - One Learning</title>`

If any differ, STOP and investigate — the parser fix or a case's markup is wrong, not the golden.

- [ ] **Step 9: Validate interior scenarios against React (dev-time oracle)**

Create `/private/tmp/claude-501/-Users-jackieli-personal-gsxhq-gsx/af1b1dae-86c0-45cd-8a0f-ddd7603e012e/scratchpad/ws_oracle.mjs` (React is already installed there from earlier; if not: `npm i @babel/core @babel/preset-react react react-dom`):

```js
import babel from "@babel/core";
import React from "react";
import { renderToStaticMarkup } from "react-dom/server";
// Element-body equivalents of the INTERIOR scenarios (edges are gsx-specific).
const cases = [
  ["s1", `<div id="s1">{a} - end</div>`],
  ["s3", `<div id="s3">{a} {b}</div>`],
  ["s4", `<div id="s4">{a}   -   {b}</div>`],
];
for (const [name, jsx] of cases) {
  const src = `const a="x",b="y"; module.exports = (${jsx});`;
  const code = babel.transformSync(src, { presets: [["@babel/preset-react", { runtime: "classic" }]] }).code;
  const m = { exports: {} };
  new Function("React", "module", "exports", code)(React, m, m.exports);
  console.log(name, "=>", JSON.stringify(renderToStaticMarkup(m.exports)));
}
```

Run: `cd <scratchpad> && node ws_oracle.mjs`
Expected: `s1 => "<div id=\"s1\">x - end</div>"`, `s3 => "<div id=\"s3\">x y</div>"`, `s4 => "<div id=\"s4\">x - y</div>"` — matching the `element_baseline` render and, transitively, the `interior_*` renders. If React disagrees with a pinned interior render, STOP — `wsnorm`'s rule and the goldens must agree with React.

- [ ] **Step 10: Verify corpus without -update**

Run: `go test ./internal/corpus -run TestCorpus`
Expected: `ok`. (No `-update`; goldens must already be correct.)

- [ ] **Step 11: Commit**

```bash
git add internal/corpus/testdata/cases/whitespace internal/corpus/testdata/coverage.golden
git commit -m "test(corpus): control-flow body whitespace (interior + edges + layout)

Pins interior-whitespace preservation per context (if/else/for/switch)
against the element-body baseline, brace-interior edge trimming, and the
layout.gsx title regression. Interior renders validated vs React
renderToStaticMarkup.

Claude-Session: https://claude.ai/code/session_01NHMAaMsoZwgjNVvR3GLxAa"
```

---

### Task 3: fmt idempotence case, docs note, and full CI

**Files:**
- Create: `internal/gsxfmt/testdata/cases/control_flow_body_ws.txtar`
- Modify: `docs/guide/` whitespace/JSX section (locate with grep below)

**Interfaces:**
- Consumes: Task 1 parser behavior. No new symbols.

- [ ] **Step 1: Create the fmt idempotence case**

Confirm the section format first: `head -20 internal/gsxfmt/testdata/cases/tab_width_2.txtar` (sections `-- input.gsx --` and `-- fmt.golden --`).

Create `internal/gsxfmt/testdata/cases/control_flow_body_ws.txtar` with `input.gsx` only (leave `fmt.golden` empty; `-update` fills it):

```
Formatting a control-flow body must not change what it renders: interior
whitespace around the hyphen is preserved, and reflow does not reintroduce the
eaten-space bug.
-- input.gsx --
package ui

component C(a, b string) {
	<div>{ if true { {a} - {b} } }</div>
}
-- fmt.golden --
```

- [ ] **Step 2: Generate and inspect the fmt golden**

Run: `go test ./internal/gsxfmt -run TestFmtCorpus -update`
Then: `sed -n '/fmt.golden/,$p' internal/gsxfmt/testdata/cases/control_flow_body_ws.txtar`
Expected: the formatted output still contains `{a} - {b}` with the spaces around the hyphen intact (whatever exact reflow the formatter chooses, the ` - ` interior text must survive).

- [ ] **Step 3: Verify fmt corpus without -update (idempotence)**

Run: `go test ./internal/gsxfmt -run TestFmtCorpus`
Expected: `ok`. This proves parse→wsnorm→print is stable and semantics-preserving for the block body.

- [ ] **Step 4: Add the docs note**

Find the whitespace section: `grep -rln -i "whitespace\|jsx" docs/guide/ | head`. In the relevant page (e.g. a text/whitespace guide), add a short note. If literal `{{ }}` appears in prose, wrap the block in `::: v-pre`. Add:

```markdown
## Whitespace in control-flow bodies

Whitespace inside `{ if }`, `{ for }`, and `{ switch }` bodies follows the same
JSX rule as element bodies: whitespace *between* content is preserved (a run with
a newline collapses away; an inline run collapses to one space). Whitespace
*immediately inside* the control-flow braces is ignored, like the interior of
`{ expr }`. To keep a separator space next to a conditional, put it in the
surrounding markup rather than at the block's inner edge:

::: v-pre
```gsx
<title>{ if !isProd { {env} - } } {page} - One Learning</title>
```
:::
```

- [ ] **Step 5: Run the full check**

Run: `make check`
Expected: all lanes pass (build/vet/test both modules, examples drift, gofmt + gsx fmt). If `examples/` drift is reported, regenerate per the message and re-run.

- [ ] **Step 6: Commit**

```bash
git add internal/gsxfmt/testdata/cases/control_flow_body_ws.txtar docs/guide
git commit -m "test(fmt)+docs: control-flow body whitespace idempotence and guide note

Claude-Session: https://claude.ai/code/session_01NHMAaMsoZwgjNVvR3GLxAa"
```

---

## Self-Review

**Spec coverage:**
- Problem/root cause → Task 1 (parser fix in `parseMarkupUntilClose` + `parseCaseBody`). ✓
- Chosen model Option B (preserve interior, trim edges, scope attr-slots) → Task 1 (`preserveWS`, `trimBodyEdges`). ✓
- Switch case bodies (separate `parseCaseBody`, keyword terminators, lookahead-restore) → Task 1 Step 3 + `TestSwitchCaseBody*`. ✓
- Attribute-slot guard → Task 1 (existing parser suite passes; `preserveWS=false` keeps slot behavior — the full `go test ./parser/` in Step 5 covers the slot tests). ✓
- Interior scenarios per context (if/else/for/switch) + element baseline + nested (S10) → Task 2. ✓
- Edge scenarios E1–E4 + whitespace-only + layout regression → Task 2 (`edges`, `layout_title`). ✓
- Pre-existing switch keyword-swallow limitation → out of scope (spec); corpus switch cases avoid it (default-only / element-terminated arms). ✓
- React-as-oracle validation of interior scenarios → Task 2 Step 9. ✓
- fmt idempotence → Task 3. ✓
- Docs note → Task 3. ✓
- Siblings unaffected (no grammar change) → no task needed; stated in spec. ✓

**Placeholder scan:** No TBD/TODO; all code and expected outputs are concrete. The `render.golden`/`fmt.golden`/`generated.x.go.golden` sections are intentionally empty pre-`-update` (the harness fills them), with expected renders pinned in Task 2 Step 8.

**Type consistency:** `parseMarkupUntilCloseWS(what, preserveWS)`, `parseControlBody`, `parseCaseBody`, `trimBodyEdges([]ast.Markup) []ast.Markup`, `firstIfThen`, `firstSwitchCaseBody` used consistently across Task 1. `ast.Text.Value`, `ast.Element.Tag`, `ast.IfMarkup.Then`, `ast.SwitchMarkup.Cases`, `ast.CaseClause.Body` match the codebase.
