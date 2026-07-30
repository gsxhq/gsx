# Dev Panel ANSI Colors Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Render the gsx dev panel's backend-log tail as colored HTML instead of raw ANSI escape noise.

**Architecture:** The panel's log box currently HTML-escapes the whole log tail into a `<pre>`. Insert a two-stage pipeline: a pure pre-pass helper in `client-logic.ts` (collapse `\r` overwrites, strip OSC strings) feeding `ansi_up`'s `ansi_to_html`, which does the SGR→HTML conversion and its own HTML escaping. Basic-16 colors come out as `ansi-*` classes styled for the panel's dark background.

**Tech Stack:** TypeScript, vitest, tsup, `ansi_up@^6` (zero-dependency, browser-first).

## Global Constraints

- **Repo:** ALL work in `~/personal/gsxhq/vite-plugin-gsx` — NOT the `gsx` repo. Only this plan and its spec live in `gsx`.
- **Branch:** work on `ansi-log-colors`, created off `main`. Never commit to `main`.
- **Spec:** `~/personal/gsxhq/gsx/docs/superpowers/specs/2026-07-30-dev-panel-ansi-colors-design.md`
- `dist/client.js` must stay a dependency-free browser module: no runtime import of `vite`, and `ansi_up` must be *inlined* by tsup, never left as a bare external specifier.
- No gsx syntax/codegen change, so **no corpus case** and no `make ci` in the gsx repo.
- Test commands: `npm test` (vitest, `environment: "node"`), `npm run typecheck`, `npm run build`.
- The test suite uses hand-written DOM fakes (`test/client.test.ts`), not jsdom. Do not add jsdom.
- Verified `ansi_up@6.0.6` behavior — do not re-derive, and do not assume beyond it:
  - `import { AnsiUp } from "ansi_up"` (named export; no default export).
  - `use_classes = true` affects **only** the 16 basic colors → `class="ansi-red-fg"`, `class="ansi-bright-green-fg"`, `class="ansi-blue-bg"`. 256-color and truecolor still emit `style="color:rgb(r,g,b)"`; bold/dim/italic/underline always emit inline styles.
  - Escapes HTML itself: `<script>` → `&lt;script&gt;`, `"` → `&quot;`.
  - Consumes cursor-movement (`ESC[1A`) and erase-line (`ESC[2K`) silently.
  - Does **not** touch `\r`.
  - Leaks OSC strings as literal text: `ESC]0;my title BEL` → `]0;my title`.
  - Handles OSC 8 hyperlinks → `<a href="…">`, with `url_allowlist` defaulting to `{http:1, https:1}` and the URL HTML-escaped.

---

### Task 1: `\r`/OSC pre-pass helper

Pure text→text helper, no DOM, no library. It is the only part of this feature that needs its own reasoning, so it lands and gets reviewed on its own.

**Files:**
- Modify: `src/client-logic.ts` (append at end of file, after `logTruncationBanner`)
- Test: `test/client-logic.test.ts` (append a new `describe` at end of file)

**Interfaces:**
- Consumes: nothing.
- Produces: `export function normalizeLogText(text: string): string` — Task 2 calls it on the raw log tail before handing the result to `ansi_to_html`.

- [ ] **Step 1: Create the branch**

```bash
cd ~/personal/gsxhq/vite-plugin-gsx
git checkout main && git pull --ff-only
git checkout -b ansi-log-colors
git rev-parse --abbrev-ref HEAD   # must print: ansi-log-colors
```

- [ ] **Step 2: Write the failing tests**

Append to `test/client-logic.test.ts`. Note the escape literals: `\x1b` is ESC, `\x07` is BEL. Import `normalizeLogText` by adding it to the existing top-of-file import from `../src/client-logic.js`.

