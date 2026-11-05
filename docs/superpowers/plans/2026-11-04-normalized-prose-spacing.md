# Whitespace normalization plan

Spec: `../specs/2026-11-04-normalized-prose-spacing.md`.

1. Replace JSX edge dropping with the region scan in `internal/wsnorm`; parse
   body whitespace uniformly; trim brace padding through comments/Go blocks.
2. Formatter: bond Text/sibling boundaries; keep list-edge guards.
3. Retire `child-element-spacing` (diagnostic, LSP fixes, config entry → error).
4. Corpus: exact render goldens; cases for siblings of every kind, comments,
   root/slot/control-flow bodies, Go-expression markup, entities and NBSP.
5. Docs: guide whitespace section, CLI fmt note, config, CHANGELOG, ROADMAP.
6. Gate: focused tests, adversarial probes, `make ci` on Go 1.27.1.
