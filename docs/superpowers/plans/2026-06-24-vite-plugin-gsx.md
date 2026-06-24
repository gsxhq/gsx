# @gsxhq/vite-plugin-gsx Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A dev-only Vite plugin that watches `.gsx`, regenerates via `go tool gsx generate`, surfaces gsx diagnostics as a Vite error overlay, and exposes a `/__reload` endpoint that broadcasts a browser full-reload.

**Architecture:** A single ESM/TypeScript npm package in a new repo. Four focused `src/` modules — options resolution, a pure diagnostics→Vite-overlay mapper, a child-process generate runner, and the plugin factory wiring them into `configureServer`. The plugin owns regeneration + reload delivery; it does NOT manage the Go process or ship a Go helper (the Go-side reload glue is documented in the README).

**Tech Stack:** TypeScript, Vite (peer dep), vitest (tests), tsup (build), picomatch (glob matching), Node ≥18.

**Spec:** `docs/superpowers/specs/2026-06-24-vite-plugin-gsx-design.md` (in the gsx repo).

## Global Constraints

- **Package name:** `@gsxhq/vite-plugin-gsx`. **Plugin `name`:** `"vite-plugin-gsx"`.
- **Repo location:** new git repo at `/Users/jackieli/personal/gsxhq/vite-plugin-gsx` (sibling of `gsx`, `tree-sitter-gsx`).
- **Dev-only:** plugin sets `apply: "serve"`; it is a no-op in `vite build`.
- **Vite peer dep:** `"^5.0.0 || ^6.0.0 || ^7.0.0"`. **Node:** `>=18`. **ESM only** (`"type": "module"`).
- **Default invocation:** `command` defaults to `["go","tool","gsx","generate"]` (the recommended `go tool gsx` pattern). **`paths` defaults to `["."]`** (whole-module regen; gsx's content-hash cache keeps it cheap).
- **Reload correctness:** the plugin NEVER broadcasts a reload on successful generate. Reload is driven solely by an external `POST <reloadEndpoint>` (the Go server after boot). Default `reloadEndpoint` is `"/__reload"`.
- **Full-reload only** — no partial HMR (server-rendered HTML).
- **No change to core gsx.** The Go-side glue (proxy, `@vite/client` script, `NotifyReload` POST) is documented in the README, not packaged.
- **License:** MIT (match the gsx org).

---

### Task 1: Package scaffold

Creates the repo and a working build/test harness so later tasks have a TDD loop. Deliverable: `npm run build` and `npm test` both succeed.

**Files:**
- Create: `/Users/jackieli/personal/gsxhq/vite-plugin-gsx/package.json`
- Create: `/Users/jackieli/personal/gsxhq/vite-plugin-gsx/tsconfig.json`
- Create: `/Users/jackieli/personal/gsxhq/vite-plugin-gsx/tsup.config.ts`
- Create: `/Users/jackieli/personal/gsxhq/vite-plugin-gsx/vitest.config.ts`
- Create: `/Users/jackieli/personal/gsxhq/vite-plugin-gsx/.gitignore`
- Create: `/Users/jackieli/personal/gsxhq/vite-plugin-gsx/LICENSE`
- Create: `/Users/jackieli/personal/gsxhq/vite-plugin-gsx/src/index.ts` (temporary stub, replaced in Task 5)
- Create: `/Users/jackieli/personal/gsxhq/vite-plugin-gsx/test/smoke.test.ts`

**Interfaces:**
- Produces: a buildable ESM package whose default export will become `gsx(options?)`. Test runner is `vitest run`.

- [ ] **Step 1: Create the repo directory and init git**

Run:
```bash
mkdir -p /Users/jackieli/personal/gsxhq/vite-plugin-gsx
cd /Users/jackieli/personal/gsxhq/vite-plugin-gsx
git init
```
Expected: `Initialized empty Git repository`.

- [ ] **Step 2: Write `package.json`**

```json
{
  "name": "@gsxhq/vite-plugin-gsx",
  "version": "0.1.0",
  "description": "Vite dev plugin for gsx: watch .gsx, regenerate, error overlay, browser reload.",
  "type": "module",
  "license": "MIT",
  "engines": { "node": ">=18" },
  "files": ["dist"],
  "exports": {
    ".": {
      "types": "./dist/index.d.ts",
      "import": "./dist/index.js"
    }
  },
  "main": "./dist/index.js",
  "types": "./dist/index.d.ts",
  "scripts": {
    "build": "tsup",
    "test": "vitest run",
    "test:watch": "vitest",
    "typecheck": "tsc --noEmit"
  },
  "peerDependencies": {
    "vite": "^5.0.0 || ^6.0.0 || ^7.0.0"
  },
  "dependencies": {
    "picomatch": "^4.0.2"
  },
  "devDependencies": {
    "@types/node": "^20.14.0",
    "@types/picomatch": "^3.0.1",
    "tsup": "^8.3.0",
    "typescript": "^5.6.0",
    "vite": "^6.0.0",
    "vitest": "^2.1.0"
  }
}
```

- [ ] **Step 3: Write `tsconfig.json`**

```json
{
  "compilerOptions": {
    "target": "ES2022",
    "module": "ESNext",
    "moduleResolution": "Bundler",
    "lib": ["ES2022"],
    "types": ["node"],
    "strict": true,
    "declaration": true,
    "esModuleInterop": true,
    "skipLibCheck": true,
    "noUncheckedIndexedAccess": true,
    "outDir": "dist"
  },
  "include": ["src", "test", "tsup.config.ts", "vitest.config.ts"]
}
```

- [ ] **Step 4: Write `tsup.config.ts`, `vitest.config.ts`, `.gitignore`, `LICENSE`**

`tsup.config.ts`:
```ts
import { defineConfig } from "tsup";

export default defineConfig({
  entry: ["src/index.ts"],
  format: ["esm"],
  dts: true,
  clean: true,
  target: "node18",
});
```

`vitest.config.ts`:
```ts
import { defineConfig } from "vitest/config";

export default defineConfig({
  test: {
    include: ["test/**/*.test.ts"],
    environment: "node",
  },
});
```

`.gitignore`:
```
node_modules
dist
*.log
.DS_Store
```

`LICENSE`: MIT license text, copyright holder `gsxhq`, year `2026`.

- [ ] **Step 5: Write the temporary `src/index.ts` stub**

```ts
import type { Plugin } from "vite";

export interface GsxOptions {}

export function gsx(_options: GsxOptions = {}): Plugin {
  return {
    name: "vite-plugin-gsx",
    apply: "serve",
  };
}

export default gsx;
```

- [ ] **Step 6: Write `test/smoke.test.ts`**

```ts
import { describe, it, expect } from "vitest";
import { gsx } from "../src/index.js";

describe("scaffold", () => {
  it("exports a plugin factory with the right name", () => {
    const plugin = gsx();
    expect(plugin.name).toBe("vite-plugin-gsx");
    expect(plugin.apply).toBe("serve");
  });
});
```

- [ ] **Step 7: Install deps and run build + test**

Run:
```bash
cd /Users/jackieli/personal/gsxhq/vite-plugin-gsx
npm install
npm test
npm run build
```
Expected: `npm test` → 1 passed; `npm run build` → emits `dist/index.js` and `dist/index.d.ts` with no errors.

- [ ] **Step 8: Commit**

```bash
cd /Users/jackieli/personal/gsxhq/vite-plugin-gsx
git add -A
git commit -m "chore: scaffold @gsxhq/vite-plugin-gsx (build + test harness)"
```

---

### Task 2: Options resolution

**Files:**
- Create: `/Users/jackieli/personal/gsxhq/vite-plugin-gsx/src/options.ts`
- Create: `/Users/jackieli/personal/gsxhq/vite-plugin-gsx/test/options.test.ts`

**Interfaces:**
- Produces:
  ```ts
  interface GsxOptions {
    command?: string[];
    paths?: string[];
    watch?: string | string[];
    cwd?: string;
    reloadEndpoint?: string;
    debounce?: number;
    generateOnStart?: boolean;
  }
  interface ResolvedOptions {
    command: string[];
    paths: string[];
    watch: string[];
    cwd: string;
    reloadEndpoint: string;
    debounce: number;
    generateOnStart: boolean;
  }
  function resolveOptions(user: GsxOptions, root: string): ResolvedOptions
  ```
- Consumes: nothing (replaces the empty `GsxOptions` stub from Task 1; Task 5 re-exports `GsxOptions` from here).

- [ ] **Step 1: Write the failing test** — `test/options.test.ts`

```ts
import { describe, it, expect } from "vitest";
import { resolveOptions } from "../src/options.js";

describe("resolveOptions", () => {
  it("applies all defaults when user passes nothing", () => {
    const r = resolveOptions({}, "/proj");
    expect(r.command).toEqual(["go", "tool", "gsx", "generate"]);
    expect(r.paths).toEqual(["."]);
    expect(r.watch).toEqual(["**/*.gsx"]);
    expect(r.cwd).toBe("/proj");
    expect(r.reloadEndpoint).toBe("/__reload");
    expect(r.debounce).toBe(50);
    expect(r.generateOnStart).toBe(true);
  });

  it("normalizes a string watch into an array", () => {
    const r = resolveOptions({ watch: "src/**/*.gsx" }, "/proj");
    expect(r.watch).toEqual(["src/**/*.gsx"]);
  });

  it("honors overrides, including generateOnStart:false and a custom command", () => {
    const r = resolveOptions(
      {
        command: ["go", "run", "./cmd/gsx", "generate"],
        paths: ["./views"],
        cwd: "/elsewhere",
        reloadEndpoint: "/__reload2",
        debounce: 120,
        generateOnStart: false,
      },
      "/proj",
    );
    expect(r.command).toEqual(["go", "run", "./cmd/gsx", "generate"]);
    expect(r.paths).toEqual(["./views"]);
    expect(r.cwd).toBe("/elsewhere");
    expect(r.reloadEndpoint).toBe("/__reload2");
    expect(r.debounce).toBe(120);
    expect(r.generateOnStart).toBe(false);
  });
});
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd /Users/jackieli/personal/gsxhq/vite-plugin-gsx && npx vitest run test/options.test.ts`
Expected: FAIL — `Cannot find module '../src/options.js'`.

- [ ] **Step 3: Write `src/options.ts`**

```ts
export interface GsxOptions {
  /** Command + leading args to invoke gsx. Default: ["go","tool","gsx","generate"]. */
  command?: string[];
  /** Path args passed to generate. Default: ["."]. */
  paths?: string[];
  /** Globs whose changes trigger regeneration. Default: all .gsx files. */
  watch?: string | string[];
  /** Working directory for the command. Default: Vite config root. */
  cwd?: string;
  /** Endpoint the Go server POSTs to trigger reload. Default: "/__reload". */
  reloadEndpoint?: string;
  /** Debounce window for rapid saves, ms. Default: 50. */
  debounce?: number;
  /** Run an initial generate when the dev server starts. Default: true. */
  generateOnStart?: boolean;
}

export interface ResolvedOptions {
  command: string[];
  paths: string[];
  watch: string[];
  cwd: string;
  reloadEndpoint: string;
  debounce: number;
  generateOnStart: boolean;
}

const DEFAULT_GSX_GLOB = "**/*.gsx";

export function resolveOptions(user: GsxOptions, root: string): ResolvedOptions {
  const watch =
    user.watch === undefined
      ? [DEFAULT_GSX_GLOB]
      : Array.isArray(user.watch)
        ? user.watch
        : [user.watch];
  return {
    command: user.command ?? ["go", "tool", "gsx", "generate"],
    paths: user.paths ?? ["."],
    watch,
    cwd: user.cwd ?? root,
    reloadEndpoint: user.reloadEndpoint ?? "/__reload",
    debounce: user.debounce ?? 50,
    generateOnStart: user.generateOnStart ?? true,
  };
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `cd /Users/jackieli/personal/gsxhq/vite-plugin-gsx && npx vitest run test/options.test.ts`
Expected: PASS — 3 passed.

- [ ] **Step 5: Commit**

```bash
cd /Users/jackieli/personal/gsxhq/vite-plugin-gsx
git add src/options.ts test/options.test.ts
git commit -m "feat: options resolution with defaults"
```

---

### Task 3: Diagnostics → Vite overlay mapper

A pure module: gsx `--json` diagnostic types + a mapper to Vite's error payload, including a source-frame builder. `readSource` is injected so the mapper is pure and unit-testable.

**Files:**
- Create: `/Users/jackieli/personal/gsxhq/vite-plugin-gsx/src/diagnostics.ts`
- Create: `/Users/jackieli/personal/gsxhq/vite-plugin-gsx/test/diagnostics.test.ts`

**Interfaces:**
- Produces:
  ```ts
  interface GsxPos { line: number; col: number }
  interface GsxRange { start: GsxPos; end: GsxPos }
  interface GsxDiagnostic {
    file: string; range: GsxRange; severity: string;
    code?: string; message: string; help?: string; source?: string;
  }
  interface ViteError {
    message: string; stack: string; id: string;
    frame: string; plugin: string;
    loc: { file: string; line: number; column: number };
  }
  function toViteError(
    diags: GsxDiagnostic[],
    readSource: (file: string) => string | null,
  ): ViteError | null   // null when no error-severity diagnostic
  ```
- Consumes: nothing.

- [ ] **Step 1: Write the failing test** — `test/diagnostics.test.ts`

```ts
import { describe, it, expect } from "vitest";
import { toViteError, type GsxDiagnostic } from "../src/diagnostics.js";

function diag(over: Partial<GsxDiagnostic> = {}): GsxDiagnostic {
  return {
    file: "views/foo.gsx",
    range: { start: { line: 2, col: 7 }, end: { line: 2, col: 10 } },
    severity: "error",
    code: "syntax",
    message: "mismatched close tag",
    ...over,
  };
}

const SRC = "package views\n  <div></span>\n"; // line 2 is "  <div></span>"
const read = (_f: string) => SRC;

describe("toViteError", () => {
  it("returns null when there is no error-severity diagnostic", () => {
    expect(toViteError([diag({ severity: "warning" })], read)).toBeNull();
    expect(toViteError([], read)).toBeNull();
  });

  it("prefixes the code and fills loc + plugin", () => {
    const err = toViteError([diag()], read)!;
    expect(err.message).toContain("syntax: mismatched close tag");
    expect(err.loc).toEqual({ file: "views/foo.gsx", line: 2, column: 7 });
    expect(err.plugin).toBe("vite-plugin-gsx");
    expect(err.id).toBe("views/foo.gsx");
  });

  it("appends help after a blank line when present", () => {
    const err = toViteError([diag({ help: "did you mean </div>?" })], read)!;
    expect(err.message).toBe(
      "syntax: mismatched close tag\n\ndid you mean </div>?",
    );
  });

  it("omits the code prefix when code is absent", () => {
    const err = toViteError([diag({ code: undefined })], read)!;
    expect(err.message).toBe("mismatched close tag");
  });

  it("builds a frame with a caret under the start column", () => {
    const err = toViteError([diag()], read)!;
    // frame shows the offending source line and a caret at column 7
    expect(err.frame).toContain("<div></span>");
    expect(err.frame).toMatch(/\n\s+\^/);
  });

  it("yields an empty frame when the source cannot be read", () => {
    const err = toViteError([diag()], () => null)!;
    expect(err.frame).toBe("");
  });

  it("picks the first error when warnings precede it", () => {
    const err = toViteError(
      [diag({ severity: "warning", message: "w" }), diag({ message: "real" })],
      read,
    )!;
    expect(err.message).toContain("real");
  });
});
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd /Users/jackieli/personal/gsxhq/vite-plugin-gsx && npx vitest run test/diagnostics.test.ts`
Expected: FAIL — `Cannot find module '../src/diagnostics.js'`.

- [ ] **Step 3: Write `src/diagnostics.ts`**

```ts
export interface GsxPos {
  line: number;
  col: number;
}
export interface GsxRange {
  start: GsxPos;
  end: GsxPos;
}
export interface GsxDiagnostic {
  file: string;
  range: GsxRange;
  severity: string;
  code?: string;
  message: string;
  help?: string;
  source?: string;
}

export interface ViteError {
  message: string;
  stack: string;
  id: string;
  frame: string;
  plugin: string;
  loc: { file: string; line: number; column: number };
}

/**
 * Map gsx `--json` diagnostics onto Vite's error-overlay payload shape.
 * Returns null when no error-severity diagnostic is present (warnings alone
 * do not raise an overlay). `readSource` is injected so this stays pure.
 */
export function toViteError(
  diags: GsxDiagnostic[],
  readSource: (file: string) => string | null,
): ViteError | null {
  const err = diags.find((d) => d.severity === "error");
  if (!err) return null;

  const head = err.code ? `${err.code}: ${err.message}` : err.message;
  const message = err.help ? `${head}\n\n${err.help}` : head;

  return {
    message,
    stack: "",
    id: err.file,
    frame: buildFrame(err, readSource),
    plugin: "vite-plugin-gsx",
    loc: {
      file: err.file,
      line: err.range.start.line,
      column: err.range.start.col,
    },
  };
}

/** Build a one-line code frame with a caret under the diagnostic's start column. */
function buildFrame(
  diag: GsxDiagnostic,
  readSource: (file: string) => string | null,
): string {
  const src = readSource(diag.file);
  if (src === null) return "";
  const lines = src.split("\n");
  const lineNo = diag.range.start.line; // 1-based
  const srcLine = lines[lineNo - 1];
  if (srcLine === undefined) return "";

  const gutter = `${lineNo} | `;
  const caretPad = " ".repeat(gutter.length + Math.max(0, diag.range.start.col - 1));
  return `${gutter}${srcLine}\n${caretPad}^`;
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `cd /Users/jackieli/personal/gsxhq/vite-plugin-gsx && npx vitest run test/diagnostics.test.ts`
Expected: PASS — 7 passed.

- [ ] **Step 5: Commit**

```bash
cd /Users/jackieli/personal/gsxhq/vite-plugin-gsx
git add src/diagnostics.ts test/diagnostics.test.ts
git commit -m "feat: gsx diagnostics -> Vite overlay mapper"
```

---

### Task 4: Generate runner

Spawns the gsx command with `--json` and the paths, captures output, parses diagnostics on failure, and synthesizes a clear remediation diagnostic when the process can't be spawned (e.g. `go` missing / tool directive absent) or stdout isn't JSON.

**Files:**
- Create: `/Users/jackieli/personal/gsxhq/vite-plugin-gsx/src/generate.ts`
- Create: `/Users/jackieli/personal/gsxhq/vite-plugin-gsx/test/generate.test.ts`
- Create: `/Users/jackieli/personal/gsxhq/vite-plugin-gsx/test/fixtures/fake-gsx.mjs`

**Interfaces:**
- Consumes: `GsxDiagnostic` from `src/diagnostics.ts`.
- Produces:
  ```ts
  interface GenerateResult { ok: boolean; diagnostics: GsxDiagnostic[]; raw: string }
  function runGenerate(opts: {
    command: string[]; paths: string[]; cwd: string;
  }): Promise<GenerateResult>
  ```

- [ ] **Step 1: Write the fake gsx fixture** — `test/fixtures/fake-gsx.mjs`

```js
#!/usr/bin/env node
// Test double for `gsx generate`. Behaviour controlled by argv flags that the
// test prepends to `command` (they arrive before runGenerate's "--json" + paths):
//   --mode=ok    (default) : append a run-marker line, exit 0
//   --mode=fail            : print a gsx --json diagnostics array, exit 1
//   --mode=badjson         : print non-JSON to stdout, exit 1
//   --mode=crash           : print "gsx: boom" to stderr, exit 2 (no stdout)
// Always appends one line to ./gsx-ran.log in cwd so tests can count invocations.
import { appendFileSync } from "node:fs";
import { join } from "node:path";

const argv = process.argv.slice(2);
const mode = (argv.find((a) => a.startsWith("--mode=")) ?? "--mode=ok").slice(7);

appendFileSync(join(process.cwd(), "gsx-ran.log"), mode + "\n");

if (mode === "fail") {
  const diags = [
    {
      file: "views/foo.gsx",
      range: { start: { line: 2, col: 7 }, end: { line: 2, col: 10 } },
      severity: "error",
      code: "syntax",
      message: "mismatched close tag",
      help: "did you mean </div>?",
    },
  ];
  process.stdout.write(JSON.stringify(diags));
  process.exit(1);
} else if (mode === "badjson") {
  process.stdout.write("not json at all");
  process.exit(1);
} else if (mode === "crash") {
  process.stderr.write("gsx: boom\n");
  process.exit(2);
}
process.exit(0);
```

- [ ] **Step 2: Write the failing test** — `test/generate.test.ts`

```ts
import { describe, it, expect, beforeEach } from "vitest";
import { fileURLToPath } from "node:url";
import { mkdtempSync, readFileSync, existsSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { runGenerate } from "../src/generate.js";

const fakeGsx = fileURLToPath(
  new URL("./fixtures/fake-gsx.mjs", import.meta.url),
);

let cwd: string;
beforeEach(() => {
  cwd = mkdtempSync(join(tmpdir(), "gsx-gen-"));
});

describe("runGenerate", () => {
  it("returns ok on a clean run and invokes the command once", async () => {
    const r = await runGenerate({
      command: ["node", fakeGsx],
      paths: ["."],
      cwd,
    });
    expect(r.ok).toBe(true);
    expect(r.diagnostics).toEqual([]);
    expect(readFileSync(join(cwd, "gsx-ran.log"), "utf8").trim()).toBe("ok");
  });

  it("parses --json diagnostics on failure", async () => {
    const r = await runGenerate({
      command: ["node", fakeGsx, "--mode=fail"],
      paths: ["."],
      cwd,
    });
    expect(r.ok).toBe(false);
    expect(r.diagnostics).toHaveLength(1);
    expect(r.diagnostics[0]!.message).toBe("mismatched close tag");
    expect(r.diagnostics[0]!.severity).toBe("error");
  });

  it("synthesizes a diagnostic when stdout is not JSON", async () => {
    const r = await runGenerate({
      command: ["node", fakeGsx, "--mode=badjson"],
      paths: ["."],
      cwd,
    });
    expect(r.ok).toBe(false);
    expect(r.diagnostics).toHaveLength(1);
    expect(r.diagnostics[0]!.severity).toBe("error");
  });

  it("synthesizes a diagnostic from stderr on a usage/crash exit", async () => {
    const r = await runGenerate({
      command: ["node", fakeGsx, "--mode=crash"],
      paths: ["."],
      cwd,
    });
    expect(r.ok).toBe(false);
    expect(r.diagnostics).toHaveLength(1);
    expect(r.diagnostics[0]!.message).toContain("boom");
  });

  it("returns a remediation diagnostic when the binary cannot be spawned", async () => {
    const r = await runGenerate({
      command: ["definitely-not-a-real-binary-xyz"],
      paths: ["."],
      cwd,
    });
    expect(r.ok).toBe(false);
    expect(r.diagnostics).toHaveLength(1);
    expect(r.diagnostics[0]!.message.toLowerCase()).toContain("gsx");
  });
});
```

- [ ] **Step 3: Run the test to verify it fails**

Run: `cd /Users/jackieli/personal/gsxhq/vite-plugin-gsx && npx vitest run test/generate.test.ts`
Expected: FAIL — `Cannot find module '../src/generate.js'`.

- [ ] **Step 4: Write `src/generate.ts`**

```ts
import { spawn } from "node:child_process";
import type { GsxDiagnostic } from "./diagnostics.js";

export interface GenerateResult {
  ok: boolean;
  diagnostics: GsxDiagnostic[];
  raw: string;
}

export interface RunGenerateOptions {
  command: string[];
  paths: string[];
  cwd: string;
}

/**
 * Run `<command> --json <paths...>` in cwd. On success (exit 0) returns
 * ok:true with no diagnostics. On a non-zero exit, parses the gsx JSON
 * diagnostics array from stdout; if that is missing/unparseable, synthesizes a
 * single error diagnostic from stderr (or stdout). A spawn failure (binary not
 * found) yields a remediation diagnostic pointing at the gsx tool setup.
 */
export function runGenerate(opts: RunGenerateOptions): Promise<GenerateResult> {
  const [bin, ...leading] = opts.command;
  if (!bin) {
    return Promise.resolve({
      ok: false,
      raw: "",
      diagnostics: [synthetic("vite-plugin-gsx: empty `command` option")],
    });
  }
  const args = [...leading, "--json", ...opts.paths];

  return new Promise<GenerateResult>((resolve) => {
    let stdout = "";
    let stderr = "";
    const child = spawn(bin, args, { cwd: opts.cwd });

    child.stdout.on("data", (d) => (stdout += d.toString()));
    child.stderr.on("data", (d) => (stderr += d.toString()));

    child.on("error", (e) => {
      resolve({
        ok: false,
        raw: String(e),
        diagnostics: [
          synthetic(
            `vite-plugin-gsx: could not run gsx (\`${opts.command.join(" ")}\`): ${
              (e as NodeJS.ErrnoException).code ?? e.message
            }. Is the gsx \`tool\` directive in go.mod, and is Go installed?`,
          ),
        ],
      });
    });

    child.on("close", (code) => {
      if (code === 0) {
        resolve({ ok: true, diagnostics: [], raw: stdout });
        return;
      }
      const parsed = parseDiagnostics(stdout);
      if (parsed) {
        resolve({ ok: false, diagnostics: parsed, raw: stdout });
        return;
      }
      const detail = (stderr || stdout || `exit ${code}`).trim();
      resolve({
        ok: false,
        raw: stdout,
        diagnostics: [synthetic(`gsx generate failed: ${detail}`)],
      });
    });
  });
}