```ts
describe("normalizeLogText", () => {
  it("leaves text with no carriage returns or OSC untouched", () => {
    expect(normalizeLogText("plain\nlines\n")).toBe("plain\nlines\n");
    expect(normalizeLogText("")).toBe("");
  });

  it("collapses a \\r-overwritten line to its final segment", () => {
    expect(normalizeLogText("downloading 10%\rdownloading 99%\rdone\nnext\n")).toBe("done\nnext\n");
  });

  it("collapses each line independently", () => {
    expect(normalizeLogText("a\rb\nc\rd\n")).toBe("b\nd\n");
  });

  it("re-prepends SGR sequences from the discarded prefix", () => {
    // SGR state persists across \r in a real terminal, so the kept segment
    // must stay green.
    expect(normalizeLogText("\x1b[32m10%\r99% done")).toBe("\x1b[32m99% done");
  });

  it("re-prepends every SGR sequence from the prefix, in order", () => {
    expect(normalizeLogText("\x1b[1m\x1b[31mx\ry")).toBe("\x1b[1m\x1b[31my");
  });

  it("does not re-prepend SGR from the kept segment's own prefix twice", () => {
    expect(normalizeLogText("a\r\x1b[32mb")).toBe("\x1b[32mb");
  });

  it("keeps a trailing \\r\\n line ending intact", () => {
    // CRLF: the \r immediately precedes the newline, so the "segment after
    // the last \r" is empty and the line's content must not be dropped.
    expect(normalizeLogText("hello\r\nworld\r\n")).toBe("hello\nworld\n");
  });

  it("strips a BEL-terminated OSC string", () => {
    expect(normalizeLogText("a\x1b]0;my title\x07b")).toBe("ab");
  });

  it("strips an ST-terminated OSC string", () => {
    expect(normalizeLogText("a\x1b]0;my title\x1b\\b")).toBe("ab");
  });

  it("preserves OSC 8 hyperlinks for ansi_up to handle", () => {
    const link = "\x1b]8;;http://x\x07link\x1b]8;;\x07";
    expect(normalizeLogText(link)).toBe(link);
  });

  it("does not let an unterminated OSC string eat the rest of the log", () => {
    // A byte-sliced tail can end mid-OSC. Dropping everything after it would
    // blank the newest output, which is the part the user is watching.
    expect(normalizeLogText("keep me\x1b]0;never terminated")).toBe("keep me\x1b]0;never terminated");
  });

  it("leaves SGR sequences alone", () => {
    expect(normalizeLogText("\x1b[31mred\x1b[0m\n")).toBe("\x1b[31mred\x1b[0m\n");
  });
});
```

- [ ] **Step 3: Run the tests to verify they fail**

```bash
cd ~/personal/gsxhq/vite-plugin-gsx && npx vitest run test/client-logic.test.ts -t normalizeLogText
```

Expected: FAIL — `normalizeLogText is not a function` / TypeScript reports no such export.

- [ ] **Step 4: Implement the helper**

Append to `src/client-logic.ts`:

```ts
// ---------------------------------------------------------------------------
// Log pre-pass. Two things ansi_up (verified against 6.0.6) does not do:
// carriage-return overwrite, and OSC strings — which leak through as literal
// text (`ESC]0;title BEL` renders as `]0;title`). Runs before ansi_to_html,
// on raw log text.

// OSC introducer through its terminator (BEL or ST), excluding OSC 8 —
// ansi_up turns those into real anchors (http/https only, URL escaped), so
// they are its business, not ours. Requiring the terminator means an
// unterminated OSC at a truncated tail's end is left as-is rather than
// swallowing the newest output.
const OSC_RE = /\x1b\](?!8;)[\s\S]*?(?:\x07|\x1b\\)/g;
const SGR_RE = /\x1b\[[0-9;]*m/g;

/**
 * Strips OSC strings and applies carriage-return overwrite, so a `\r`-driven
 * progress line collapses to its final state instead of stacking. SGR state
 * persists across a `\r` in a real terminal, so sequences from the discarded
 * prefix are re-prepended to the kept segment.
 */
export function normalizeLogText(text: string): string {
  const stripped = text.replace(OSC_RE, "");
  if (!stripped.includes("\r")) return stripped;
  return stripped
    .split("\n")
    .map((line) => {
      // A CRLF log ends every line with a \r that is NOT an overwrite — it is
      // half the line ending, left over from splitting on \n. Ignore that one
      // (and drop it, so a bare CR never reaches the DOM); an overwriting \r
      // is one with content after it.
      const crlf = line.endsWith("\r");
      const body = crlf ? line.slice(0, -1) : line;
      const cut = body.lastIndexOf("\r");
      if (cut === -1) return body;
      const carried = (body.slice(0, cut).match(SGR_RE) ?? []).join("");
      return carried + body.slice(cut + 1);
    })
    .join("\n");
}
```

The CRLF handling is the subtle part: `"hello\r\n"` splits to the line `"hello\r"`, whose last `\r` is at the end. Treating that as an overwrite would keep the empty segment after it and render a blank line, losing `hello`. Stripping the trailing `\r` first — before looking for an overwrite — is what the `"keeps a trailing \r\n line ending intact"` test pins.

