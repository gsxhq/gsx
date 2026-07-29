# `gsx init` Demo Selector Scope Design

**Date:** 2026-07-29

## Proven failure

The simple `gsx init` scaffold styles every `button` with unlayered CSS. When a
fresh project adds GSXUI, that rule outranks Tailwind's named cascade layers.
A destructive GSXUI Button therefore keeps its compiled `bg-destructive`,
`text-contrast`, and sizing classes but computes the starter counter button's
background, padding, type size, and radius.

GSXUI's construct is correct: theme tokens use `@theme inline`, foundation
rules use `@layer base`, component CSS uses `@layer components`, and copied
component classes compile into Tailwind's utilities layer.

## Decision

The GSX starter demo must not apply demo presentation through global
interactive or heading element selectors.

- logo-link presentation targets `.logos a`;
- the demo heading carries and uses `.app-title`;
- counter-control presentation targets its existing `#counter` hook;
- the light-mode overrides use the same scoped hooks.

The scaffold keeps intentional application-shell rules on `:root` and `body`.
It does not add Tailwind or GSXUI knowledge: GSX remains framework-agnostic.

No GSXUI-side rewrite, specificity escalation, `!important`, or cascade-layer
workaround is added.

## Regression contract

`gen` tests render the real embedded simple scaffold and assert that its CSS:

- contains the explicit `.logos a`, `.app-title`, and `#counter` hooks;
- contains no top-level `a`, `h1`, or `button` demo selector;
- retains the counter focus, hover, and light-mode states through `#counter`.

An end-to-end probe creates a new project from this checkout, initializes
GSXUI, adds Button, builds the real Vite bundle, and checks Chromium-computed
styles for a destructive large Button. The Button must compute its GSXUI
background, 36px height, 14px type, 10px inline padding, and 10px radius rather
than the starter counter values.

## Scope

This changes only the simple scaffold template and its tests. Existing
projects are not rewritten. The user's unrelated HTML-attribute refactor in
the working tree remains untouched.
