# @gsxhq/vite-plugin-gsx v0.2.0 — devFallback Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a `devFallback` export to `@gsxhq/vite-plugin-gsx` — a backend-down interstitial (tails the dev log, polls `/__dev/status`, auto-reloads) wired to the proxy's `onError` — and ship it as v0.2.0.

**Architecture:** A new `src/dev-fallback.ts` module exporting `devFallback(opts)`, a factory returning `{ plugin, configureProxy }`. `plugin` registers a `/__dev/status` Vite middleware (`{ up, log }`); `configureProxy` attaches `proxy.on("error", …)` that serves an interstitial HTML page. Stdlib-only Node (`node:fs`, `node:http`). Re-exported from `src/index.ts`.

**Tech Stack:** TypeScript, Vite (peer), vitest, tsup. Node ≥18.

**Spec:** `docs/superpowers/specs/2026-06-25-gsx-dev-fallback-design.md` (sub-project A).

## Global Constraints

- Repo: `/Users/jackieli/personal/gsxhq/vite-plugin-gsx`. Stdlib-only Node (no new deps).
- `devFallback(opts: DevFallbackOptions): DevFallback` where `DevFallbackOptions = { target: string; logFile?: string; healthPath?: string; statusPath?: string }` (defaults `logFile "tmp/dev.log"`, `healthPath "/healthz"`, `statusPath "/__dev/status"`) and `DevFallback = { plugin: Plugin; configureProxy: (proxy: any) => void }`.
- `plugin.apply = "serve"`; registers `GET <statusPath>` → JSON `{ up: boolean, log: string }`. `up` = a `GET <target><healthPath>` (800ms timeout) with status 2xx–4xx; 5xx/transport-error/timeout ⇒ down. `log` = last ~20KB of `logFile`.
- `configureProxy` attaches `proxy.on("error", (err, req, res) => …)`: HTTP `res` (has `writeHead`) → 503 + interstitial HTML; socket `res` (no `writeHead`) → `destroy()`.
- The interstitial carries `<script type="module" src="/@vite/client">`, polls `<statusPath>` every 1s, reloads on `{ up: true }`. Dev-only.
- Bump to **v0.2.0**.

---

### Task 1: `dev-fallback.ts` module + tests

**Files:**
- Create: `/Users/jackieli/personal/gsxhq/vite-plugin-gsx/src/dev-fallback.ts`
- Create: `/Users/jackieli/personal/gsxhq/vite-plugin-gsx/test/dev-fallback.test.ts`

**Interfaces:**
- Produces:
  ```ts
  interface DevFallbackOptions { target: string; logFile?: string; healthPath?: string; statusPath?: string }
  interface DevFallback { plugin: Plugin; configureProxy: (proxy: any) => void }
  function devFallback(opts: DevFallbackOptions): DevFallback
  function backendUp(target: string, healthPath?: string): Promise<boolean>   // exported for tests
  function readLogTail(logFile: string, maxBytes?: number): string            // exported for tests
  function serveBackendDown(res: any, html: string): void                     // exported for tests
  ```

- [ ] **Step 1: Write the failing test** — `test/dev-fallback.test.ts`

```ts
import { describe, it, expect, beforeEach, afterEach } from "vitest";
import { createServer as createHttp, type Server } from "node:http";
import { mkdtempSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { createServer, type ViteDevServer } from "vite";
import {
  devFallback,
  backendUp,
  readLogTail,
  serveBackendDown,
} from "../src/dev-fallback.js";

// fakeUpstream starts an http server answering /healthz with the given status.
function fakeUpstream(status: number): Promise<{ url: string; close: () => void }> {
  return new Promise((resolve) => {
    const srv = createHttp((req, res) => {
      if (req.url === "/healthz") {
        res.statusCode = status;
        res.end("ok");
      } else {
        res.statusCode = 404;
        res.end();
      }
    });
    srv.listen(0, () => {
      const port = (srv.address() as { port: number }).port;
      resolve({ url: `http://localhost:${port}`, close: () => srv.close() });
    });
  });
}

describe("backendUp", () => {
  it("true when healthz is 200", async () => {
    const up = await fakeUpstream(200);
    expect(await backendUp(up.url)).toBe(true);
    up.close();
  });
  it("false when healthz is 503", async () => {
    const up = await fakeUpstream(503);
    expect(await backendUp(up.url)).toBe(false);
    up.close();
  });
  it("false when the upstream is unreachable", async () => {
    expect(await backendUp("http://localhost:1")).toBe(false);
  });
});