- [ ] **Step 5: Run the tests to verify they pass**

```bash
cd ~/personal/gsxhq/vite-plugin-gsx && npx vitest run test/client-logic.test.ts -t normalizeLogText && npm run typecheck
```

Expected: all `normalizeLogText` tests PASS, typecheck clean.

- [ ] **Step 6: Run the whole suite**

```bash
cd ~/personal/gsxhq/vite-plugin-gsx && npm test
```

Expected: PASS, no regressions.

- [ ] **Step 7: Commit**

```bash
cd ~/personal/gsxhq/vite-plugin-gsx
git add src/client-logic.ts test/client-logic.test.ts
git commit -m "feat: pre-pass log text for carriage returns and OSC strings"
```

---

### Task 2: Render the log through ansi_up

**Files:**
- Modify: `package.json` (add `ansi_up` to `dependencies`)
- Modify: `tsup.config.ts` (add `noExternal`)
- Modify: `src/client.ts:124` (the `<pre id="gsx-log-box">` body) and its import block
- Test: `test/client.test.ts` (append a new `describe` at end of file)

**Interfaces:**
- Consumes: `normalizeLogText(text: string): string` from Task 1.
- Produces: colored markup inside `<pre id="gsx-log-box">`. Task 3 styles the `ansi-*` classes it emits.

- [ ] **Step 1: Add the dependency and inline it in the bundle**

```bash
cd ~/personal/gsxhq/vite-plugin-gsx && npm install ansi_up@^6
```

Then edit `tsup.config.ts` so `ansi_up` is bundled into `dist/client.js` rather than left as a bare specifier the *user's* vite would have to resolve out of nested `node_modules`:

```ts
export default defineConfig({
  entry: ["src/index.ts", "src/client.ts"],
  format: ["esm"],
  dts: true,
  clean: true,
  target: "node18",
  noExternal: ["ansi_up"],
});
```

- [ ] **Step 2: Write the failing tests**

Append to `test/client.test.ts`. This reuses the file's existing helpers — `installFakeDom`, `loadClient`, `makeHot`, `press`, `fakeLogResponse` — exactly as the `describe("log box", …)` block above it does. The only new thing is `renderWithLog`, which collapses that block's boilerplate (press to show, push a `building` status, let the probe fetch resolve) into one call returning the rendered shadow-root HTML.

