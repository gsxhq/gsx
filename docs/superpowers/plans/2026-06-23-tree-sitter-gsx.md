# tree-sitter-gsx Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax.

**Goal:** A tree-sitter grammar (`gsxhq/tree-sitter-gsx`) that highlights `.gsx` across Go/HTML/JS/CSS via injection, handling gsx's additions (`{ }` Go holes, `@{ }` JS/CSS holes, `|>` pipeline).

**Architecture:** Purpose-built `grammar.js` + external `src/scanner.c`. The grammar parses gsx structure only; base languages are delegated by range via `queries/injections.scm` (`go`, `javascript`-combined, `css`-combined). The scanner does boundary scanning the CFG can't: hole-end (`goExprEnd`), top-level `|>`, `<script>`/`<style>` raw text, and the markup-vs-Go (Babel) decision.

**Tech Stack:** tree-sitter CLI 0.26.x, grammar.js (tree-sitter DSL), C (external scanner), Scheme query files. Node 25 + clang available.

**Working repo:** all paths below are inside the **new** repo `~/personal/gsxhq/tree-sitter-gsx` (created in Task 1), NOT the gsx repo. The gsx examples are read from `~/personal/gsxhq/gsx/examples/` as the parse oracle.

## Global Constraints
- Grammar **name** is `gsx`; file type `.gsx`; tree-sitter scope `source.gsx`.
- gsx additions over base languages, and ONLY these: `{ goExpr }` markup holes (with `?` try-marker), `@{ goExpr }` holes inside `<script>`/`<style>` and JS/CSS attributes, `|>` pipeline inside holes/expr-attrs. Everything else is Go/HTML/JS/CSS, delegated by injection.
- Component tag = first letter uppercase **or** dotted (`<Card>`, `<ui.Button>`, `<p.Content>`); element tag = lowercase/hyphenated (`<div>`, `<el-dialog>`).
- Injected languages are named exactly `go`, `javascript`, `css` in `injections.scm`. Each `|>` pipeline segment is injected separately so the `go` grammar never sees `|>`.
- `src/parser.c` and `src/tree_sitter/` (generated) are **committed** (GitHub Linguist + zero-build installs need them).
- Parse oracle: every file in `~/personal/gsxhq/gsx/examples/*.gsx` must parse with **zero ERROR/MISSING nodes** by the end (Task 11).
- The grammar re-implements gsx boundary rules independently of the Go `parser/`; when in doubt about a boundary, match the gsx parser's behavior (the examples are the contract).

## File Structure
- `grammar.js` — all grammar rules (one file; tree-sitter convention).
- `src/scanner.c` — external scanner: `go_text`, `raw_text`, `pipe_op` boundary tokens + Babel lookahead.
- `src/parser.c`, `src/grammar.json`, `src/node-types.json`, `src/tree_sitter/parser.h` — generated, committed.
- `queries/highlights.scm`, `queries/injections.scm` — captures + injections.
- `test/corpus/*.txt` — tree-sitter tests (one file per construct group).
- `test/examples/` — the 12 gsx examples (committed copies; oracle).
- `tree-sitter.json`, `package.json`, `.gitignore`, `README.md`, `.github/workflows/ci.yml`.

---

## Task 1: Scaffold repo + toolchain + trivial grammar

**Files:** Create everything under `~/personal/gsxhq/tree-sitter-gsx/`.

- [ ] **Step 1: Create the repo and config**
```bash
mkdir -p ~/personal/gsxhq/tree-sitter-gsx && cd ~/personal/gsxhq/tree-sitter-gsx
git init -b main
```
Create `package.json`:
```json
{
  "name": "tree-sitter-gsx",
  "version": "0.0.1",
  "private": true,
  "description": "tree-sitter grammar for the gsx templating language",
  "devDependencies": { "tree-sitter-cli": "^0.26.0" },
  "scripts": { "generate": "tree-sitter generate", "test": "tree-sitter test" }
}
```
Create `tree-sitter.json`:
```json
{
  "grammars": [
    {
      "name": "gsx",
      "scope": "source.gsx",
      "file-types": ["gsx"],
      "injection-regex": "^gsx$",
      "path": "."
    }
  ],
  "metadata": { "version": "0.0.1", "license": "MIT", "description": "tree-sitter grammar for gsx" }
}
```
Create `.gitignore`:
```gitignore
node_modules/
*.log
build/
```
(Do NOT gitignore `src/` — `parser.c` is committed.)

- [ ] **Step 2: Minimal grammar.js**
```js
module.exports = grammar({
  name: 'gsx',
  extras: $ => [/\s/, $.line_comment, $.block_comment],
  rules: {
    source_file: $ => repeat($._top_level),
    _top_level: $ => choice($.component_declaration, $.go_chunk),

    // Placeholder until Task 2 wires the scanner: a component with an empty body.
    component_declaration: $ => seq(
      'component', field('name', $.identifier), '(', ')', $.body,
    ),
    body: $ => seq('{', '}'),

    // Placeholder: a single Go-ish line. Replaced in Task 2.
    go_chunk: $ => token(prec(-1, /[^\n]+/)),

    identifier: $ => /[A-Za-z_][A-Za-z0-9_]*/,
    line_comment: $ => token(seq('//', /.*/)),
    block_comment: $ => token(seq('/*', /[^*]*\*+([^/*][^*]*\*+)*/, '/')),
  },
});
```

