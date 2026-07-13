# Static Fold Provenance Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Preserve author-trusted static attribute values when an element enters the shared-bag fold, while leaving non-folding leaf emission unchanged.

**Architecture:** In `composeBag`'s `StaticAttr` arm, wrap the value with `gsx.RawURL` only for `bagElementFold`; component conditional bags keep plain strings. Source-order last-wins selects the winning typed value before `Spread` applies URL handling.

**Tech Stack:** Go, gsx codegen, txtar corpus.

## Global Constraints

- Static fold provenance is per contribution; later dynamic values remain sanitized and later static values remain trusted.
- HTML attribute escaping always applies.
- JS/CSS contextual literals on URL keys remain rejected.
- Non-folding leaves keep direct literal tag writes with no bag/Spread path.
- Component-prop bags and runtime APIs remain unchanged.
- Generated goldens are regenerated, never hand-edited.

---

### Task 1: Preserve static values through element folds

**Files:**
- Modify: `internal/codegen/emit.go`.
- Replace: `internal/corpus/testdata/cases/condmerge/nonforwarding_merge_url_static.txtar`.
- Create: `internal/corpus/testdata/cases/condmerge/nonforwarding_merge_url_override.txtar`.
- Extend negative-control corpus and fold differential tests.

- [ ] RED: change the static URL expectation from `about:invalid#gsx` to verbatim and confirm failure.
- [ ] Implement `bagElementFold` static entries as `{Key: ..., Value: _gsxrt.RawURL("...")}`; keep `bagComponentCond` as plain strings.
- [ ] Pin override ordering: trusted static then dynamic dangerous string => sanitized winner; dynamic then trusted static => verbatim winner; two statics => later verbatim; conditional static taken/untaken.
- [ ] Pin nav/image/srcset/prefix/custom URL sinks and HTML-special characters.
- [ ] Pin non-URL folded static output unchanged and component-prop value type unchanged.
- [ ] Pin non-folding leaf generated shapes for `href="javascript:void(0)"` and ordinary statics: direct `_gsxgw.S("<...>")`, no `Attrs`, `RawURL`, or `Spread`.
- [ ] Remove the ROADMAP debt that warns about fold rewriting static URLs; update docs to state provenance preservation.
- [ ] Full corpus update and inspection; run codegen/corpus/root tests, `gopls check`, and diff check.
- [ ] Commit `fix(codegen): preserve static attribute provenance in folds`.

### Task 2: Adversarial verification and publish

- [ ] Run `make ci` outside sandbox and `make lint`.
- [ ] Independent throwaway probes for all override orders, URL sink kinds, HTML escaping, component bags, and direct non-fold leaf generation.
- [ ] Fix findings, push PR #101 branch, and refresh checks.

## Self-review

- No runtime API or URL classifier duplication.
- Direct leaf fast path is explicitly pinned.
- Trust follows the winning contribution, not merely the attribute name.