```ts
describe("log box ANSI rendering", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date("2026-07-24T12:00:00Z"));
  });

  // Serve `body` from /__gsx/log, open the panel mid-build so the log box
  // expands, and return the rendered shadow-root HTML. Fresh module + fake
  // DOM per call, so no state leaks between cases.
  async function renderWithLog(body: string): Promise<string> {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => fakeLogResponse(true, body, "0")),
    );
    const { bodyChildren, keydownListeners } = installFakeDom();
    const { init } = await loadClient();
    const hot = makeHot();
    init({ key: "d", hot } as any);
    const host = bodyChildren[0]!;
    press(keydownListeners);
    hot.handlers["gsx:status"]!({ phase: "building", phaseSince: "2026-07-24T12:00:00Z" });
    await vi.advanceTimersByTimeAsync(0);
    return host.shadow.innerHTML;
  }

  // Each case: log text served by /__gsx/log -> assertion on the rendered
  // shadow-root HTML.
  it("renders SGR colors as ansi-* classes", async () => {
    const html = await renderWithLog("\x1b[31mred\x1b[0m");
    expect(html).toContain('<span class="ansi-red-fg">red</span>');
  });

  it("renders bright colors and bold", async () => {
    const html = await renderWithLog("\x1b[1;92mboldbright\x1b[0m");
    expect(html).toContain('class="ansi-bright-green-fg"');
    expect(html).toContain("font-weight:bold");
  });

  it("renders 256-color and truecolor as inline rgb", async () => {
    const html = await renderWithLog("\x1b[38;5;208m256\x1b[0m \x1b[38;2;10;20;30mtrue\x1b[0m");
    expect(html).toContain("color:rgb(255,135,0)");
    expect(html).toContain("color:rgb(10,20,30)");
  });

  it("escapes HTML in log content", async () => {
    const html = await renderWithLog('<script>alert(1)</script> & "q"');
    expect(html).not.toContain("<script>alert(1)");
    expect(html).toContain("&lt;script&gt;alert(1)&lt;/script&gt;");
  });

  it("does not turn a javascript: OSC 8 hyperlink into an anchor", async () => {
    const html = await renderWithLog("\x1b]8;;javascript:alert(1)\x07click\x1b]8;;\x07");
    expect(html).not.toContain("javascript:alert(1)");
    expect(html).not.toContain("<a href");
  });

  it("collapses \\r progress lines", async () => {
    const html = await renderWithLog("10%\r50%\r100%\n");
    expect(html).toContain("100%");
    expect(html).not.toContain("10%\r");
  });

  it("strips OSC title sequences", async () => {
    const html = await renderWithLog("a\x1b]0;my title\x07b");
    expect(html).not.toContain("my title");
    expect(html).toContain("ab");
  });

  it("does not carry color state from one poll into the next", async () => {
    // A tail that starts mid-escape or never resets must not tint the NEXT
    // poll of the SAME panel instance. A fresh AnsiUp per render is what
    // guarantees this — so this case must drive two polls through one client,
    // not two renderWithLog calls (those reset the module and would pass
    // even with a shared converter).
    let body = "\x1b[31mno reset here";
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => fakeLogResponse(true, body, "0")),
    );
    const { bodyChildren, keydownListeners } = installFakeDom();
    const { init } = await loadClient();
    const hot = makeHot();
    init({ key: "d", hot } as any);
    const host = bodyChildren[0]!;
    press(keydownListeners);
    hot.handlers["gsx:status"]!({ phase: "building", phaseSince: "2026-07-24T12:00:00Z" });
    await vi.advanceTimersByTimeAsync(0);
    expect(host.shadow.innerHTML).toContain("ansi-red-fg");

    body = "plain";
    await vi.advanceTimersByTimeAsync(1000);
    expect(host.shadow.innerHTML).toContain("plain");
    expect(host.shadow.innerHTML).not.toContain("ansi-red-fg");
  });
});
```

- [ ] **Step 3: Run the tests to verify they fail**

```bash
cd ~/personal/gsxhq/vite-plugin-gsx && npx vitest run test/client.test.ts -t "log box ANSI"
```

Expected: FAIL — the assertions find escaped `&lt;` ANSI text (`&#x1b;[31m` / literal `[31m`) instead of spans.

- [ ] **Step 4: Wire it into the render path**

In `src/client.ts`, add to the import from `./client-logic.js`:

```ts
  normalizeLogText,
```

and add the library import next to it:

```ts
import { AnsiUp } from "ansi_up";
```

In `render()`, before building the markup, construct a fresh converter — one per render, so a truncated or unreset escape in one poll cannot tint the next:

```ts
    const ansi = new AnsiUp();
    ansi.use_classes = true;
    const logHtml = ansi.ansi_to_html(normalizeLogText(logText));
```

Then in the template at `src/client.ts:124`, replace `escapeHtml(logText)` with `logHtml`:

```ts
            ? `${banner ? `<p class="logbanner">${escapeHtml(banner)}</p>` : ""}<pre id="gsx-log-box">${logHtml}</pre>`
```

Leave `escapeHtml(banner)` and every other `escapeHtml` call site alone — only the log body changes.

- [ ] **Step 5: Run the tests to verify they pass**

```bash
cd ~/personal/gsxhq/vite-plugin-gsx && npx vitest run test/client.test.ts -t "log box ANSI" && npm test && npm run typecheck
```

Expected: new tests PASS, full suite PASS, typecheck clean.

- [ ] **Step 6: Verify ansi_up is actually inlined, not external**

```bash
cd ~/personal/gsxhq/vite-plugin-gsx && npm run build
grep -c "from *[\"']ansi_up[\"']" dist/client.js   # must print 0
grep -c "ansi-bright-green-fg" dist/client.js      # must print 1 or more
grep -c "from *[\"']vite[\"']" dist/client.js      # must print 0
```

Expected: `0`, then a nonzero count, then `0`. A nonzero first count means `noExternal` did not take effect — fix that before committing.

- [ ] **Step 7: Commit**

```bash
cd ~/personal/gsxhq/vite-plugin-gsx
git add package.json package-lock.json tsup.config.ts src/client.ts test/client.test.ts
git commit -m "feat: render dev-panel log tail with ANSI colors"
```

---

### Task 3: Panel-tuned ANSI palette