describe("readLogTail", () => {
  it("returns the tail of the log file", () => {
    const dir = mkdtempSync(join(tmpdir(), "gsxlog-"));
    const f = join(dir, "dev.log");
    writeFileSync(f, "line1\nline2\nBOOT ERROR xyz\n");
    expect(readLogTail(f)).toContain("BOOT ERROR xyz");
  });
  it("returns a note when the file is missing", () => {
    expect(readLogTail("/no/such/file.log")).toContain("unavailable");
  });
});

describe("serveBackendDown", () => {
  it("writes 503 + HTML to an HTTP response", () => {
    let status = 0;
    let body = "";
    const res: any = {
      headersSent: false,
      writeHead(s: number) { status = s; },
      end(s: string) { body = s; },
    };
    serveBackendDown(res, "<html>INTERSTITIAL</html>");
    expect(status).toBe(503);
    expect(body).toContain("INTERSTITIAL");
  });
  it("destroys a socket-like response (no writeHead)", () => {
    let destroyed = false;
    const sock: any = { destroy() { destroyed = true; } };
    serveBackendDown(sock, "<html></html>");
    expect(destroyed).toBe(true);
  });
});

describe("devFallback factory", () => {
  let upstream: { url: string; close: () => void };
  let server: ViteDevServer;
  let http: Server | undefined;
  beforeEach(async () => {
    upstream = await fakeUpstream(200);
  });
  afterEach(async () => {
    http?.close();
    await server?.close();
    upstream.close();
  });

  it("plugin serves /__dev/status with {up, log}", async () => {
    const dir = mkdtempSync(join(tmpdir(), "gsxlog-"));
    const logFile = join(dir, "dev.log");
    writeFileSync(logFile, "hello from the dev log\n");
    const fb = devFallback({ target: upstream.url, logFile });

    server = await createServer({
      logLevel: "silent",
      server: { middlewareMode: true, hmr: false },
      plugins: [fb.plugin],
    });
    http = createHttp(server.middlewares);
    await new Promise<void>((r) => http!.listen(0, r));
    const port = (http.address() as { port: number }).port;

    const resp = await fetch(`http://localhost:${port}/__dev/status`);
    const json = (await resp.json()) as { up: boolean; log: string };
    expect(json.up).toBe(true);
    expect(json.log).toContain("hello from the dev log");
  });

  it("configureProxy registers an error handler that serves the interstitial", () => {
    const fb = devFallback({ target: upstream.url });
    let handler: ((e: unknown, req: unknown, res: any) => void) | undefined;
    const proxy: any = { on(ev: string, fn: any) { if (ev === "error") handler = fn; } };
    fb.configureProxy(proxy);
    expect(handler).toBeTypeOf("function");

    let status = 0;
    let body = "";
    const res: any = { headersSent: false, writeHead(s: number) { status = s; }, end(s: string) { body = s; } };
    handler!(new Error("ECONNREFUSED"), {}, res);
    expect(status).toBe(503);
    expect(body).toContain("Backend");          // interstitial title/heading
    expect(body).toContain("/__dev/status");     // the poll target
  });
});
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd /Users/jackieli/personal/gsxhq/vite-plugin-gsx && npx vitest run test/dev-fallback.test.ts`
Expected: FAIL — `Cannot find module '../src/dev-fallback.js'`.

- [ ] **Step 3: Write `src/dev-fallback.ts`**

```ts
import fs from "node:fs";
import http from "node:http";
import type { Plugin } from "vite";

export interface DevFallbackOptions {
  /** Go upstream origin, e.g. "http://localhost:7777". */
  target: string;
  /** Combined dev log to tail in the interstitial. Default "tmp/dev.log". */
  logFile?: string;
  /** Backend liveness endpoint. Default "/healthz". */
  healthPath?: string;
  /** Status endpoint to register. Default "/__dev/status". */
  statusPath?: string;
}

export interface DevFallback {
  /** Registers GET <statusPath> → { up, log }. */
  plugin: Plugin;
  /** Vite proxy `configure` hook: serves the interstitial on a proxy error. */
  configureProxy: (proxy: any) => void;
}

