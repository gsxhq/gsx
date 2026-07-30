# Dev panel: colored log output

## Problem

The dev panel's log box fetches `/__gsx/log` and renders it with
`escapeHtml(logText)` into a `<pre>` (`vite-plugin-gsx/src/client.ts`). The
backend log is written by `gsx dev` from child-process output — `go build`,
`vite`, and the user's own server — none of which strip ANSI. So SGR sequences
show up as literal `[32m` noise, and `\r`-driven progress lines stack instead
of overwriting.

## Scope

Implementation lives entirely in the sibling `vite-plugin-gsx` repo: no gsx
syntax or codegen change, therefore no corpus case. Ships as a plugin version
bump.

## Design

### Library

Add `ansi_up@^6` to `dependencies`, and list it in `noExternal` in
`tsup.config.ts` so it is inlined into `dist/client.js`.

`ansi_up` over the alternatives: `ansi-to-html` has the most downloads but is
stuck at 0.7.2 (2021) and pulls in `entities`; `ansi_up` (6.0.6) and `anser`
(2.3.5) are both zero-dependency and maintained, and `ansi_up` is the
browser-first one with HTML escaping built in.

Bundling rather than leaving it external: the panel client is loaded in the
browser via the `virtual:gsx-devpanel` entry import. An external bare
specifier would depend on vite resolving and pre-bundling `ansi_up` out of the
plugin's nested `node_modules` in the *user's* project. Inlining removes that
variable.

### Render path

In `client.ts`, the `#gsx-log-box` `<pre>` body becomes
`ansiUp.ansi_to_html(logText)` instead of `escapeHtml(logText)`.

AnsiUp escapes its input itself (`escape_html` defaults to true), so this does
not widen the XSS surface. A test asserts a `<script>` tag present in the log
renders inert.

One `AnsiUp` instance, recreated per full re-render. The poll refetches the
whole tail each time and the panel re-renders from scratch, so there is no
cross-poll SGR state to carry — and a fresh instance guarantees no leaked
color state from a truncated escape.

### Truncated escapes

The tail is a byte slice and can begin mid-escape. AnsiUp treats an incomplete
leading sequence as text. Worst case is one garbled first line, which the
existing "earlier output truncated" banner already contextualizes. Accepted as
is.

### Pre-pass: carriage returns and OSC

(Behaviors below were established against a real `ansi_up@6.0.6`, not assumed.)

A pure helper in `client-logic.ts`, applied before `ansi_to_html`, unit-tested
alongside the other pure helpers there. Two jobs, both things AnsiUp verifiably
does not do:

1. **`\r` overwrite.** Collapse each line to the segment after its last `\r`.
   SGR state persists across a `\r` in a real terminal, so the SGR sequences
   found in the discarded prefix are re-prepended to the kept segment —
   otherwise a colored spinner line (`ESC[32m10%\r99% done`) loses its color.
2. **OSC strings.** `ESC ] … BEL` / `ESC ] … ESC \` leak through AnsiUp as
   literal text — a title-setting sequence renders as `]0;my title`. Stripped,
   *except* OSC 8 hyperlinks, which AnsiUp does handle (it emits `<a href>`,
   with a scheme allowlist of http/https and the URL HTML-escaped) and which
   are left for it.

Cursor movement (`ESC[1A`) and erase-line (`ESC[2K`) need no handling — AnsiUp
already consumes them.

### Colors

`use_classes = true`, so AnsiUp emits `class="ansi-*"` for the 16 basic
colors instead of inline `style="color:rgb(...)"`. The classes
(`ansi-{color}-fg/-bg` and `ansi-bright-{color}-fg/-bg`) are defined in the
panel's existing `<style>` block, tuned for the panel's dark background — the
default ANSI blue and black are unreadable there.

`use_classes` covers only those 16. 256-color and truecolor sequences still
emit inline rgb styles, and bold/dim/italic/underline always emit inline
styles. That is fine: an author who wrote an explicit rgb value meant that
value.

### Tests

- `client-logic` tests for the `\r` collapse helper.
- Client tests for SGR → HTML, HTML escaping of log content, 256-color /
  24-bit truecolor sequences, and a `javascript:` OSC 8 hyperlink not
  becoming an anchor.