- [ ] **Step 3: First corpus test** — Create `test/corpus/skeleton.txt`:
```
==================
empty component
==================

component Foo() {}

---

(source_file
  (component_declaration
    name: (identifier)
    (body)))
```

- [ ] **Step 4: Generate + test**
```bash
cd ~/personal/gsxhq/tree-sitter-gsx && npm install && npx tree-sitter generate && npx tree-sitter test
```
Expected: `skeleton.txt` passes. (If `go_chunk` swallows the component, that's expected to be fixed in Task 2 — for now the single test should pass; remove the placeholder `go_chunk` from the choice temporarily if it interferes: keep `_top_level: $ => $.component_declaration` for this task only.)

- [ ] **Step 5: CI** — Create `.github/workflows/ci.yml`:
```yaml
name: CI
on: [push, pull_request]
jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v5
      - uses: actions/setup-node@v6
        with: { node-version: 24 }
      - run: npm install
      - run: npx tree-sitter generate
      - run: npx tree-sitter test
```

- [ ] **Step 6: Commit** (commit generated `src/` too)
```bash
git add -A && git commit -m "chore: scaffold tree-sitter-gsx (toolchain, trivial grammar, CI)"
```

---

## Task 2: External scanner + Go/component split

**Files:** `src/scanner.c` (create), `grammar.js` (modify), `test/corpus/toplevel.txt` (create).

**Interfaces:**
- Produces grammar `externals`: `$.go_text` (a run of Go source up to a gsx boundary), `$.raw_text` (Task 8), `$.pipe` (Task 9). Declare all three now; implement `go_text` here, leave `raw_text`/`pipe` returning false until their tasks.
- `go_text` scans bytes — respecting Go `"..."`, `` `...` ``, `'.'` literals, `//`/`/* */` comments, and nested `{}` — stopping (without consuming) at a top-level boundary: the keyword `component` at column-significant top level, or (inside a hole) the matching `}`, or `|>`. Here it handles the **top-level** case: consume Go until `component ` at brace-depth 0 or EOF.

- [ ] **Step 1: Declare externals + scanner registration in grammar.js**
```js
module.exports = grammar({
  name: 'gsx',
  externals: $ => [$.go_text, $.raw_text, $.pipe],
  extras: $ => [/\s/, $.line_comment, $.block_comment],
  // ... rules
});
```
Update `_top_level` and `go_chunk`:
```js
    source_file: $ => repeat($._top_level),
    _top_level: $ => choice($.component_declaration, $.go_chunk),
    go_chunk: $ => $.go_text,   // injected as Go (Task 10)
```

- [ ] **Step 2: Write `src/scanner.c` (go_text top-level scan)**
```c
#include "tree_sitter/parser.h"
#include <string.h>

enum TokenType { GO_TEXT, RAW_TEXT, PIPE };

void *tree_sitter_gsx_external_scanner_create(void) { return NULL; }
void tree_sitter_gsx_external_scanner_destroy(void *p) {}
unsigned tree_sitter_gsx_external_scanner_serialize(void *p, char *b) { return 0; }
void tree_sitter_gsx_external_scanner_deserialize(void *p, const char *b, unsigned n) {}

static void advance(TSLexer *l) { l->advance(l, false); }

// Returns true if the upcoming input is the `component` keyword at a word boundary.
static bool at_component_kw(TSLexer *l) {
  const char *kw = "component";
  // NOTE: TSLexer cannot peek multiple chars without consuming; this helper is
  // only correct when called at a position the scanner controls. We instead detect
  // `component` by consuming into go_text and bailing — see scan_go_text.
  return false; // unused; real logic in scan_go_text
}

// Consume Go source until brace-depth 0 AND the next token would start a `component`
// declaration, or EOF. Respects strings/runes/comments/braces. Marks end before the
// `component` keyword. Returns true if it consumed at least one byte.
static bool scan_go_text(TSLexer *l) {
  int depth = 0;
  bool consumed = false;
  for (;;) {
    if (l->eof(l)) break;
    int32_t c = l->lookahead;

    // At depth 0 and a word boundary, check for `component`.
    if (depth == 0 && c == 'c') {
      l->mark_end(l);            // candidate stop point (before `component`)
      // Try to match the keyword by consuming; if it matches and is followed by a
      // non-identifier char, stop here (do not include `component`).
      const char *kw = "component";
      size_t i = 0;
      while (kw[i] && (int32_t)kw[i] == l->lookahead) { advance(l); i++; }
      if (kw[i] == 0) {
        int32_t after = l->lookahead;
        bool ident = (after=='_'||(after>='A'&&after<='Z')||(after>='a'&&after<='z')||(after>='0'&&after<='9'));
        if (!ident) {
          // `component` keyword found at depth 0: stop BEFORE it (mark_end already set).
          return consumed;       // go_text ends just before `component`
        }
      }
      // Not the keyword (or an identifier like `components`): the consumed chars are
      // part of go_text; continue. (mark_end will be reset by the normal path below.)
      consumed = true;
      continue;
    }

    switch (c) {
      case '"': { advance(l); while (!l->eof(l) && l->lookahead!='"') { if (l->lookahead=='\\') advance(l); advance(l);} if(!l->eof(l)) advance(l); break; }
      case '`': { advance(l); while (!l->eof(l) && l->lookahead!='`') advance(l); if(!l->eof(l)) advance(l); break; }
      case '\'':{ advance(l); while (!l->eof(l) && l->lookahead!='\'') { if (l->lookahead=='\\') advance(l); advance(l);} if(!l->eof(l)) advance(l); break; }
      case '/': { advance(l); if (l->lookahead=='/') { while(!l->eof(l)&&l->lookahead!='\n') advance(l);} else if (l->lookahead=='*'){ advance(l); int32_t prev=0; while(!l->eof(l)&&!(prev=='*'&&l->lookahead=='/')){prev=l->lookahead;advance(l);} if(!l->eof(l)) advance(l);} break; }
      case '{': depth++; advance(l); break;
      case '}': if (depth>0) depth--; advance(l); break;
      default: advance(l); break;
    }
    consumed = true;
    l->mark_end(l);
  }
  l->mark_end(l);
  return consumed;
}

bool tree_sitter_gsx_external_scanner_scan(void *payload, TSLexer *l, const bool *valid) {
  if (valid[GO_TEXT]) {
    if (scan_go_text(l)) { l->result_symbol = GO_TEXT; return true; }
  }
  return false; // RAW_TEXT (Task 8), PIPE (Task 9) added later
}
```

- [ ] **Step 3: Corpus test** — `test/corpus/toplevel.txt`:
```
==================
package, import, then component
==================

package views

import "github.com/gsxhq/gsx"

component Foo() {}

---

(source_file
  (go_chunk)
  (component_declaration name: (identifier) (body)))
```

- [ ] **Step 4: Generate + test**
```bash
cd ~/personal/gsxhq/tree-sitter-gsx && npx tree-sitter generate && npx tree-sitter test
```
Expected: `toplevel.txt` and `skeleton.txt` pass. If `tree-sitter generate` reports a conflict between `go_chunk` and `component_declaration`, add the keyword `'component'` to a `word`/reserved set is not needed — the scanner stops before `component`, so no conflict; if one appears, ensure `go_text` is only valid where `_top_level` expects it.

- [ ] **Step 5: Commit**
```bash
git add -A && git commit -m "feat: external scanner + top-level Go/component split"
```

---

## Task 3: Component params/receiver + body opening; elements & text

**Files:** `grammar.js` (modify), `test/corpus/elements.txt` (create).

**Interfaces:**
- `parameter_list` and `receiver` are Go ranges (their inner text is a `go_text`-style token reused via a bracketed rule). For params/receiver, parse `'(' $.go_params ')'` where `go_params` is `$.go_text`-like but stops at the matching `)`. SIMPLIFICATION for v1: model params as `'(' optional($._paren_go) ')'` where `_paren_go` is `token(prec(-1, /[^()]*/))` (params rarely contain nested parens except func types; acceptable for highlighting — refine only if an example fails in Task 11).
- `body: '{' repeat($._node) '}'` where `_node` is the markup union, grown across Tasks 3–9.

- [ ] **Step 1: Grammar — params + element + text**
```js
    component_declaration: $ => seq(
      'component',
      optional(field('receiver', $.receiver)),
      field('name', $.identifier),
      field('parameters', $.parameter_list),
      field('body', $.body),
    ),
    receiver: $ => seq('(', optional($._paren_go), ')'),
    parameter_list: $ => seq('(', optional($._paren_go), ')'),
    _paren_go: $ => token(prec(-1, /[^()]+/)),

    body: $ => seq('{', repeat($._node), '}'),
    _node: $ => choice($.element, $.text),

    element: $ => choice($.self_closing_element, seq($.start_tag, repeat($._node), $.end_tag)),
    start_tag: $ => seq('<', field('name', $.tag_name), repeat($.attribute), '>'),
    end_tag: $ => seq('</', $.tag_name, '>'),
    self_closing_element: $ => seq('<', field('name', $.tag_name), repeat($.attribute), '/>'),
    tag_name: $ => /[A-Za-z][A-Za-z0-9.\-]*/,

    // Static attribute only for now (expr/bool/etc. in Task 5).
    attribute: $ => seq($.attribute_name, optional(seq('=', $.quoted_string))),
    attribute_name: $ => /[A-Za-z_@:][A-Za-z0-9_@:.\-]*/,
    quoted_string: $ => choice(seq('"', /[^"]*/, '"'), seq("'", /[^']*/, "'")),

    text: $ => token(prec(-1, /[^<{}>]+/)),
```

- [ ] **Step 2: Corpus test** — `test/corpus/elements.txt`:
```
==================
elements, void, self-closing, hyphenated, attrs
==================

component E() {
  <div class="card">
    <br/>
    <el-dialog open></el-dialog>
    hello
  </div>
}

---

(source_file (component_declaration name: (identifier) (parameter_list)
  (body (element (start_tag name: (tag_name) (attribute (attribute_name) (quoted_string)))
    (element (self_closing_element name: (tag_name)))
    (element (start_tag name: (tag_name) (attribute (attribute_name))) (end_tag (tag_name)))
    (text)
    (end_tag (tag_name))))))
```

- [ ] **Step 3: Generate + test; resolve conflicts**
```bash
npx tree-sitter generate && npx tree-sitter test
```
Expected: passes. tree-sitter may report that `<` is ambiguous between start/self-closing/end — resolved by the distinct `'</'` and `'/>'` tokens. If a conflict is reported, add `conflicts: $ => [[$.element]]` and re-run; iterate until clean.

- [ ] **Step 4: Commit**
```bash
git add -A && git commit -m "feat: component params + elements/text markup"
```

---

## Task 4: Component tags, fragments, DOCTYPE, comments

**Files:** `grammar.js`, `test/corpus/components_fragments.txt`.

- [ ] **Step 1: Grammar additions**
```js
    _node: $ => choice($.element, $.fragment, $.doctype, $.html_comment, $.content_comment, $.text),
    fragment: $ => seq('<>', repeat($._node), '</>'),
    doctype: $ => seq('<!', /[Dd][Oo][Cc][Tt][Yy][Pp][Ee]/, /[^>]*/, '>'),
    html_comment: $ => seq('<!--', /([^-]|-[^-]|--[^>])*/, '-->'),
    content_comment: $ => seq('{/*', /([^*]|\*[^/])*/, '*/}'),
```
`tag_name` already accepts `Card`, `ui.Button`, `p.Content` (dot + uppercase). Highlighting distinguishes component-vs-element via a query predicate (Task 11), so no separate node is required — but capture the distinction by adding a helper rule used only in queries: keep `tag_name` single, and in `highlights.scm` use `#match?` on capitalization.

- [ ] **Step 2: Corpus test** — `test/corpus/components_fragments.txt`:
```
==================
component tags, fragment, doctype, comments
==================

component C() {
  <>
    <!DOCTYPE html>
    <!-- hi -->
    <Card>{/* slot */}</Card>
    <ui.Button/>
  </>
}

---

(source_file (component_declaration name: (identifier) (parameter_list)
  (body (fragment
    (doctype)
    (html_comment)
    (element (start_tag name: (tag_name)) (content_comment) (end_tag (tag_name)))
    (element (self_closing_element name: (tag_name)))))))
```

- [ ] **Step 3: Generate + test** — `npx tree-sitter generate && npx tree-sitter test` → passes (resolve any conflicts via precedence as before).
- [ ] **Step 4: Commit** — `git add -A && git commit -m "feat: component tags, fragments, doctype, comments"`

---

## Task 5: Holes `{ expr }`, try-marker `?`, expr/bool/spread/conditional attributes

**Files:** `grammar.js`, `src/scanner.c` (extend `go_text` for hole mode), `test/corpus/holes_attrs.txt`.

**Interfaces:**
- The scanner's `go_text` must also serve **hole** content: when valid in a hole context, consume Go until the matching `}` at depth 0 (respecting strings/braces). Distinguish via the `valid` flags + a small scanner state, OR (simpler) make hole content a grammar rule that uses `go_text` which stops at top-level `}`. Implement: `go_text` stops at `}` when brace-depth would go negative (i.e. the closing brace of the hole), without consuming it.

- [ ] **Step 1: Scanner — stop go_text at hole-closing `}`** (extend `scan_go_text`): in the `'}'` case, `if (depth==0) { l->mark_end(l); return consumed; }` BEFORE the `advance`. This makes `go_text` end right before the hole's `}`. (The top-level `component` stop still applies; both are depth-0 stops.)

- [ ] **Step 2: Grammar — holes + attributes**
```js
    _node: $ => choice($.element, $.fragment, $.doctype, $.html_comment, $.content_comment, $.interpolation, $.control_flow, $.go_block, $.text),

    interpolation: $ => seq('{', $._hole_body, optional('?'), '}'),
    _hole_body: $ => $.pipeline,
    pipeline: $ => seq($.go_expr, repeat(seq($.pipe, $.go_expr))),  // $.pipe = scanner '|>' (Task 9); until then, single segment
    go_expr: $ => $.go_text,

    attribute: $ => choice(
      $.static_attribute, $.expr_attribute, $.bool_attribute, $.spread_attribute, $.conditional_attribute,
    ),
    static_attribute: $ => seq($.attribute_name, '=', $.quoted_string),
    expr_attribute: $ => seq($.attribute_name, '=', '{', $.pipeline, optional('?'), '}'),
    bool_attribute: $ => $.attribute_name,
    spread_attribute: $ => seq('{', '...', $.go_expr, '}'),
    conditional_attribute: $ => seq('{', /if|for/, $.go_expr, '{', repeat($.attribute), '}', optional(seq(/else/, '{', repeat($.attribute), '}')), '}'),
```
Note: until Task 9 implements `$.pipe`, `pipeline` is effectively `$.go_expr` (the `repeat(seq($.pipe, …))` matches zero times). Declaring `pipe` as an external (Task 2) that returns false makes it produce zero repetitions — fine.

- [ ] **Step 3: Corpus test** — `test/corpus/holes_attrs.txt`:
```
==================
holes, try-marker, expr/bool/spread attributes
==================

component H(x int) {
  <div id={x} hidden {...attrs} class="c">
    {title}
    {user.Name?}
  </div>
}

---

(source_file (component_declaration name: (identifier) (parameter_list)
  (body (element (start_tag name: (tag_name)
      (expr_attribute (attribute_name) (interpolation (pipeline (go_expr))))
      (bool_attribute (attribute_name))
      (spread_attribute (go_expr))
      (static_attribute (attribute_name) (quoted_string)))
    (interpolation (pipeline (go_expr)))
    (interpolation (pipeline (go_expr)))
    (end_tag (tag_name))))))
```
(The exact tree for `expr_attribute` may nest `interpolation` or inline `{ pipeline }`; adjust the expected S-expr to match what the grammar emits — run with `npx tree-sitter parse <file>` to see the actual tree, then pin it. Do NOT use `--update` blindly; verify the tree is correct first.)

- [ ] **Step 4: Generate + test; resolve `{`-prefixed ambiguity** — `{` now starts interpolation, spread, conditional, control_flow, and go_block (`{{`). tree-sitter will need help: make `{{` a distinct token (it already is via `go_block`), and use `conflicts`/`prec.dynamic` to disambiguate `{ ...`, `{ if`, `{ <ident>`. Run `npx tree-sitter generate`; for each reported conflict, prefer making the leading token distinct (`'...'`, `/if|for/`) over GLR conflicts. Iterate to green.

- [ ] **Step 5: Commit** — `git add -A && git commit -m "feat: holes, try-marker, expr/bool/spread/conditional attributes"`

---

## Task 6: Control flow + `{{ }}` Go blocks + the markup-vs-Go (Babel) rule

**Files:** `grammar.js`, `src/scanner.c` (Babel lookahead), `test/corpus/control_flow.txt`.

**Interfaces:**
- `control_flow: '{' (if|for|switch) goCond '{' markup '}' (else …)? '}'`.
- `go_block: '{{' goStmts '}}'`.
- **Babel rule:** a `{` followed (after optional ws) by `<` begins **markup** content (the hole holds nested markup), e.g. `{ <a/> }`; otherwise it's a Go expr. For v1 highlighting, model interpolation's body as `choice($.pipeline, repeat($._node))` and let the scanner's `go_text` naturally yield to `<` (go_text already stops? no — add: in hole mode, if the FIRST non-space char is `<`, emit zero-length go and let markup rules take over). Implement via a `prec.dynamic` + a scanner `valid`-gated check, OR accept that `{ <a/> }` parses as markup by giving `_node` higher precedence inside holes. Concretely:
```js
    interpolation: $ => seq('{', choice($.pipeline, repeat1($._node)), optional('?'), '}'),
```
tree-sitter's GLR will try both; the scanner makes `go_text` fail (consume nothing) when the next char is `<`, so the markup alternative wins. Add to `scan_go_text` start: `if (depth==0 && l->lookahead=='<') return false;` (only in hole mode — gate with a state flag set when GO_TEXT is requested inside a hole; if stateful gating is hard, a simpler rule: `go_text` never starts with `<`, which is always safe since a Go expr never begins with `<`).

- [ ] **Step 1: Scanner — go_text never starts with `<`** — at the top of `scan_go_text`, after skipping leading whitespace is NOT done by the scanner (extras handle ws), so: `if (l->lookahead=='<') return false;`. This lets `{ <a/> }` fall to the markup alternative.

- [ ] **Step 2: Grammar — control flow + go block**
```js
    control_flow: $ => seq('{', field('kw', alias(/if|for|switch/, $.keyword)), $.go_expr, $.block, repeat($.else_clause), '}'),
    block: $ => seq('{', repeat($._node), '}'),
    else_clause: $ => seq(alias(/else( if)?/, $.keyword), $.go_expr_opt, $.block),
    go_expr_opt: $ => optional($.go_text),
    go_block: $ => seq('{{', $.go_text, '}}'),
```

- [ ] **Step 3: Corpus test** — `test/corpus/control_flow.txt`:
```
==================
if/for/switch, {{ }}, markup-in-hole
==================

component F(items []int) {
  <ul>
    {{ heading := "x" }}
    { for _, it := range items { <li>{it}</li> } }
    { cond { <a/> } }
  </ul>
}

---

(source_file (component_declaration name: (identifier) (parameter_list)
  (body (element (start_tag name: (tag_name))
    (go_block (go_text))
    (control_flow (keyword) (go_expr) (block (element (start_tag name: (tag_name)) (interpolation (pipeline (go_expr))) (end_tag (tag_name)))))
    (interpolation (element (self_closing_element name: (tag_name))))
    (end_tag (tag_name))))))
```
(Pin the actual emitted tree via `npx tree-sitter parse`.)

- [ ] **Step 4: Generate + test** — iterate conflicts. Expected pass with `{ <a/> }` parsing as `interpolation` containing an `element`.
- [ ] **Step 5: Commit** — `git add -A && git commit -m "feat: control flow, {{ }} blocks, markup-in-hole (Babel rule)"`

---

## Task 7: Raw-text `<script>` / `<style>` + `@{ }` holes

**Files:** `grammar.js`, `src/scanner.c` (raw_text + `@{` hole scan), `test/corpus/raw_text.txt`.

**Interfaces:**
- `raw_element: start_tag raw_content end_tag` where the tag name is `script` or `style`. `raw_content` is a sequence of `raw_text` (scanner token) and `js_hole`/`css_hole` (`@{ go }`).
- The scanner's `raw_text` consumes bytes until either `@{` (start of a hole) or `</script`/`</style` (case-insensitive close), without consuming the delimiter. The grammar wraps the hole: `at_hole: '@{' $.go_text '}'` (reusing `go_text` hole mode, which stops at `}`).

- [ ] **Step 1: Scanner — implement RAW_TEXT** (in `scan` add, gated by `valid[RAW_TEXT]`): consume until `@{` or `</` (lowercase/uppercase s/t) ahead, respecting nothing else (raw). Stop before the delimiter; require ≥1 byte. Pseudocode in C:
```c
static bool scan_raw_text(TSLexer *l) {
  bool consumed = false;
  while (!l->eof(l)) {
    if (l->lookahead == '@') { l->mark_end(l); advance(l); if (l->lookahead=='{') return consumed; consumed=true; continue; }
    if (l->lookahead == '<') { l->mark_end(l); advance(l); if (l->lookahead=='/') return consumed; consumed=true; continue; }
    advance(l); consumed = true; l->mark_end(l);
  }
  l->mark_end(l); return consumed;
}
```
Wire into `scan`: `if (valid[RAW_TEXT]) { if (scan_raw_text(l)) { l->result_symbol = RAW_TEXT; return true; } }` (place BEFORE GO_TEXT so raw context wins).

- [ ] **Step 2: Grammar — raw element**
```js
    _node: $ => choice($.element, $.raw_element, /* …rest… */),
    raw_element: $ => seq(
      seq('<', field('name', alias(/[Ss][Cc][Rr][Ii][Pp][Tt]|[Ss][Tt][Yy][Ll][Ee]/, $.tag_name)), repeat($.attribute), '>'),
      repeat(choice($.raw_text, $.at_hole)),
      seq('</', alias(/[A-Za-z]+/, $.tag_name), '>'),
    ),
    at_hole: $ => seq('@{', $.pipeline, '}'),
```
Ensure `raw_element` is tried before `element` for script/style (use `prec` or token-level alias so the raw name matches first).

- [ ] **Step 3: Corpus test** — `test/corpus/raw_text.txt`:
```
==================
script and style with @{ } holes
==================

component R(color string) {
  <style>.a { color: @{ color }; }</style>
  <script>const n = @{ count };</script>
}

---

(source_file (component_declaration name: (identifier) (parameter_list)
  (body
    (raw_element name: (tag_name) (raw_text) (at_hole (pipeline (go_expr))) (raw_text) (tag_name))
    (raw_element name: (tag_name) (raw_text) (at_hole (pipeline (go_expr))) (raw_text) (tag_name)))))
```

- [ ] **Step 4: Generate + test** — verify raw text is NOT parsed as markup and `@{ }` holes are carved. Iterate.
- [ ] **Step 5: Commit** — `git add -A && git commit -m "feat: raw-text script/style with @{ } holes"`

---

## Task 8: Pipeline `|>` splitting

**Files:** `src/scanner.c` (PIPE token), `grammar.js` (already references `$.pipe`), `test/corpus/pipeline.txt`.

**Interfaces:**
- `$.pipe` (external) matches `|>` ONLY at hole brace-depth 0 (not inside nested parens/braces/strings of a filter arg). Because `go_text` stops at a depth-0 `|`, the scanner emits PIPE when it sees `|>` at depth 0; `go_text` must also stop before a depth-0 `|>`.

- [ ] **Step 1: Scanner — stop go_text before depth-0 `|>`, and emit PIPE** — in `scan_go_text`, in the default loop, before `advance`, add: `if (depth==0 && c=='|') { /* peek next */ l->mark_end(l); advance(l); if (l->lookahead=='>') { /* this is a pipe; but go_text must stop BEFORE it */ ... }}`. Cleanest: `go_text` treats a depth-0 `|` (followed by `>`) as a stop (like `}`), returning what it consumed. Then a separate `scan_pipe` (gated by `valid[PIPE]`) consumes exactly `|>`:
```c
static bool scan_pipe(TSLexer *l) {
  if (l->lookahead != '|') return false; advance(l);
  if (l->lookahead != '>') return false; advance(l);
  l->mark_end(l); return true;
}
```
Wire: check PIPE before GO_TEXT when `valid[PIPE]`. And in `scan_go_text`, add a depth-0 stop: when `c=='|'`, mark_end and `return consumed` (do not consume) so PIPE can match next.

- [ ] **Step 2: Corpus test** — `test/corpus/pipeline.txt`:
```
==================
pipeline in hole and attr
==================

component P(name string) {
  <a title={name |> upper}>{ name |> upper |> truncate(20) }</a>
}

---

(source_file (component_declaration name: (identifier) (parameter_list)
  (body (element (start_tag name: (tag_name)
      (expr_attribute (attribute_name) (interpolation (pipeline (go_expr) (pipe) (go_expr)))))
    (interpolation (pipeline (go_expr) (pipe) (go_expr) (pipe) (go_expr)))
    (end_tag (tag_name))))))
```

- [ ] **Step 3: Generate + test** — verify `truncate(20)` (parens) does NOT split, only top-level `|>` splits. Iterate.
- [ ] **Step 4: Commit** — `git add -A && git commit -m "feat: |> pipeline splitting in holes/attrs"`

---

## Task 9: Injections

**Files:** `queries/injections.scm` (create), `test/corpus/` (no new parse tests; injection verified by highlight test in Task 10).

- [ ] **Step 1: Write `queries/injections.scm`**
```scheme
; File-level Go and all Go expression holes → the Go grammar.
((go_chunk) @injection.content
 (#set! injection.language "go"))

((go_expr) @injection.content
 (#set! injection.language "go"))

((go_text) @injection.content
 (#set! injection.language "go"))

((parameter_list) @injection.content
 (#set! injection.language "go"))

((receiver) @injection.content
 (#set! injection.language "go"))

; <script> raw text runs → JavaScript, stitched across @{ } holes.
(raw_element
  name: (tag_name) @_n
  (raw_text) @injection.content
  (#match? @_n "^[Ss][Cc][Rr][Ii][Pp][Tt]$")
  (#set! injection.language "javascript")
  (#set! injection.combined))

; <style> raw text runs → CSS, stitched across @{ } holes.
(raw_element
  name: (tag_name) @_n
  (raw_text) @injection.content
  (#match? @_n "^[Ss][Tt][Yy][Ll][Ee]$")
  (#set! injection.language "css")
  (#set! injection.combined))
```
Note: `go_expr` (pipeline segments) and `go_text` both inject Go; each segment is a separate `go_expr`, so `|>` (the `pipe` token, outside `go_expr`) is never inside an injected range. JS/CSS-context attribute injection (`x-data`, `style=`) is deferred to a follow-up (note in README) — v1 covers `<script>`/`<style>` bodies + all Go.

- [ ] **Step 2: Verify injection parse** — `npx tree-sitter query queries/injections.scm test/examples/02_text_escaping.gsx` runs without query errors (after Task 11 copies examples) — for now run against `test/corpus` inputs via a scratch `.gsx`. Expected: no "invalid node type" errors.
- [ ] **Step 3: Commit** — `git add -A && git commit -m "feat: injections (go / javascript-combined / css-combined)"`

---

## Task 10: Highlights

**Files:** `queries/highlights.scm` (create), `test/highlight/basic.gsx` + assertions.

- [ ] **Step 1: Write `queries/highlights.scm`**
```scheme
"component" @keyword
(keyword) @keyword

(component_declaration name: (identifier) @function)

; Element vs component tag by capitalization / dotting.
((tag_name) @tag (#match? @tag "^[a-z]"))
((tag_name) @type (#match? @tag "^[A-Z]"))
((tag_name) @type (#match? @tag "\\."))

(attribute_name) @attribute
(quoted_string) @string

(pipe) @operator
"?" @operator

"{" @punctuation.special
"}" @punctuation.special
"@{" @punctuation.special
"{{" @punctuation.special
"}}" @punctuation.special
"<>" @tag
"</>" @tag
"<" @punctuation.bracket
">" @punctuation.bracket
"/>" @punctuation.bracket
"</" @punctuation.bracket

(line_comment) @comment
(block_comment) @comment
(html_comment) @comment
(content_comment) @comment
(doctype) @keyword
```

- [ ] **Step 2: Highlight test** — Create `test/highlight/basic.gsx` with tree-sitter highlight assertions (the `; <- ` / `; ^` comment syntax):
```gsx
component Card(title string) {
; <- keyword
  <div class="c">{ title |> upper }</div>
}
```
Run: `npx tree-sitter highlight test/highlight/basic.gsx` — Expected: `component`→keyword, `Card`→function, `div`→tag, `class`→attribute, `|>`→operator, no ERROR. (Full assertion-comment tests are nice-to-have; at minimum confirm the command emits highlights with no parse error.)

- [ ] **Step 3: Commit** — `git add -A && git commit -m "feat: highlights.scm"`

---

## Task 11: Parse oracle — the 12 gsx examples

**Files:** `test/examples/*.gsx` (copy), `grammar.js`/`src/scanner.c` (fix gaps), `test/corpus/` (regressions).

- [ ] **Step 1: Copy the examples**
```bash
cp ~/personal/gsxhq/gsx/examples/*.gsx ~/personal/gsxhq/tree-sitter-gsx/test/examples/
```

- [ ] **Step 2: Parse each, assert zero ERROR/MISSING**
```bash
cd ~/personal/gsxhq/tree-sitter-gsx
for f in test/examples/*.gsx; do echo "== $f =="; npx tree-sitter parse -q "$f" || echo "FAILED: $f"; done
```
Run: `npx tree-sitter parse test/examples/06_corner_cases.gsx` and inspect for `(ERROR …)`/`(MISSING …)`. Expected: none. `-q` exits non-zero on error.

- [ ] **Step 3: Fix grammar/scanner gaps** — for each example that errors, identify the construct (likely candidates: `a < b` Go comparison inside a hole vs markup; multiline attribute values; `class={ a, "x": cond }` composable-class commas; `@click`/`:class`/`hx-on::click` attr names; raw `<script>` containing `}`/`<`). Add corpus regression tests reproducing the failure, then adjust `grammar.js`/`scanner.c`. The attribute-name regex must already cover `@`, `:`, `_`, `.`, `-`. For `a < b` in a hole: `go_text` consumes `<` mid-expression (only a LEADING `<` yields to markup), so `{ a < b }` stays Go — verify.

- [ ] **Step 4: Add an examples-parse CI check** — append to `.github/workflows/ci.yml` after `tree-sitter test`:
```yaml
      - name: Parse examples (zero errors)
        run: for f in test/examples/*.gsx; do npx tree-sitter parse -q "$f"; done
```

- [ ] **Step 5: Commit** — `git add -A && git commit -m "test: parse the 12 gsx examples (oracle), fix grammar gaps"`

---

## Task 12: README + final pass

**Files:** `README.md`.

- [ ] **Step 1: Write `README.md`**
```markdown
# tree-sitter-gsx

A [tree-sitter](https://tree-sitter.github.io) grammar for the
[gsx](https://github.com/gsxhq/gsx) templating language — syntax highlighting across
Go, HTML, JavaScript, and CSS in `.gsx` files, including gsx's `{ }` Go holes,
`@{ }` JS/CSS holes, and the `|>` pipeline.

## How it works
gsx structure is parsed natively; the base languages are delegated via tree-sitter
**injection**: `go` (file-level Go + every `{ }`/`@{ }` hole, each `|>` segment
separately), `javascript` (combined, over `<script>` bodies), `css` (combined, over
`<style>` bodies). See `queries/injections.scm`.

## Develop
```bash
npm install
npx tree-sitter generate
npx tree-sitter test
```

## Status
v1: highlighting for Neovim/Helix/Zed and GitHub. Deferred: indents/folds/locals,
JS/CSS-context **attribute** injection (`x-data`, `style=`), npm/crate bindings,
nvim-treesitter / Linguist submission, VS Code.

The grammar re-implements gsx's boundary rules independently of the Go parser; the
`test/examples/` corpus (synced from gsx `examples/`) keeps them in agreement.
```

- [ ] **Step 2: Full green run**
```bash
cd ~/personal/gsxhq/tree-sitter-gsx && npx tree-sitter generate && npx tree-sitter test && for f in test/examples/*.gsx; do npx tree-sitter parse -q "$f"; done && echo ALL_GREEN
```
Expected: `ALL_GREEN`.

- [ ] **Step 3: Commit** — `git add -A && git commit -m "docs: README"`

---

## Self-Review

**Spec coverage:**
- Repo layout (grammar.js, scanner.c, generated src, queries, tests, config, README, CI) → Tasks 1–12. ✅
- Native parsing of gsx structure (components, elements, component tags, fragments, holes, control flow, raw text, pipeline) → Tasks 3–8. ✅
- External scanner (goExprEnd hole-end, top-level `|>`, raw text, Babel `<`-lookahead) → Tasks 2, 5, 6, 7, 8. ✅
- Injections go/javascript-combined/css-combined → Task 9. ✅
- Highlights → Task 10. ✅
- Testing: corpus per construct + 12-example oracle (zero ERROR) + CI → every task + Task 11. ✅
- Deferred items (indents/folds, attr-context JS/CSS injection, bindings, submission, VS Code) → noted in README (Task 12), not built. ✅

**Placeholder scan:** Grammar/scanner are given as concrete starting snippets; tasks explicitly call out the tree-sitter iterate-on-conflict workflow (generate → resolve → test) and say to pin expected S-expressions via `tree-sitter parse` rather than blind `--update`. This is the genuine workflow, not deferred work. The deterministic artifacts (config, injections.scm, highlights.scm, scanner core, CI) are complete.

**Type/name consistency:** node names are consistent across tasks and queries — `go_chunk`, `go_text`, `go_expr`, `pipeline`, `pipe`, `interpolation`, `expr_attribute`, `raw_element`, `raw_text`, `at_hole`, `tag_name`, `attribute_name`, `control_flow`, `go_block`, `keyword`. `injections.scm` (Task 9) and `highlights.scm` (Task 10) reference only nodes defined in Tasks 2–8.

**Note on realism:** tree-sitter grammars are tuned by running `generate`/`test` and resolving GLR conflicts; the grammar snippets are starting points the implementer iterates to green within each task. Each task's *corpus test* is the precise, verifiable contract; the snippet is the means.