// devFallback returns a Vite plugin + a proxy configure hook that together turn
// a down/restarting Go backend into a self-recovering interstitial instead of a
// raw proxy error. Dev-only.
export function devFallback(opts: DevFallbackOptions): DevFallback {
  const logFile = opts.logFile ?? "tmp/dev.log";
  const healthPath = opts.healthPath ?? "/healthz";
  const statusPath = opts.statusPath ?? "/__dev/status";
  const html = interstitial(statusPath);

  const plugin: Plugin = {
    name: "vite-plugin-gsx:dev-fallback",
    apply: "serve",
    configureServer(server) {
      server.middlewares.use(statusPath, async (_req, res) => {
        const up = await backendUp(opts.target, healthPath);
        res.setHeader("content-type", "application/json");
        res.setHeader("cache-control", "no-store");
        res.end(JSON.stringify({ up, log: readLogTail(logFile) }));
      });
    },
  };

  const configureProxy = (proxy: any) => {
    proxy.on("error", (_err: unknown, _req: unknown, res: any) => {
      serveBackendDown(res, html);
    });
  };

  return { plugin, configureProxy };
}

// backendUp resolves true once the backend answers healthPath with a non-5xx
// status (up AND ready). A 5xx, transport error, or timeout is treated as down.
export function backendUp(target: string, healthPath = "/healthz"): Promise<boolean> {
  return new Promise((resolve) => {
    let done = false;
    const finish = (v: boolean) => {
      if (!done) {
        done = true;
        resolve(v);
      }
    };
    const req = http.get(new URL(healthPath, target), { timeout: 800 }, (res) => {
      res.resume();
      const code = res.statusCode ?? 0;
      finish(code >= 200 && code < 500);
    });
    req.on("error", () => finish(false));
    req.on("timeout", () => {
      req.destroy();
      finish(false);
    });
  });
}

// readLogTail returns the last maxBytes of logFile, or a short note if unreadable.
export function readLogTail(logFile: string, maxBytes = 20000): string {
  let fd: number | undefined;
  try {
    fd = fs.openSync(logFile, "r");
    const { size } = fs.fstatSync(fd);
    const start = Math.max(0, size - maxBytes);
    const buf = Buffer.alloc(size - start);
    fs.readSync(fd, buf, 0, buf.length, start);
    return buf.toString("utf8");
  } catch (e) {
    return `(${logFile} unavailable: ${(e as Error).message})`;
  } finally {
    if (fd !== undefined) fs.closeSync(fd);
  }
}

// serveBackendDown writes the interstitial on a proxy error. A failed WS upgrade
// passes a net.Socket (no writeHead) — destroy it; the client reconnects.
export function serveBackendDown(res: any, html: string): void {
  if (typeof res.writeHead !== "function") {
    try {
      res.destroy();
    } catch {
      /* socket already gone */
    }
    return;
  }
  if (!res.headersSent) {
    res.writeHead(503, {
      "content-type": "text/html; charset=utf-8",
      "cache-control": "no-store",
    });
  }
  res.end(html);
}

