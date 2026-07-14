# Concise Website Alignment Plan

> Execute this after the canonical `docs/guide` rewrite is complete. Website
> work belongs in an isolated worktree of `../gsxhq.github.io`; synced
> `guide/**` remains generated from this repository and must not be edited by
> hand.

**Goal:** Make the website-owned home page, navigation, metadata, and
playground copy match the concise, current guide.

**Source of truth:** `docs/guide/**` in the gsx repository owns guide content.
The website owns `index.md`, `.vitepress/config.mts`, package dependencies, and
the playground UI.

## Task 1: Create and audit the website worktree

1. Require a clean tracked state in `../gsxhq.github.io`.
2. Confirm local `main` matches `origin/main`, then create
   `codex/concise-docs-website` from that revision in an isolated worktree.
3. Confirm that `guide/**` is ignored/synced and inventory website-owned claims,
   sidebar entries, metadata, Mermaid use, and playground service errors.

## Task 2: Align the home page, navigation, and metadata

**Files:** `index.md`, `.vitepress/config.mts`

- Replace the generated-props-only claim with the current generated-or-user-owned
  props model.
- Replace “capitalization decides” with the actual tag-name-and-package-declaration
  rule; link to the syntax reference instead of expanding the rule on the home page.
- Link the alpha callout to `/guide/status`.
- Rename the stale “Interop and Gaps” sidebar group and add Status alongside
  Comparisons and Interop.
- Use one short positioning line for the site description and Open Graph title.
- Keep the hero, routes, and existing visual design unchanged.

Commit:

```text
docs: align website copy with concise guide
```

## Task 3: Remove unused Mermaid support

**Files:** `.vitepress/config.mts`, `package.json`, `package-lock.json`

1. Prove there are no Mermaid fences in website-owned Markdown or the synced
   canonical guide.
2. Export `defineConfig(...)` directly and remove the Mermaid wrapper/import.
3. Remove `mermaid` and `vitepress-plugin-mermaid` from development dependencies
   with the package manager so the lockfile stays coherent.
4. Run the website build.

Commit:

```text
chore: remove unused mermaid support
```

## Task 4: Clarify playground service failures

**File:** `.vitepress/theme/GsxPlayground.vue`

- In a development build, keep a concrete local-service hint.
- In a production build, say that the hosted render service is unavailable and
  suggest retrying; do not ask whether a local server is running.
- Leave compiler diagnostics and successful rendering behavior unchanged.

Commit:

```text
fix: clarify playground service errors
```

## Task 5: Verify the published surface

1. Sync and build with both canonical sources explicit:

   ```sh
   GSX_DOCS_SRC=/Users/jackieli/personal/gsxhq/gsx/.worktrees/concise-docs \
   GSX_GRAMMAR_SRC=/Users/jackieli/personal/gsxhq/vscode-gsx/syntaxes/gsx.tmLanguage.json \
   npm run sync

   GSX_DOCS_SRC=/Users/jackieli/personal/gsxhq/gsx/.worktrees/concise-docs \
   GSX_GRAMMAR_SRC=/Users/jackieli/personal/gsxhq/vscode-gsx/syntaxes/gsx.tmLanguage.json \
   npm run build
   ```
2. Check the home page, sidebar, Status, and Playground at desktop and 390px.
3. Click the Status navigation and verify the home-page guide links.
4. Start development with an unreachable `VITE_GSX_PLAYGROUND_API`, press Run,
   and confirm the local-service hint. Then run the production preview without
   a render service and confirm its hosted-service retry message. Check both
   states for page overflow.
5. Run editorial/link scans and `git diff --check`.
6. Request an independent review of the website diff and fix every confirmed
   Critical or Important finding.
7. Confirm both canonical and website worktrees are clean.