`use_classes` emits class names with no styles behind them, so after Task 2 basic-16 colors render *unstyled* — correct markup, no color. This task supplies the CSS, tuned for the panel's `#101013` log-box background where default ANSI black and blue are unreadable.

**Files:**
- Modify: `src/client.ts` (the `<style>` block in `render()`, after the `#gsx-log-box` rule)
- Test: `test/client.test.ts` (append to the `describe` from Task 2)

**Interfaces:**
- Consumes: the `ansi-*` class names emitted in Task 2.
- Produces: nothing later tasks depend on.

- [ ] **Step 1: Write the failing test**

Append inside the `describe("log box ANSI rendering", …)` block:

```ts
  it("ships styles for every ansi-* class it can emit", async () => {
    const html = await renderWithLog("");
    // One rule per class ansi_up can emit with use_classes, so no color
    // renders as unstyled inherit-colored text.
    for (const color of ["black", "red", "green", "yellow", "blue", "magenta", "cyan", "white"]) {
      for (const variant of [color, `bright-${color}`]) {
        expect(html).toContain(`.ansi-${variant}-fg`);
        expect(html).toContain(`.ansi-${variant}-bg`);
      }
    }
  });
```

- [ ] **Step 2: Run the test to verify it fails**

```bash
cd ~/personal/gsxhq/vite-plugin-gsx && npx vitest run test/client.test.ts -t "ships styles"
```

Expected: FAIL — no `.ansi-black-fg` in the rendered markup.

- [ ] **Step 3: Add the palette to the panel stylesheet**

In `src/client.ts`, inside the template's `<style>` block, immediately after the `#gsx-log-box { … }` rule. Foregrounds are lifted toward the panel's dark background — plain black becomes a visible grey and plain blue a legible mid-blue, since the terminal defaults disappear on `#101013`:

```css
        .ansi-black-fg { color: #6b6b74 } .ansi-red-fg { color: #e05561 }
        .ansi-green-fg { color: #8cc265 } .ansi-yellow-fg { color: #d5a336 }
        .ansi-blue-fg { color: #6a9fd8 } .ansi-magenta-fg { color: #c162de }
        .ansi-cyan-fg { color: #42b3c2 } .ansi-white-fg { color: #d7dae0 }
        .ansi-bright-black-fg { color: #8b8b94 } .ansi-bright-red-fg { color: #ff616e }
        .ansi-bright-green-fg { color: #a5e075 } .ansi-bright-yellow-fg { color: #f0c674 }
        .ansi-bright-blue-fg { color: #8ab7f0 } .ansi-bright-magenta-fg { color: #de73ff }
        .ansi-bright-cyan-fg { color: #4cd1e0 } .ansi-bright-white-fg { color: #f4f4f6 }
        .ansi-black-bg { background: #2b2b31 } .ansi-red-bg { background: #6e2a30 }
        .ansi-green-bg { background: #3d5a2c } .ansi-yellow-bg { background: #6a5320 }
        .ansi-blue-bg { background: #2f4c6e } .ansi-magenta-bg { background: #5c2f6b }
        .ansi-cyan-bg { background: #235b62 } .ansi-white-bg { background: #4a4a52 }
        .ansi-bright-black-bg { background: #3c3c44 } .ansi-bright-red-bg { background: #8c3540 }
        .ansi-bright-green-bg { background: #4e7238 } .ansi-bright-yellow-bg { background: #856828 }
        .ansi-bright-blue-bg { background: #3c608a } .ansi-bright-magenta-bg { background: #743a86 }
        .ansi-bright-cyan-bg { background: #2c737c } .ansi-bright-white-bg { background: #5e5e68 }
```

- [ ] **Step 4: Run the tests to verify they pass**

```bash
cd ~/personal/gsxhq/vite-plugin-gsx && npm test && npm run typecheck
```

Expected: full suite PASS, typecheck clean.

- [ ] **Step 5: Commit**

```bash
cd ~/personal/gsxhq/vite-plugin-gsx
git add src/client.ts test/client.test.ts
git commit -m "feat: style the dev panel's ANSI palette for its dark log box"
```

---

### Task 4: Live verification against a real dev loop

Unit tests assert markup; this task confirms a human actually sees color in a browser. The DOM fakes cannot catch a shadow-DOM styling mistake or a bundling failure in a real vite.

**Files:**
- Modify: `README.md` (the dev-log/panel section around `README.md:298`)

