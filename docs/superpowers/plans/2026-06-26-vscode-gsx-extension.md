# vscode-gsx Extension Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A VS Code extension (`vscode-gsx`) giving `.gsx` files TextMate syntax highlighting plus a language client that runs `gsx lsp` (diagnostics, go-to-definition, hover, references, formatting), distributed as a local `.vsix` and, on tag, to the Marketplace + Open VSX.

**Architecture:** A standalone TypeScript extension in a new sibling repo. A coarse, templ-style TextMate grammar (authored as YAML, generated to JSON) colors gsx structure and delegates Go/JS/CSS regions to VS Code's bundled grammars via `embeddedLanguages`. A thin `vscode-languageclient` spawns `gsx lsp` over stdio; all language intelligence (including formatting) comes from the server. A gopls-style binary manager resolves/installs `gsx`. esbuild bundles; `@vscode/vsce` packages.

**Tech Stack:** TypeScript, `@vscode/vsce`, esbuild, `vscode-languageclient`, `js-yaml` (grammar gen), `vscode-tmgrammar-test` (grammar tests), `vitest` (unit tests), `@vscode/test-electron` + `mocha` (integration smoke), GitHub Actions.

## Global Constraints

Copied verbatim from the spec (`docs/superpowers/specs/2026-06-26-vscode-gsx-extension-design.md`); every task inherits these.

- **Repo:** a new, standalone git repo at `/Users/jackieli/personal/gsxhq/vscode-gsx` (sibling to `gsx`, `tree-sitter-gsx`, `vite-plugin-gsx`). All extension files live there. This plan/spec stays in the `gsx` repo's `docs/superpowers/`.
- **Identity:** publisher `gsxhq`, extension name `gsx` → Marketplace id `gsxhq.gsx`. Display name `gsx`.
- **Engine:** VS Code `^1.85`.
- **No formatter code in the extension.** Formatting is the LSP's `textDocument/formatting` → `gsx fmt`. Never parse/format gsx/Go/JS/CSS in the extension.
- **No bundled gsx binary.** gopls pattern: resolve `gsx.server.path` → `PATH` → `GOBIN` → `GOPATH/bin`; if missing, offer **Install gsx** running `go install github.com/gsxhq/gsx/cmd/gsx@latest`.
- **Highlighting is a coarse TextMate grammar** (templ-style): color structure + delegate embedded Go/JS/CSS; lean on the LSP for semantics. Do **not** reimplement tree-sitter. Author as `syntaxes/gsx.tmLanguage.src.yaml`; **generate** the committed `syntaxes/gsx.tmLanguage.json`; CI asserts no drift.
- **LSP launch:** `vscode-languageclient/node`, `serverOptions` spawn `{ command: <gsx path>, args: ["lsp"] }` over stdio; `documentSelector: [{ language: "gsx" }]`. Server already advertises `definitionProvider`, `referencesProvider`, `documentFormattingProvider`, `hoverProvider`, and publishes diagnostics — no per-feature client code.
- **Settings:** `gsx.server.path` (string, default `""`); `gsx.trace.server` (enum `off`/`messages`/`verbose`, default `off`).
- **Commands:** `gsx.installServer` (run the go install), `gsx.restartServer` (restart the client).
- **Distribution (local-first):** `npm run package` → `.vsix` for local install/testing; a `v*` git tag triggers CI publish to Marketplace (`vsce publish`) + Open VSX (`ovsx publish`) using `VSCE_PAT` / `OVSX_PAT` secrets. Nothing publishes without a tag.

---

## File Structure

```
vscode-gsx/                        # NEW standalone repo
  package.json                     # manifest: contributes languages/grammars/config/commands; scripts; deps
  language-configuration.json      # brackets / autoclose / comments
  tsconfig.json
  esbuild.mjs                      # bundle src/extension.ts → dist/extension.js
  .gitignore , .vscodeignore
  syntaxes/
    gsx.tmLanguage.src.yaml        # AUTHORED coarse grammar
    gsx.tmLanguage.json            # GENERATED (committed) from the YAML
  scripts/
    build-grammar.mjs              # js-yaml: src.yaml → .json
  src/
    extension.ts                   # activate/deactivate + LanguageClient + commands
    gsxBinary.ts                   # pure resolve logic + vscode-aware wrappers
  test/
    grammar/                       # vscode-tmgrammar-test fixtures (.gsx with scope assertions)
    unit/gsxBinary.test.ts         # vitest unit tests (pure resolver)
    integration/                   # @vscode/test-electron + mocha smoke
      runTest.ts , suite/index.ts , suite/extension.test.ts
  icons/gsx.png                    # 128px marketplace icon (from gsx favicon)
  .github/workflows/ci.yml         # PR: gen grammar (+no-drift), typecheck, lint, grammar+unit tests, package artifact
  .github/workflows/release.yml    # tag v*: vsce publish + ovsx publish
  README.md , CHANGELOG.md , LICENSE
```

Responsibilities: `gsxBinary.ts` resolves/installs the binary (pure logic separated for unit testing). `extension.ts` wires the LanguageClient + commands + settings. The grammar (YAML→JSON) is highlighting only. Each is independently testable.

---

## Task 1: Scaffold repo, manifest, language registration, build

