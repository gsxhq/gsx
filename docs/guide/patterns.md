# Patterns

Reusable recipes for things gsx doesn't build in — singletons, integrations with
other libraries, and idioms that fall out of gsx being *plain Go*. Each pattern is
self-contained: copy the code into your project and adapt it.

Unlike the [Syntax reference](./syntax), these pages are not language features. They
are conventions built on top of the language, kept here so you don't have to
rediscover them.

## Available patterns

- **[Render once](./patterns/render-once)** — emit a per-request singleton (a dialog
  container, a dev-mode asset preamble, a one-time inline `<style>`/`<script>`)
  exactly once even when its component is invoked from many call sites. A userland
  port of templ's `OnceHandle`.

## Planned

More patterns will land here as they stabilise — HTMX partial rendering and
[structpages](https://github.com/jackielii/structpages) routing integration are the
next candidates. If you have a pattern worth documenting, open an issue.