**Interfaces:** none.

- [ ] **Step 1: Build and link the plugin into a scratch gsx project**

```bash
cd ~/personal/gsxhq/vite-plugin-gsx && npm run build
cd ~/personal/gsxhq/gsx && go run ./cmd/gsx init /tmp/ansi-check && cd /tmp/ansi-check
npm install && npm install ~/personal/gsxhq/vite-plugin-gsx
```

- [ ] **Step 2: Emit known ANSI into the dev log and look at the panel**

Add a line to the scaffolded Go server's startup that writes colored output plus a `\r` progress line to stdout, e.g.:

```go
fmt.Print("\x1b[31mred\x1b[0m \x1b[1;92mboldbright\x1b[0m \x1b[38;5;208m256color\x1b[0m\n")
fmt.Print("10%\r50%\r100% done\n")
```

Then run the dev loop with a log file so the panel's log box appears at all:

```bash
cd /tmp/ansi-check && go run ~/personal/gsxhq/gsx/cmd/gsx dev --log-file tmp/dev.log
```

Open the app, press Cmd-D to show the panel, and trigger a rebuild (touch a `.gsx` file) so the box expands during `phase: building`.

- [ ] **Step 3: Confirm with your own eyes, and record what you saw**

Check every one of these, and report each explicitly in your task report:
- `red` is red, `boldbright` is bold bright-green, `256color` is orange.
- No literal `[31m` anywhere in the box.
- The progress line shows `100% done` only — not `10%50%100% done` and not `10%` on its own line.
- Colors are legible against the box background (this is the judgment the unit tests cannot make).
- Take a screenshot and include it in the report.

If any check fails, STOP and report — do not paper over it in CSS.

- [ ] **Step 4: Document the behavior in the README**

In the dev-panel section near `README.md:298`, add one sentence to the existing prose: the log box renders ANSI color from the backend log, collapses `\r` progress lines to their final state, and strips terminal-control sequences. Keep it to a sentence or two — match the surrounding density, do not add a new heading.

- [ ] **Step 5: Clean up the scratch project**

```bash
rm -rf /tmp/ansi-check
```

- [ ] **Step 6: Commit**

```bash
cd ~/personal/gsxhq/vite-plugin-gsx
git add README.md
git commit -m "docs: note ANSI color rendering in the dev panel log box"
```

---

### Task 5: Release

The plugin ships tag-gated: bumping the version and pushing the tag is what publishes.

**Files:**
- Modify: `package.json` (`version`)

- [ ] **Step 1: Confirm the branch is green before releasing anything**

```bash
cd ~/personal/gsxhq/vite-plugin-gsx && npm test && npm run typecheck && npm run build && echo GREEN
```

Expected: `GREEN` printed. If it is not printed, the release does not proceed — report instead.

- [ ] **Step 2: Bump the minor version**

Edit `package.json`: `"version": "0.10.0"` → `"version": "0.11.0"`. A new user-visible panel behavior, no breaking change.

- [ ] **Step 3: Open the PR**

```bash
cd ~/personal/gsxhq/vite-plugin-gsx
git add package.json && git commit -m "chore: release v0.11.0"
git push -u origin ansi-log-colors
gh pr create --title "Colored ANSI output in the dev panel log box" --body "$(cat <<'EOF'
The dev panel's log box rendered raw ANSI escapes as literal `[32m` noise.
It now converts them to colored HTML via `ansi_up`, with a pre-pass that
collapses `\r` progress lines and strips OSC strings.

Spec: gsx `docs/superpowers/specs/2026-07-30-dev-panel-ansi-colors-design.md`
EOF
)"
```

- [ ] **Step 4: STOP — hand back to the user**

Do not merge, do not tag, do not publish. Report the PR URL and the live-verification screenshot from Task 4, and let the user decide.

---

## Notes for the reviewer

- The one genuinely subtle piece is Task 1's CRLF interaction: `"hello\r\n"` must not collapse to an empty line. The test pins it; the implementation note in Step 4 explains the trap.
- Task 3's palette is a judgment call, not a derived value. Reject it on legibility grounds if the Task 4 screenshot looks wrong.
- Security posture rests on two verified `ansi_up` behaviors — its own HTML escaping, and its http/https-only `url_allowlist` for OSC 8 hyperlinks. Both have pinning tests in Task 2. If a future version drops either, those tests fail loudly, which is the intent.