**Files:**
- Create: `/Users/jackieli/personal/gsxhq/vscode-gsx/package.json`
- Create: `/Users/jackieli/personal/gsxhq/vscode-gsx/language-configuration.json`
- Create: `/Users/jackieli/personal/gsxhq/vscode-gsx/tsconfig.json`
- Create: `/Users/jackieli/personal/gsxhq/vscode-gsx/esbuild.mjs`
- Create: `/Users/jackieli/personal/gsxhq/vscode-gsx/.gitignore`, `.vscodeignore`
- Create: `/Users/jackieli/personal/gsxhq/vscode-gsx/src/extension.ts` (minimal)
- Test: `/Users/jackieli/personal/gsxhq/vscode-gsx/test/unit/manifest.test.ts`

**Interfaces:**
- Produces: a buildable extension whose `package.json` `contributes.languages` declares language id `gsx` for `.gsx`; `npm run compile` and `npm run package` succeed. Later tasks add `contributes.grammars`/`configuration`/`commands` and flesh out `src/extension.ts`.

- [ ] **Step 1: Init the repo**

```bash
mkdir -p /Users/jackieli/personal/gsxhq/vscode-gsx
cd /Users/jackieli/personal/gsxhq/vscode-gsx
git init
mkdir -p src test/unit syntaxes scripts icons
```

- [ ] **Step 2: Write the failing manifest test**

Create `test/unit/manifest.test.ts`:

```ts
import { describe, it, expect } from 'vitest'
import pkg from '../../package.json'

describe('package.json manifest', () => {
  it('declares the gsx language for .gsx', () => {
    const langs = pkg.contributes?.languages ?? []
    const gsx = langs.find((l: any) => l.id === 'gsx')
    expect(gsx, 'a language with id "gsx"').toBeTruthy()
    expect(gsx.extensions).toContain('.gsx')
    expect(gsx.configuration).toBe('./language-configuration.json')
  })
  it('targets the agreed VS Code engine and identity', () => {
    expect(pkg.engines.vscode).toBe('^1.85.0')
    expect(pkg.publisher).toBe('gsxhq')
    expect(pkg.name).toBe('gsx')
  })
})
```

- [ ] **Step 3: Run it — fails (no package.json)**

Run: `cd /Users/jackieli/personal/gsxhq/vscode-gsx && npx vitest run` after Step 4 installs deps; before deps exist it errors on missing module. Expected at this point: FAIL.

- [ ] **Step 4: Write `package.json`**

```json
{
  "name": "gsx",
  "displayName": "gsx",
  "description": "Syntax highlighting and language support for gsx (.gsx) — JSX-style HTML templating for Go.",
  "version": "0.0.1",
  "publisher": "gsxhq",
  "license": "MIT",
  "repository": { "type": "git", "url": "https://github.com/gsxhq/vscode-gsx" },
  "icon": "icons/gsx.png",
  "engines": { "vscode": "^1.85.0" },
  "categories": ["Programming Languages"],
  "main": "./dist/extension.js",
  "contributes": {
    "languages": [
      {
        "id": "gsx",
        "aliases": ["gsx"],
        "extensions": [".gsx"],
        "configuration": "./language-configuration.json"
      }
    ]
  },
  "scripts": {
    "gen:grammar": "node scripts/build-grammar.mjs",
    "compile": "node esbuild.mjs",
    "watch": "node esbuild.mjs --watch",
    "typecheck": "tsc --noEmit",
    "lint": "eslint src --ext ts",
    "test:unit": "vitest run",
    "test:grammar": "vscode-tmgrammar-test \"test/grammar/**/*.gsx\"",
    "vscode:prepublish": "npm run gen:grammar && npm run compile",
    "package": "npm run gen:grammar && vsce package"
  },
  "devDependencies": {
    "@types/node": "^20.0.0",
    "@types/vscode": "^1.85.0",
    "@vscode/vsce": "^3.0.0",
    "esbuild": "^0.23.0",
    "eslint": "^9.0.0",
    "js-yaml": "^4.1.0",
    "ovsx": "^0.9.0",
    "typescript": "^5.5.0",
    "vitest": "^2.0.0",
    "vscode-tmgrammar-test": "^0.1.3"
  },
  "dependencies": {
    "vscode-languageclient": "^9.0.1"
  }
}
```

- [ ] **Step 5: Write `language-configuration.json`**

```json
{
  "comments": { "lineComment": "//", "blockComment": ["/*", "*/"] },
  "brackets": [["<", ">"], ["{", "}"], ["(", ")"], ["[", "]"]],
  "autoClosingPairs": [
    { "open": "{", "close": "}" },
    { "open": "[", "close": "]" },
    { "open": "(", "close": ")" },
    { "open": "\"", "close": "\"", "notIn": ["string"] },
    { "open": "`", "close": "`", "notIn": ["string"] }
  ],
  "surroundingPairs": [["<", ">"], ["{", "}"], ["(", ")"], ["[", "]"], ["\"", "\""], ["`", "`"]],
  "autoCloseBefore": ";:.,=}])>` \n\t"
}
```

- [ ] **Step 6: Write `tsconfig.json`**

```json
{
  "compilerOptions": {
    "module": "Node16",
    "moduleResolution": "Node16",
    "target": "ES2022",
    "lib": ["ES2022"],
    "outDir": "dist",
    "strict": true,
    "esModuleInterop": true,
    "resolveJsonModule": true,
    "skipLibCheck": true
  },
  "include": ["src", "test"]
}
```

- [ ] **Step 7: Write `esbuild.mjs`**

```js
import esbuild from 'esbuild'