// interstitial builds the dark recovery page. It carries @vite/client (so a
// clean restart's /__reload push reloads it) and polls statusPath every 1s,
// reloading when the backend is up.
function interstitial(statusPath: string): string {
  return `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Backend restarting…</title>
<script type="module" src="/@vite/client"></script>
<style>
  :root { color-scheme: dark; }
  html, body { height: 100%; }
  body { font: 13px/1.6 ui-monospace, SFMono-Regular, Menlo, monospace; margin: 0; background: #0b0d10; color: #e6e6e6; display: flex; flex-direction: column; }
  header { padding: 16px 20px; border-bottom: 1px solid #23262b; flex: none; }
  h1 { font-size: 15px; margin: 0 0 6px; }
  #status { color: #f0b429; }
  .hint { color: #6b7280; font-size: 12px; margin-top: 4px; }
  pre { margin: 0; padding: 16px 20px; white-space: pre-wrap; word-break: break-word; font-size: 12px; color: #c9d1d9; background: #0b0d10; flex: 1 1 auto; min-height: 0; overflow: auto; }
</style>
</head>
<body>
<header>
  <h1>Backend unavailable</h1>
  <div id="status">checking…</div>
  <div class="hint">Vite is up; waiting on the Go server. This page reloads automatically when it returns. Tail of the dev log below.</div>
</header>
<pre id="log">loading…</pre>
<script>
(function () {
  var statusEl = document.getElementById("status");
  var logEl = document.getElementById("log");
  var tries = 0;
  function poll() {
    tries++;
    fetch("${statusPath}", { cache: "no-store" }).then(function (r) { return r.json(); }).then(function (s) {
      if (s.log) { logEl.textContent = s.log; logEl.scrollTop = logEl.scrollHeight; }
      if (s.up) { statusEl.textContent = "back up — reloading…"; location.reload(); return; }
      statusEl.textContent = "Go server down — retrying (attempt " + tries + ", " + new Date().toLocaleTimeString() + ")";
      setTimeout(poll, 1000);
    }).catch(function (e) {
      statusEl.textContent = "dev status check failed: " + e;
      setTimeout(poll, 1000);
    });
  }
  poll();
})();
</script>
</body>
</html>`;
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `cd /Users/jackieli/personal/gsxhq/vite-plugin-gsx && npx vitest run test/dev-fallback.test.ts`
Expected: PASS — all dev-fallback tests green.

- [ ] **Step 5: Commit**

```bash
cd /Users/jackieli/personal/gsxhq/vite-plugin-gsx
git add src/dev-fallback.ts test/dev-fallback.test.ts
git commit -m "feat: devFallback — backend-down interstitial + /__dev/status"
```

---

### Task 2: Export, bump to v0.2.0, README, build

**Files:**
- Modify: `/Users/jackieli/personal/gsxhq/vite-plugin-gsx/src/index.ts`
- Modify: `/Users/jackieli/personal/gsxhq/vite-plugin-gsx/package.json`
- Modify: `/Users/jackieli/personal/gsxhq/vite-plugin-gsx/README.md`

**Interfaces:**
- Consumes: `devFallback`, `DevFallbackOptions`, `DevFallback` (Task 1).

- [ ] **Step 1: Re-export from `src/index.ts`**

Add near the top (after the existing exports):
```ts
export { devFallback } from "./dev-fallback.js";
export type { DevFallbackOptions, DevFallback } from "./dev-fallback.js";
```

- [ ] **Step 2: Bump the version** — `package.json`

Change `"version": "0.1.0"` to `"version": "0.2.0"`.

- [ ] **Step 3: Verify the public API typechecks + builds**

Run (the package is ESM, so use a dynamic ESM import to verify the export):
```bash
cd /Users/jackieli/personal/gsxhq/vite-plugin-gsx
npm run typecheck
npm run build
node --input-type=module -e "import { devFallback } from './dist/index.js'; if (typeof devFallback !== 'function') throw new Error('devFallback not exported'); console.log('devFallback exported OK');"
```
Expected: typecheck clean; build emits `dist/`; the import check prints `devFallback exported OK`.

- [ ] **Step 4: Document in `README.md`**

Add a `## Dev fallback (backend-restart resilience)` section: what it does (interstitial on backend-down, tails the dev log, auto-reloads), the `devFallback({ target, logFile, healthPath, statusPath })` → `{ plugin, configureProxy }` API, and the wiring snippet:
```ts
import { gsx, devFallback } from "@gsxhq/vite-plugin-gsx";
const fallback = devFallback({ target: "http://localhost:7777", logFile: "tmp/dev.log" });
export default defineConfig({
  plugins: [gsx(), fallback.plugin],
  server: { proxy: { "^(?!/@vite|/@id|/@fs|/web/|/node_modules|/__reload|/__dev).*": {
    target: "http://localhost:7777", changeOrigin: true, configure: fallback.configureProxy,
  } } },
});
```
Note it requires a `/healthz` on the Go server and a `tmp/dev.log` (the Taskfile tees to it). Dev-only.

- [ ] **Step 5: Run the full suite + commit**

Run: `cd /Users/jackieli/personal/gsxhq/vite-plugin-gsx && npm test && npm run build`
Expected: all tests pass; build clean.
```bash
git add src/index.ts package.json README.md
git commit -m "feat: export devFallback; bump to v0.2.0; README"
```

---

## Final verification (after all tasks)

- [ ] `npm test`, `npm run typecheck`, `npm run build` — all green; `dist/index.js` exports `devFallback`.
- [ ] `git log --oneline` shows the two task commits; tree clean.

Publishing (`npm publish` + tag) is a follow-up handled outside this plan (needs the npm OTP). Sub-project B (`gsx init` template updates) consumes `@gsxhq/vite-plugin-gsx@^0.2.0` and is its own plan.