function parseDiagnostics(stdout: string): GsxDiagnostic[] | null {
  const text = stdout.trim();
  if (!text.startsWith("[")) return null;
  try {
    const arr = JSON.parse(text);
    if (!Array.isArray(arr)) return null;
    return arr as GsxDiagnostic[];
  } catch {
    return null;
  }
}

function synthetic(message: string): GsxDiagnostic {
  return {
    file: "",
    range: { start: { line: 1, col: 1 }, end: { line: 1, col: 1 } },
    severity: "error",
    message,
  };
}
```

- [ ] **Step 5: Run the test to verify it passes**

Run: `cd /Users/jackieli/personal/gsxhq/vite-plugin-gsx && npx vitest run test/generate.test.ts`
Expected: PASS — 5 passed.

- [ ] **Step 6: Commit**

```bash
cd /Users/jackieli/personal/gsxhq/vite-plugin-gsx
git add src/generate.ts test/generate.test.ts test/fixtures/fake-gsx.mjs
git commit -m "feat: gsx generate runner with diagnostic + spawn-failure handling"
```

---

### Task 5: Plugin factory + dev-server integration

Wires options + generate + diagnostics into the Vite plugin: the `/__reload` middleware, the debounced `.gsx` watcher, the initial generate, and the error overlay. Replaces the Task 1 stub.

**Files:**
- Modify (replace): `/Users/jackieli/personal/gsxhq/vite-plugin-gsx/src/index.ts`
- Create: `/Users/jackieli/personal/gsxhq/vite-plugin-gsx/test/index.test.ts`
- Delete: `/Users/jackieli/personal/gsxhq/vite-plugin-gsx/test/smoke.test.ts` (superseded by index.test.ts)

**Interfaces:**
- Consumes: `resolveOptions`, `GsxOptions` (`src/options.ts`); `runGenerate` (`src/generate.ts`); `toViteError` (`src/diagnostics.ts`).
- Produces: `gsx(options?: GsxOptions): Plugin` (default + named export). Re-exports `GsxOptions`.

- [ ] **Step 1: Write the failing test** — `test/index.test.ts`

```ts
import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { fileURLToPath } from "node:url";
import { createServer, type ViteDevServer } from "vite";
import { createServer as createHttp, type Server } from "node:http";
import { mkdtempSync, writeFileSync, existsSync, readFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { gsx } from "../src/index.js";

const fakeGsx = fileURLToPath(
  new URL("./fixtures/fake-gsx.mjs", import.meta.url),
);

let root: string;
let server: ViteDevServer;
let http: Server | undefined;

beforeEach(() => {
  root = mkdtempSync(join(tmpdir(), "gsx-plugin-"));
});
afterEach(async () => {
  http?.close();
  await server?.close();
});

async function start(command: string[], generateOnStart = false) {
  server = await createServer({
    root,
    logLevel: "silent",
    server: { middlewareMode: true, hmr: false },
    plugins: [gsx({ command, generateOnStart })],
  });
  return server;
}

describe("vite-plugin-gsx", () => {
  it("POST /__reload broadcasts a full-reload over the ws", async () => {
    await start(["node", fakeGsx]);
    const send = vi.spyOn(server.ws, "send");

    http = createHttp(server.middlewares);
    await new Promise<void>((r) => http!.listen(0, r));
    const port = (http.address() as { port: number }).port;

    const res = await fetch(`http://localhost:${port}/__reload`, {
      method: "POST",
    });
    expect(res.status).toBe(204);
    expect(send).toHaveBeenCalledWith(
      expect.objectContaining({ type: "full-reload" }),
    );
  });

  it("a failing generate on a .gsx change sends an error overlay payload", async () => {
    await start(["node", fakeGsx, "--mode=fail"]);
    const send = vi.spyOn(server.ws, "send");

    const gsxFile = join(root, "foo.gsx");
    writeFileSync(gsxFile, "package x\n");
    server.watcher.emit("change", gsxFile);

    await vi.waitFor(() => {
      expect(send).toHaveBeenCalledWith(
        expect.objectContaining({ type: "error" }),
      );
    }, { timeout: 2000 });
  });

  it("a successful generate on change does NOT broadcast a reload", async () => {
    await start(["node", fakeGsx]); // --mode=ok
    const send = vi.spyOn(server.ws, "send");

    const gsxFile = join(root, "foo.gsx");
    writeFileSync(gsxFile, "package x\n");
    server.watcher.emit("change", gsxFile);

    // Wait past the debounce window and the generate.
    await new Promise((r) => setTimeout(r, 400));
    expect(send).not.toHaveBeenCalledWith(
      expect.objectContaining({ type: "full-reload" }),
    );
    expect(send).not.toHaveBeenCalledWith(
      expect.objectContaining({ type: "error" }),
    );
  });

  it("ignores non-.gsx file changes", async () => {
    await start(["node", fakeGsx]);
    const other = join(root, "notes.txt");
    writeFileSync(other, "hi");
    server.watcher.emit("change", other);
    await new Promise((r) => setTimeout(r, 200));
    expect(existsSync(join(root, "gsx-ran.log"))).toBe(false);
  });

  it("generateOnStart runs one generate at startup", async () => {
    await start(["node", fakeGsx], true);
    await vi.waitFor(
      () => expect(existsSync(join(root, "gsx-ran.log"))).toBe(true),
      { timeout: 2000 },
    );
    expect(readFileSync(join(root, "gsx-ran.log"), "utf8").trim()).toBe("ok");
  });
});
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd /Users/jackieli/personal/gsxhq/vite-plugin-gsx && npx vitest run test/index.test.ts`
Expected: FAIL — the stub plugin has no `configureServer`, so `/__reload` 404s and the spies are never called.

- [ ] **Step 3: Replace `src/index.ts`**

```ts
import { readFileSync } from "node:fs";
import { relative } from "node:path";
import picomatch from "picomatch";
import type { Plugin, ViteDevServer } from "vite";
import { resolveOptions, type GsxOptions } from "./options.js";
import { runGenerate } from "./generate.js";
import { toViteError } from "./diagnostics.js";

export type { GsxOptions };

export function gsx(options: GsxOptions = {}): Plugin {
  return {
    name: "vite-plugin-gsx",
    apply: "serve",
    configureServer(server: ViteDevServer) {
      const opts = resolveOptions(options, server.config.root);
      const isMatch = picomatch(opts.watch);
      const logger = server.config.logger;

      // 1. /__reload endpoint — external trigger (the Go server after boot).
      server.middlewares.use(opts.reloadEndpoint, (req, res) => {
        if (req.method !== "POST") {
          res.statusCode = 405;
          res.end();
          return;
        }
        server.ws.send({ type: "full-reload", path: "*" });
        res.statusCode = 204;
        res.end();
      });

      // 2. Run gsx generate; show or clear the overlay. Never broadcasts a
      //    reload — that is the Go-POST's job, so we only reload once the new
      //    binary is up.
      async function generate() {
        const result = await runGenerate({
          command: opts.command,
          paths: opts.paths,
          cwd: opts.cwd,
        });
        if (result.ok) return;
        const err = toViteError(result.diagnostics, readSource);
        if (err) {
          for (const d of result.diagnostics) {
            logger.error(`[vite-plugin-gsx] ${d.file}: ${d.message}`, {
              timestamp: true,
            });
          }
          server.ws.send({ type: "error", err });
        }
      }

      // 3. Debounced watcher over the .gsx globs.
      let timer: ReturnType<typeof setTimeout> | undefined;
      function schedule() {
        if (timer) clearTimeout(timer);
        timer = setTimeout(() => void generate(), opts.debounce);
      }
      // Watch globs are relative to cwd; chokidar reports absolute paths, so
      // match on the path relative to cwd (no string munging).
      function onChange(file: string) {
        if (isMatch(relative(opts.cwd, file))) schedule();
      }
      server.watcher.on("change", onChange);
      server.watcher.on("add", onChange);
      server.watcher.on("unlink", onChange);

      // 4. Initial generate so .x.go exist on first boot.
      if (opts.generateOnStart) void generate();
    },
  };
}

function readSource(file: string): string | null {
  if (!file) return null;
  try {
    return readFileSync(file, "utf8");
  } catch {
    return null;
  }
}

export default gsx;
```

- [ ] **Step 4: Delete the superseded smoke test**

Run:
```bash
cd /Users/jackieli/personal/gsxhq/vite-plugin-gsx
git rm test/smoke.test.ts
```

- [ ] **Step 5: Run the test to verify it passes**

Run: `cd /Users/jackieli/personal/gsxhq/vite-plugin-gsx && npx vitest run test/index.test.ts`
Expected: PASS — 5 passed.

- [ ] **Step 6: Run the full suite + typecheck + build**

Run:
```bash
cd /Users/jackieli/personal/gsxhq/vite-plugin-gsx
npm test
npm run typecheck
npm run build
```
Expected: all tests pass (options + diagnostics + generate + index); `tsc --noEmit` clean; build emits `dist/`.

- [ ] **Step 7: Commit**

```bash
cd /Users/jackieli/personal/gsxhq/vite-plugin-gsx
git add -A
git commit -m "feat: plugin factory — /__reload, debounced watcher, overlay, initial generate"
```

---

### Task 6: README with the documented dev-loop recipe

The README is a deliverable: it carries the Go-side glue (proxy, `@vite/client`, `NotifyReload`) the package deliberately doesn't ship. An `examples/vite.config.ts` makes the central snippet type-checked rather than prose-only.

**Files:**
- Create: `/Users/jackieli/personal/gsxhq/vite-plugin-gsx/README.md`
- Create: `/Users/jackieli/personal/gsxhq/vite-plugin-gsx/examples/vite.config.ts`

**Interfaces:**
- Consumes: the public `gsx` / `GsxOptions` API from `src/index.ts`.

- [ ] **Step 1: Write `examples/vite.config.ts` (type-checked usage)**

```ts
import { defineConfig } from "vite";
import { gsx } from "@gsxhq/vite-plugin-gsx";

// Example dev config: Vite is the front door and proxies non-Vite routes to the
// Go server, so the injected @vite/client socket survives Go rebuilds.
export default defineConfig({
  plugins: [
    gsx({
      // Default is ["go","tool","gsx","generate"]; override for a custom cmd/gsx:
      // command: ["go", "run", "./cmd/gsx", "generate"],
    }),
  ],
  server: {
    proxy: {
      "^(?!/@vite|/@id|/node_modules).*": {
        target: "http://localhost:8080",
        changeOrigin: true,
        ws: true,
      },
    },
  },
});
```

To make this compile against the local package without publishing, the example
imports the package by name; the typecheck step below uses a path alias.

- [ ] **Step 2: Add an example typecheck script and a tsconfig path**

Append to `package.json` `scripts`:
```json
"typecheck:example": "tsc --noEmit -p examples/tsconfig.json"
```

Create `examples/tsconfig.json`:
```json
{
  "compilerOptions": {
    "target": "ES2022",
    "module": "ESNext",
    "moduleResolution": "Bundler",
    "types": ["node"],
    "strict": true,
    "skipLibCheck": true,
    "noEmit": true,
    "baseUrl": ".",
    "paths": { "@gsxhq/vite-plugin-gsx": ["../src/index.ts"] }
  },
  "include": ["vite.config.ts"]
}
```

- [ ] **Step 3: Verify the example type-checks**

Run:
```bash
cd /Users/jackieli/personal/gsxhq/vite-plugin-gsx
npm run typecheck:example
```
Expected: no errors (the `gsx()` call and options are valid against `src/index.ts`).

- [ ] **Step 4: Write `README.md`**

Include these sections (full prose, not placeholders):

1. **Title + one-line description** and an explanation that gsx renders HTML server-side, so the plugin does regenerate + full-reload (not JS HMR).
2. **Install:** `npm i -D @gsxhq/vite-plugin-gsx`, and the prerequisite `go tool gsx` setup (`go get -tool github.com/gsxhq/gsx/cmd/gsx` so `go tool gsx` works).
3. **Quick start:** the `examples/vite.config.ts` content above.
4. **How the loop works:** the data-flow diagram from the spec (vite → watch `.gsx` → `go tool gsx generate` → wgo rebuilds Go → Go POSTs `/__reload` → full-reload), and the key invariant that reload is driven by the Go-POST, not the file change.
5. **The three project-side pieces (documented glue):**
   - **Proxy** — the `server.proxy` block (from the example).
   - **Client script** — `<script type="module" src="/@vite/client"></script>` in the layout `<head>`, dev-gated. Show a gsx snippet:
     ```gsx
     component Layout(title string) {
       <head>
         <title>{title}</title>
         if dev { <script type="module" src="/@vite/client"></script> }
       </head>
     }
     ```
   - **Reload notify** — the Go boot snippet (verbatim, copy-ready):
     ```go
     // NotifyViteReload pokes the Vite dev server after this binary boots so any
     // browser tab holding an @vite/client socket reloads. Dev-only: no-ops when
     // VITE_DEV_URL is unset.
     func NotifyViteReload(viteDevURL string) {
         if viteDevURL == "" {
             return
         }
         go func() {
             ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
             defer cancel()
             for range 10 {
                 req, err := http.NewRequestWithContext(ctx, http.MethodPost, viteDevURL+"/__reload", nil)
                 if err != nil {
                     return
                 }
                 if resp, err := http.DefaultClient.Do(req); err == nil {
                     resp.Body.Close()
                     return
                 }
                 select {
                 case <-ctx.Done():
                     return
                 case <-time.After(150 * time.Millisecond):
                 }
             }
         }()
     }
     ```
   - A note that `wgo`/`air` watches `.go` to rebuild+restart the Go server (the plugin only regenerates `.x.go`), with a one-line `wgo` example:
     `go tool wgo -file=.go go build -o tmp/app ./cmd/app :: tmp/app`
6. **Options table:** every `GsxOptions` field, its default, and meaning (copied from `src/options.ts` doc comments).
7. **Notes:** dev-only (`apply: "serve"`); production generate belongs in `//go:generate` / CI; full-reload only.

- [ ] **Step 5: Commit**

```bash
cd /Users/jackieli/personal/gsxhq/vite-plugin-gsx
git add -A
git commit -m "docs: README with dev-loop recipe + type-checked example config"
```

---

## Final verification (after all tasks)

- [ ] Run the whole suite, typecheck, example typecheck, and build:
```bash
cd /Users/jackieli/personal/gsxhq/vite-plugin-gsx
npm test && npm run typecheck && npm run typecheck:example && npm run build
```
Expected: all green; `dist/index.js` + `dist/index.d.ts` emitted.

- [ ] Confirm `git log --oneline` shows the six task commits and the tree is clean.

Publishing to npm and creating the GitHub `gsxhq/vite-plugin-gsx` remote are follow-ups handled outside this plan (they need npm/GitHub credentials and an org decision), and are intentionally not tasks here.