const watch = process.argv.includes('--watch')
const ctx = await esbuild.context({
  entryPoints: ['src/extension.ts'],
  bundle: true,
  outfile: 'dist/extension.js',
  external: ['vscode'],            // provided by the VS Code runtime
  format: 'cjs',
  platform: 'node',
  target: 'node18',
  sourcemap: true,
})
if (watch) { await ctx.watch() } else { await ctx.rebuild(); await ctx.dispose() }
```

- [ ] **Step 8: Write minimal `src/extension.ts`**

```ts
import * as vscode from 'vscode'

export function activate(_context: vscode.ExtensionContext): void {
  // LanguageClient wiring is added in Task 4.
  console.log('gsx extension activated')
}

export function deactivate(): void {}
```

- [ ] **Step 9: Write `.gitignore` and `.vscodeignore`**

`.gitignore`:
```
node_modules/
dist/
*.vsix
.vscode-test/
```

`.vscodeignore`:
```
.github/
test/
src/
syntaxes/*.src.yaml
scripts/
esbuild.mjs
tsconfig.json
**/*.map
.gitignore
.vscode-test/
node_modules/
!dist/
```

- [ ] **Step 10: Install + run the test (now passes)**

Run:
```bash
cd /Users/jackieli/personal/gsxhq/vscode-gsx
npm install
npm run typecheck
npm run compile
npm run test:unit
```
Expected: typecheck clean, `dist/extension.js` produced, `manifest.test.ts` 2/2 PASS.

- [ ] **Step 11: Commit**

```bash
cd /Users/jackieli/personal/gsxhq/vscode-gsx
git add -A
git commit -m "scaffold: vscode-gsx extension — manifest, gsx language, esbuild build"
```

---

## Task 2: TextMate grammar (YAML source + generator) + snapshot tests

**Files:**
- Create: `/Users/jackieli/personal/gsxhq/vscode-gsx/scripts/build-grammar.mjs`
- Create: `/Users/jackieli/personal/gsxhq/vscode-gsx/syntaxes/gsx.tmLanguage.src.yaml`
- Create: `/Users/jackieli/personal/gsxhq/vscode-gsx/syntaxes/gsx.tmLanguage.json` (generated)
- Modify: `/Users/jackieli/personal/gsxhq/vscode-gsx/package.json` (`contributes.grammars` + `embeddedLanguages`)
- Test: `/Users/jackieli/personal/gsxhq/vscode-gsx/test/grammar/basic.gsx`, `embedded.gsx`

**Interfaces:**
- Consumes: the Task 1 manifest.
- Produces: scope `source.gsx`; `embeddedLanguages` mapping `meta.embedded.block.go`→`go`, `meta.embedded.block.js`→`javascript`, `meta.embedded.block.css`→`css`. Grammar coloring verified by snapshot tests on gsx-owned scopes.

- [ ] **Step 1: Write the grammar generator**

Create `scripts/build-grammar.mjs`:

```js
// Generate syntaxes/gsx.tmLanguage.json from the authored YAML source.
import { readFileSync, writeFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { dirname, join } from 'node:path'
import yaml from 'js-yaml'

const root = join(dirname(fileURLToPath(import.meta.url)), '..')
const src = join(root, 'syntaxes', 'gsx.tmLanguage.src.yaml')
const out = join(root, 'syntaxes', 'gsx.tmLanguage.json')
const grammar = yaml.load(readFileSync(src, 'utf8'))
writeFileSync(out, JSON.stringify(grammar, null, 2) + '\n')
console.log(`generated ${out}`)
```

- [ ] **Step 2: Write the grammar source (coarse, templ-style)**

Create `syntaxes/gsx.tmLanguage.src.yaml`:

```yaml
# Coarse TextMate grammar for gsx. AUTHORED here; gsx.tmLanguage.json is generated
# (npm run gen:grammar). Philosophy: color structure, delegate embedded Go/JS/CSS,
# lean on the gsx LSP for semantic accuracy. Keep coarse — do NOT reimplement
# tree-sitter-gsx. Component bodies are matched against canonical (gsx fmt) layout:
# the opening `{` ends the signature line, the closing `}` sits alone on a line.
scopeName: source.gsx
name: gsx
patterns:
  - include: '#comments'
  - include: '#component'
  - include: source.go
repository:
  comments:
    patterns:
      - name: comment.line.double-slash.gsx
        match: '//.*$'
      - name: comment.block.gsx
        begin: '/\*'
        end: '\*/'
      - name: comment.block.html.gsx
        begin: '<!--'
        end: '-->'
  component:
    name: meta.component.gsx
    begin: '^(component)\s+(?:\(([^)]*)\)\s+)?([A-Za-z_][A-Za-z0-9_]*)\s*\(([^{]*)\)\s*(\{)\s*$'
    beginCaptures:
      '1': { name: keyword.control.component.gsx }
      '2': { name: meta.embedded.line.go.gsx, patterns: [{ include: source.go }] }
      '3': { name: entity.name.function.gsx }
      '4': { name: meta.embedded.line.go.gsx, patterns: [{ include: source.go }] }
      '5': { name: punctuation.section.block.begin.gsx }
    end: '^(\})\s*$'
    endCaptures:
      '1': { name: punctuation.section.block.end.gsx }
    patterns:
      - include: '#markup'
  markup:
    patterns:
      - include: '#comments'
      - include: '#go-block'
      - include: '#script-element'
      - include: '#style-element'
      - include: '#doctype'
      - include: '#fragment'
      - include: '#component-tag'
      - include: '#element-tag'
      - include: '#interp'
  go-block:
    name: meta.embedded.block.go.gsx
    begin: '\{\{'
    beginCaptures: { '0': { name: punctuation.section.embedded.begin.gsx } }
    end: '\}\}'
    endCaptures: { '0': { name: punctuation.section.embedded.end.gsx } }
    contentName: source.go
    patterns: [{ include: source.go }]
  interp:
    name: meta.embedded.block.go.gsx
    begin: '(@\{|\{)'
    beginCaptures: { '1': { name: punctuation.section.embedded.begin.gsx } }
    end: '\}'
    endCaptures: { '0': { name: punctuation.section.embedded.end.gsx } }
    contentName: source.go
    patterns: [{ include: source.go }]
  doctype:
    name: meta.tag.doctype.gsx
    match: '(?i)(<!)(DOCTYPE)([^>]*)(>)'
    captures:
      '1': { name: punctuation.definition.tag.gsx }
      '2': { name: keyword.other.doctype.gsx }
      '4': { name: punctuation.definition.tag.gsx }
  fragment:
    name: meta.tag.fragment.gsx
    match: '(</?>)'
    captures: { '1': { name: punctuation.definition.tag.gsx } }
  component-tag:
    # Uppercase-initial (Card) or dotted (ui.Button, nav.Link) → component.
    name: meta.tag.component.gsx
    begin: '(</?)((?:[a-z][a-zA-Z0-9]*\.)?[A-Z][A-Za-z0-9.]*)'
    beginCaptures:
      '1': { name: punctuation.definition.tag.gsx }
      '2': { name: entity.name.type.component.gsx }
    end: '(/?>)'
    endCaptures: { '1': { name: punctuation.definition.tag.gsx } }
    patterns: [{ include: '#attribute' }]
  element-tag:
    # Lowercase/hyphenated → native element.
    name: meta.tag.element.gsx
    begin: '(</?)([a-z][a-z0-9-]*)'
    beginCaptures:
      '1': { name: punctuation.definition.tag.gsx }
      '2': { name: entity.name.tag.gsx }
    end: '(/?>)'
    endCaptures: { '1': { name: punctuation.definition.tag.gsx } }
    patterns: [{ include: '#attribute' }]
  attribute:
    patterns:
      - include: '#go-block'
      - include: '#interp'
      - name: string.quoted.double.gsx
        begin: '"'
        end: '"'
      - name: keyword.operator.assignment.gsx
        match: '='
      - name: entity.other.attribute-name.gsx
        match: '[A-Za-z_@:][A-Za-z0-9_.:-]*'
  script-element:
    begin: '(<)(script)\b([^>]*)(>)'
    beginCaptures:
      '1': { name: punctuation.definition.tag.gsx }
      '2': { name: entity.name.tag.gsx }
      '4': { name: punctuation.definition.tag.gsx }
    end: '(</)(script)\s*(>)'
    endCaptures:
      '1': { name: punctuation.definition.tag.gsx }
      '2': { name: entity.name.tag.gsx }
      '3': { name: punctuation.definition.tag.gsx }
    contentName: meta.embedded.block.js.gsx
    patterns: [{ include: source.js }]
  style-element:
    begin: '(<)(style)\b([^>]*)(>)'
    beginCaptures:
      '1': { name: punctuation.definition.tag.gsx }
      '2': { name: entity.name.tag.gsx }
      '4': { name: punctuation.definition.tag.gsx }
    end: '(</)(style)\s*(>)'
    endCaptures:
      '1': { name: punctuation.definition.tag.gsx }
      '2': { name: entity.name.tag.gsx }
      '3': { name: punctuation.definition.tag.gsx }
    contentName: meta.embedded.block.css.gsx
    patterns: [{ include: source.css }]
```

- [ ] **Step 3: Generate the JSON and wire the grammar contribution**

Run: `cd /Users/jackieli/personal/gsxhq/vscode-gsx && npm run gen:grammar`
Expected: writes `syntaxes/gsx.tmLanguage.json`.

Then add to `package.json` `contributes` (after `languages`):

```json
    "grammars": [
      {
        "language": "gsx",
        "scopeName": "source.gsx",
        "path": "./syntaxes/gsx.tmLanguage.json",
        "embeddedLanguages": {
          "meta.embedded.block.go.gsx": "go",
          "meta.embedded.line.go.gsx": "go",
          "meta.embedded.block.js.gsx": "javascript",
          "meta.embedded.block.css.gsx": "css"
        }
      }
    ]
```

- [ ] **Step 4: Write failing grammar snapshot fixtures**

`vscode-tmgrammar-test` reads scope assertions written as gsx comments. Assert **gsx-owned** scopes only (not inner Go/JS/CSS tokens, which depend on external grammars).

Create `test/grammar/basic.gsx`:

```
// SYNTAX TEST "source.gsx" "gsx structure"
package views

component Card(title string) {
// <- keyword.control.component.gsx
//        ^ entity.name.function.gsx
	<div class="card">
//   ^ entity.name.tag.gsx
//       ^ entity.other.attribute-name.gsx
		<Button label="x"/>
//   ^ entity.name.type.component.gsx
		{ title }
//   ^ punctuation.section.embedded.begin.gsx
	</div>
}
```

Create `test/grammar/embedded.gsx`:

```
// SYNTAX TEST "source.gsx" "embedded js/css"
package views

component Page() {
	<style>
// ^ entity.name.tag.gsx
	.a { color: red }
	</style>
	<script>
	const x = 1
	</script>
}
```

(The `// <-` and `// ^` lines are tmgrammar-test assertions pinning a scope at a column; adjust carets to the exact columns when first run.)

- [ ] **Step 5: Run grammar tests — fail, then pass**

Run: `cd /Users/jackieli/personal/gsxhq/vscode-gsx && npm run test:grammar`
First run before the grammar is correct: FAIL (scope mismatches). Adjust the grammar/caret columns until: PASS. Each assertion must match the scope the engine actually applies — fix the **grammar** when a gsx-owned scope is wrong; fix the **caret column** when only the position is off.

- [ ] **Step 6: Commit**

```bash
cd /Users/jackieli/personal/gsxhq/vscode-gsx
git add -A
git commit -m "grammar: coarse gsx TextMate grammar (YAML→JSON) + embedded go/js/css + snapshot tests"
```

---

## Task 3: Binary manager (`gsxBinary.ts`) + unit tests

**Files:**
- Create: `/Users/jackieli/personal/gsxhq/vscode-gsx/src/gsxBinary.ts`
- Test: `/Users/jackieli/personal/gsxhq/vscode-gsx/test/unit/gsxBinary.test.ts`

**Interfaces:**
- Produces:
  - `type ResolveEnv = { configuredPath: string; pathDirs: string[]; goBin?: string; goPath?: string; isExecutable: (p: string) => boolean }`
  - `function resolveGsxPath(env: ResolveEnv): string | null` — returns the first existing `gsx` per order (configured → PATH → GOBIN → GOPATH/bin), or `null`.
  - `const GSX_INSTALL_CMD = "go install github.com/gsxhq/gsx/cmd/gsx@latest"`
- Pure (no `vscode` import) so it unit-tests under vitest. Task 4's `extension.ts` builds `ResolveEnv` from the VS Code/runtime environment and calls it.

- [ ] **Step 1: Write the failing test**

Create `test/unit/gsxBinary.test.ts`:

```ts
import { describe, it, expect } from 'vitest'
import { resolveGsxPath, GSX_INSTALL_CMD, type ResolveEnv } from '../../src/gsxBinary'

const base = (over: Partial<ResolveEnv> = {}): ResolveEnv => ({
  configuredPath: '',
  pathDirs: [],
  goBin: undefined,
  goPath: undefined,
  isExecutable: () => false,
  ...over,
})

describe('resolveGsxPath', () => {
  it('prefers the configured path when executable', () => {
    const env = base({ configuredPath: '/opt/gsx', isExecutable: (p) => p === '/opt/gsx' })
    expect(resolveGsxPath(env)).toBe('/opt/gsx')
  })
  it('falls back to PATH', () => {
    const env = base({ pathDirs: ['/a', '/usr/bin'], isExecutable: (p) => p === '/usr/bin/gsx' })
    expect(resolveGsxPath(env)).toBe('/usr/bin/gsx')
  })
  it('then GOBIN, then GOPATH/bin', () => {
    const gobin = base({ goBin: '/gb', isExecutable: (p) => p === '/gb/gsx' })
    expect(resolveGsxPath(gobin)).toBe('/gb/gsx')
    const gopath = base({ goPath: '/gp', isExecutable: (p) => p === '/gp/bin/gsx' })
    expect(resolveGsxPath(gopath)).toBe('/gp/bin/gsx')
  })
  it('returns null when nothing resolves', () => {
    expect(resolveGsxPath(base())).toBeNull()
  })
  it('exposes the canonical install command', () => {
    expect(GSX_INSTALL_CMD).toBe('go install github.com/gsxhq/gsx/cmd/gsx@latest')
  })
})
```

- [ ] **Step 2: Run it — fails**

Run: `cd /Users/jackieli/personal/gsxhq/vscode-gsx && npx vitest run test/unit/gsxBinary.test.ts`
Expected: FAIL (`Cannot find module '../../src/gsxBinary'`).

- [ ] **Step 3: Implement `gsxBinary.ts`**

```ts
import { join } from 'node:path'

/** Inputs for binary resolution — all environment access is injected for testability. */
export type ResolveEnv = {
  configuredPath: string          // the gsx.server.path setting ("" if unset)
  pathDirs: string[]              // PATH split into directories
  goBin?: string                  // `go env GOBIN` (empty/undefined if unset)
  goPath?: string                 // `go env GOPATH`
  isExecutable: (p: string) => boolean
}

const BIN = process.platform === 'win32' ? 'gsx.exe' : 'gsx'

/** Resolve the gsx binary path: configured → PATH → GOBIN → GOPATH/bin. null if none. */
export function resolveGsxPath(env: ResolveEnv): string | null {
  if (env.configuredPath && env.isExecutable(env.configuredPath)) return env.configuredPath
  for (const dir of env.pathDirs) {
    const p = join(dir, BIN)
    if (env.isExecutable(p)) return p
  }
  if (env.goBin) {
    const p = join(env.goBin, BIN)
    if (env.isExecutable(p)) return p
  }
  if (env.goPath) {
    const p = join(env.goPath, 'bin', BIN)
    if (env.isExecutable(p)) return p
  }
  return null
}

export const GSX_INSTALL_CMD = 'go install github.com/gsxhq/gsx/cmd/gsx@latest'
```

- [ ] **Step 4: Run it — passes**

Run: `cd /Users/jackieli/personal/gsxhq/vscode-gsx && npx vitest run test/unit/gsxBinary.test.ts`
Expected: 5/5 PASS.

- [ ] **Step 5: Commit**

```bash
cd /Users/jackieli/personal/gsxhq/vscode-gsx
git add -A
git commit -m "binary: gopls-style gsx resolver (setting>PATH>GOBIN>GOPATH/bin) + unit tests"
```

---

## Task 4: LSP client, commands, settings + integration smoke

**Files:**
- Modify: `/Users/jackieli/personal/gsxhq/vscode-gsx/src/extension.ts`
- Modify: `/Users/jackieli/personal/gsxhq/vscode-gsx/package.json` (`contributes.configuration` + `commands`; `activationEvents`)
- Test: `/Users/jackieli/personal/gsxhq/vscode-gsx/test/integration/runTest.ts`, `suite/index.ts`, `suite/extension.test.ts`

**Interfaces:**
- Consumes: `resolveGsxPath`, `GSX_INSTALL_CMD` (Task 3).
- Produces: an extension that, on a `.gsx` file, resolves `gsx` and starts a `LanguageClient` (`gsx lsp`, stdio); commands `gsx.installServer` / `gsx.restartServer`; settings `gsx.server.path` / `gsx.trace.server`.

- [ ] **Step 1: Add configuration, commands, activation to `package.json`**

Add to `contributes` (alongside `languages`/`grammars`):

```json
    "configuration": {
      "title": "gsx",
      "properties": {
        "gsx.server.path": {
          "type": "string",
          "default": "",
          "description": "Absolute path to the gsx binary. Empty = auto-discover (PATH, GOBIN, GOPATH/bin)."
        },
        "gsx.trace.server": {
          "type": "string",
          "enum": ["off", "messages", "verbose"],
          "default": "off",
          "description": "Trace the communication between VS Code and the gsx language server."
        }
      }
    },
    "commands": [
      { "command": "gsx.installServer", "title": "gsx: Install/Update Language Server" },
      { "command": "gsx.restartServer", "title": "gsx: Restart Language Server" }
    ]
```

Add top-level:

```json
  "activationEvents": ["onLanguage:gsx"],
```

- [ ] **Step 2: Implement `src/extension.ts`**

```ts
import { execFile } from 'node:child_process'
import { promisify } from 'node:util'
import { existsSync, accessSync, constants } from 'node:fs'
import * as vscode from 'vscode'
import {
  LanguageClient,
  LanguageClientOptions,
  ServerOptions,
  TransportKind,
} from 'vscode-languageclient/node'
import { resolveGsxPath, GSX_INSTALL_CMD, type ResolveEnv } from './gsxBinary'

const execFileAsync = promisify(execFile)
let client: LanguageClient | undefined
const output = vscode.window.createOutputChannel('gsx')

export async function activate(context: vscode.ExtensionContext): Promise<void> {
  context.subscriptions.push(output)
  context.subscriptions.push(
    vscode.commands.registerCommand('gsx.installServer', installServer),
    vscode.commands.registerCommand('gsx.restartServer', restartServer),
  )
  await startServer()
}

export async function deactivate(): Promise<void> {
  await client?.stop()
  client = undefined
}

async function buildResolveEnv(): Promise<ResolveEnv> {
  const configuredPath = vscode.workspace.getConfiguration('gsx').get<string>('server.path', '')
  const pathDirs = (process.env.PATH ?? '').split(process.platform === 'win32' ? ';' : ':').filter(Boolean)
  let goBin: string | undefined
  let goPath: string | undefined
  try {
    const { stdout } = await execFileAsync('go', ['env', 'GOBIN', 'GOPATH'])
    const [b, p] = stdout.split('\n')
    goBin = b?.trim() || undefined
    goPath = p?.trim() || undefined
  } catch { /* go not installed — fine, PATH/setting may still resolve */ }
  const isExecutable = (p: string): boolean => {
    try { accessSync(p, constants.X_OK); return existsSync(p) } catch { return false }
  }
  return { configuredPath, pathDirs, goBin, goPath, isExecutable }
}

async function startServer(): Promise<void> {
  const gsxPath = resolveGsxPath(await buildResolveEnv())
  if (!gsxPath) {
    output.appendLine('gsx binary not found on PATH/GOBIN/GOPATH/bin.')
    const pick = await vscode.window.showWarningMessage(
      'gsx language server not found. Install it to get diagnostics, navigation, and formatting.',
      'Install gsx',
    )
    if (pick === 'Install gsx') await installServer()
    return
  }
  const serverOptions: ServerOptions = {
    command: gsxPath,
    args: ['lsp'],
    transport: TransportKind.stdio,
  }
  const clientOptions: LanguageClientOptions = {
    documentSelector: [{ language: 'gsx' }],
    outputChannel: output,
  }
  client = new LanguageClient('gsx', 'gsx language server', serverOptions, clientOptions)
  await client.start()
  output.appendLine(`gsx language server started: ${gsxPath} lsp`)
}

async function installServer(): Promise<void> {
  // Run in a visible terminal so the user sees progress/errors.
  const term = vscode.window.createTerminal('gsx: install')
  term.show()
  term.sendText(GSX_INSTALL_CMD)
  await vscode.window.showInformationMessage(
    'Installing gsx in a terminal. When it finishes, run "gsx: Restart Language Server".',
  )
}

async function restartServer(): Promise<void> {
  await client?.stop()
  client = undefined
  await startServer()
}
```

- [ ] **Step 3: Add the integration smoke harness**

Create `test/integration/runTest.ts`:

```ts
import { runTests } from '@vscode/test-electron'
import { resolve } from 'node:path'

async function main() {
  const extensionDevelopmentPath = resolve(__dirname, '../../')
  const extensionTestsPath = resolve(__dirname, './suite/index')
  await runTests({ extensionDevelopmentPath, extensionTestsPath })
}
main().catch((e) => { console.error(e); process.exit(1) })
```

Create `test/integration/suite/index.ts`:

```ts
import { glob } from 'glob'
import Mocha from 'mocha'
import { resolve } from 'node:path'

export async function run(): Promise<void> {
  const mocha = new Mocha({ ui: 'tdd', color: true, timeout: 60000 })
  const files = await glob('**/*.test.js', { cwd: __dirname })
  files.forEach((f) => mocha.addFile(resolve(__dirname, f)))
  await new Promise<void>((res, rej) =>
    mocha.run((failures) => (failures ? rej(new Error(`${failures} tests failed`)) : res())),
  )
}
```

Create `test/integration/suite/extension.test.ts`:

```ts
import * as assert from 'node:assert'
import * as vscode from 'vscode'

suite('gsx extension', () => {
  test('registers the gsx language', async () => {
    const langs = await vscode.languages.getLanguages()
    assert.ok(langs.includes('gsx'), 'gsx language is registered')
  })

  test('activates and opens a .gsx document without throwing', async () => {
    const doc = await vscode.workspace.openTextDocument({
      language: 'gsx',
      content: 'package views\n\ncomponent Hello() {\n\t<p>hi</p>\n}\n',
    })
    await vscode.window.showTextDocument(doc)
    assert.strictEqual(doc.languageId, 'gsx')
    // If gsx is on PATH (CI installs it), formatting should be available.
    if (process.env.GSX_ON_PATH === '1') {
      const edits = await vscode.commands.executeCommand<vscode.TextEdit[]>(
        'vscode.executeFormatDocumentProvider', doc.uri, {},
      )
      assert.ok(Array.isArray(edits), 'a formatting provider responded')
    }
  })
})
```

Add to `package.json` `scripts`: `"test:integration": "tsc -p ./ --outDir out-test && node out-test/test/integration/runTest.js"` and devDeps `"@vscode/test-electron": "^2.4.0"`, `"glob": "^11.0.0"`, `"mocha": "^10.0.0"`, `"@types/mocha": "^10.0.0"`.

- [ ] **Step 4: Typecheck, compile, run unit + (env-gated) integration**

Run:
```bash
cd /Users/jackieli/personal/gsxhq/vscode-gsx
npm install
npm run typecheck && npm run compile && npm run test:unit
```
Expected: clean; unit tests still pass. Integration (`npm run test:integration`) requires downloading VS Code (and `xvfb` on headless Linux); run it where a display is available, else rely on CI (Task 5). If you cannot run it locally, note that and proceed.

- [ ] **Step 5: Manual smoke (if a gsx binary is available)**

In VS Code: press F5 (Extension Development Host) with `gsx` on PATH, open a `.gsx` file → highlighting shows, diagnostics appear, format-on-save reformats via the LSP. With no `gsx`, the "Install gsx" notification appears and highlighting still works.

- [ ] **Step 6: Commit**

```bash
cd /Users/jackieli/personal/gsxhq/vscode-gsx
git add -A
git commit -m "lsp: language client (gsx lsp), install/restart commands, settings, integration smoke"
```

---

## Task 5: CI, release, marketing assets

**Files:**
- Create: `/Users/jackieli/personal/gsxhq/vscode-gsx/.github/workflows/ci.yml`
- Create: `/Users/jackieli/personal/gsxhq/vscode-gsx/.github/workflows/release.yml`
- Create: `/Users/jackieli/personal/gsxhq/vscode-gsx/icons/gsx.png`
- Create: `/Users/jackieli/personal/gsxhq/vscode-gsx/README.md`, `CHANGELOG.md`, `LICENSE`

**Interfaces:**
- Consumes: all prior tasks' `npm` scripts.
- Produces: green PR CI (grammar gen + no-drift, typecheck, lint, grammar + unit tests, `.vsix` artifact); tag-gated publish.

- [ ] **Step 1: Add the marketplace icon**

```bash
cp /Users/jackieli/personal/gsxhq/gsxhq.github.io/public/gsx-favicon-512.png \
   /Users/jackieli/personal/gsxhq/vscode-gsx/icons/gsx.png
```
(512px square — valid; Marketplace requires ≥128px.)

- [ ] **Step 2: Write `LICENSE`, `CHANGELOG.md`, `README.md`**

`LICENSE`: MIT, copyright holder "gsx authors" (match the gsx repo's license).

`CHANGELOG.md`:
```markdown
# Changelog

## 0.0.1
- Initial release: gsx syntax highlighting + `gsx lsp` language client (diagnostics, go-to-definition, hover, references, formatting).
```

`README.md` (sections): what it is; **requires the `gsx` binary** (`go install github.com/gsxhq/gsx/cmd/gsx@latest`, or use the "Install gsx" prompt); features; settings (`gsx.server.path`, `gsx.trace.server`); commands; that formatting/diagnostics come from `gsx lsp`; link to https://gsxhq.github.io and the gsx repo. No `{{ }}`/raw `<tag>` worries here (plain GitHub markdown, not VitePress).

- [ ] **Step 3: Write PR CI (`.github/workflows/ci.yml`)**

```yaml
name: CI
on:
  push: { branches: [main] }
  pull_request:
permissions: { contents: read }
jobs:
  build-test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v5
      - uses: actions/setup-node@v6
        with: { node-version: 24 }
      - uses: actions/setup-go@v5
        with: { go-version: '1.26.1' }
      - run: npm ci
      - name: Generate grammar + assert no drift
        run: |
          npm run gen:grammar
          git diff --exit-code syntaxes/gsx.tmLanguage.json \
            || (echo "::error::grammar JSON stale — run npm run gen:grammar and commit"; exit 1)
      - run: npm run typecheck
      - run: npm run lint
      - run: npm run test:grammar
      - run: npm run test:unit
      - name: Install gsx for the integration test
        run: |
          go install github.com/gsxhq/gsx/cmd/gsx@latest
          echo "$(go env GOPATH)/bin" >> "$GITHUB_PATH"
      - name: Integration smoke (headless)
        run: xvfb-run -a npm run test:integration
        env: { GSX_ON_PATH: '1' }
      - name: Package .vsix
        run: npm run package
      - uses: actions/upload-artifact@v4
        with: { name: vsix, path: '*.vsix' }
```

- [ ] **Step 4: Write release CI (`.github/workflows/release.yml`)**

```yaml
name: Release
on:
  push: { tags: ['v*'] }
permissions: { contents: read }
jobs:
  publish:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v5
      - uses: actions/setup-node@v6
        with: { node-version: 24 }
      - run: npm ci
      - run: npm run gen:grammar && npm run compile
      - name: Publish to VS Code Marketplace
        run: npx @vscode/vsce publish
        env: { VSCE_PAT: '${{ secrets.VSCE_PAT }}' }
      - name: Publish to Open VSX
        run: npx ovsx publish
        env: { OVSX_PAT: '${{ secrets.OVSX_PAT }}' }
```

- [ ] **Step 5: Validate the workflows + a clean local package**

Run:
```bash
cd /Users/jackieli/personal/gsxhq/vscode-gsx
python3 -c 'import yaml,sys; [yaml.safe_load(open(f)) for f in sys.argv[1:]]; print("workflows valid")' .github/workflows/ci.yml .github/workflows/release.yml
npm run package && ls *.vsix
```
Expected: `workflows valid`; a `gsx-0.0.1.vsix` is produced.

- [ ] **Step 6: Commit + push, create the GitHub repo**

```bash
cd /Users/jackieli/personal/gsxhq/vscode-gsx
git add -A
git commit -m "ci: PR build/test/package + tag-gated Marketplace/Open VSX release; README/LICENSE/icon"
gh repo create gsxhq/vscode-gsx --public --source=. --remote=origin --push
```

(Marketplace/Open VSX publishing additionally needs the `gsxhq` publisher created and `VSCE_PAT`/`OVSX_PAT` repo secrets set — maintainer step, not required for local `.vsix` testing.)

---

## Final verification (after all tasks)

- [ ] `cd /Users/jackieli/personal/gsxhq/vscode-gsx && npm ci && npm run gen:grammar && git diff --exit-code syntaxes/gsx.tmLanguage.json && npm run typecheck && npm run lint && npm run test:grammar && npm run test:unit && npm run package` → all clean; `.vsix` produced.
- [ ] Install the `.vsix` locally (`code --install-extension gsx-0.0.1.vsix`), open a real `.gsx` (e.g. from the gsx repo's `examples/`): highlighting renders (tags, components, holes, embedded `<style>`/`<script>`); with `gsx` on PATH, diagnostics + hover + go-to-definition + format-on-save work; without it, the Install prompt appears and highlighting still works.
- [ ] Confirm the grammar also serves as a Linguist contribution candidate later (out of scope to submit now).
- [ ] Publisher `gsxhq` + `VSCE_PAT`/`OVSX_PAT` secrets exist before tagging a `v*` release.
