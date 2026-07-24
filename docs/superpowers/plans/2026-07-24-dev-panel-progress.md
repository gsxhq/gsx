# Dev Panel Build Progress Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Live rebuild feedback: phase transitions posted as they happen, the panel auto-showing after 3s of a running cycle with ticking elapsed + last-cycle duration, and an expanded log-tail box while building.

**Architecture:** gsx posts status at each in-cycle phase transition (payload gains `phaseSince`, `lastCycle.durationMs`); the panel runs a local auto-show timer and, while visible + non-idle, polls `/__gsx/log` into an expanded layout. No new channels; everything rides the existing status bus and the #4 log endpoint.

**Spec:** `docs/superpowers/specs/2026-07-24-dev-panel-progress-design.md` — authoritative, read first.

## Global Constraints

- gsx work: worktree branch `dev-panel-progress` from main (after #158 merges — it touches neighboring dev.go lines; rebase if needed). Plugin work: branch `dev-panel-progress` from main AFTER PR #4 merges (the log box consumes its endpoint + tests). Verify `git branch --show-current` before commits.
- Wire additions (cross-repo contract): status gains top-level `"phaseSince":"<RFC3339>"` (always present); `lastCycle` gains `"durationMs":<int>`. Existing fields unchanged.
- `devPanel` option grows `autoShow?: number | false` (ms, default 3000) — validated: non-negative finite number or false; invalid → default.
- Log polling: ~1s interval, strictly gated on (panel visible) AND (phase ≠ idle); probe `/__gsx/log` once per page load, 404/failure → no log box ever (no retries, no errors). Honor `x-gsx-log-start` ("earlier output truncated" line when > 0).
- Auto-shown panel hides on `idle`; manually-opened (Cmd-D) never auto-hides; `devPanel: false` disables everything.
- Gates: gsx — targeted tests, full `go test ./gen -count=1` (now safe locally post-#157, still check for stray dev loops), `gofmt -l gen/` empty, `go vet ./gen`, `make lint`. Plugin — `npx vitest run`, `npx tsc --noEmit`, `npm run build`. ALL commands FOREGROUND.
- Read exit codes before acting on any gate; never chain a merge/push on a compound command's tail.

---

### Task 1: gsx — phase-transition posts + phaseSince/durationMs

**Files:** Modify `gen/devstatus.go` (fields), `gen/dev.go` (cycle() + restart-server case), tests in `gen/devstatus_test.go` + `gen/dev_test.go`.

**Interfaces:** `devStatus` gains `PhaseSince time.Time \`json:"phaseSince"\``; `cycleStat` gains `DurationMs int64 \`json:"durationMs"\``. A `setPhase(phase string)` helper in runDev's closure scope updates `status.Phase` + `status.PhaseSince` + calls `postStatus()` — replacing the bare `status.Phase = ...` assignments at each transition in `cycle()` and the `restart-server` handler. Cycle end computes `DurationMs` from the cycle's start.

- [ ] Unit RED→GREEN: `TestStatusEventShape` extended for both fields (RFC3339 phaseSince; durationMs int).
- [ ] Integration RED→GREEN: recorder-pattern test with a deliberately slow `[dev].build` (e.g. `sh -c 'sleep 2; go build ...'` via gsx.toml in the fixture): after a source write, the recorder must observe a `"phase":"building"` status BEFORE the cycle-end `"idle"` one (fails today — mid-cycle posts don't exist), with monotonic phaseSince and a final durationMs ≥ 2000.
- [ ] Full gates; commit `feat(gen): post status at phase transitions with phaseSince and cycle duration`.

### Task 2: plugin — autoShow + phase view

**Files:** `src/options.ts` (autoShow), `src/client.ts` + `src/client-logic.ts` (+tests).

**Interfaces:** client-logic gains pure helpers: `autoShowDelay(opt): number | null` (validated); `phaseLine(status, nowMs): string` producing `building… started 42s ago · last cycle 2m10s` (durations humanized m/s; omit segments when data absent); panel state machine tracks `openedBy: "user" | "auto" | null`.

- [ ] RED→GREEN: option validation table; phaseLine rendering table (ticking values, missing lastCycle, missing phaseSince → no elapsed segment); state-machine tests — non-idle status starts timer, idle before expiry cancels, expiry while non-idle → auto-open, idle → auto-close only when openedBy=auto, Cmd-D always wins and upgrades openedBy to user.
- [ ] Wire into client.ts: 1s local tick re-renders the phase line while visible and non-idle (no re-render when hidden).
- [ ] Full plugin gates; commit `feat: panel auto-shows during long cycles; live phase line`.

### Task 3: plugin — log box

**Files:** `src/client.ts`, `src/client-logic.ts` (+tests). Consumes `/__gsx/log` exactly as merged in #4 (default tail, `x-gsx-log-start`).

- [ ] RED→GREEN (pure logic): `logBoxState(probeResult, phase, visible)` gating table (no probe success → never; idle or hidden → no polling); truncation banner from the offset header; expanded-layout class toggling.
- [ ] Client wiring: one probe per page (on first non-idle while visible), 1s polling loop with strict gate, scroll-pinned-to-bottom unless the user scrolled up (a `userScrolled` flag reset on re-open).
- [ ] Full plugin gates; commit `feat: expanded log tail box while building`.

### Task 4: docs + smoke + release sequencing

- [ ] `docs/guide/dev-loop.md`: extend the Dev panel section by 2-3 concise sentences (auto-show + threshold + option, log box needs `[dev].log`). Plugin README: `autoShow` in the devPanel option row.
- [ ] Live smoke (coordinator runs this, not a subagent): fixture with `sleep`-slowed build → save → panel auto-appears ~3s with ticking line and log box; quick save → nothing appears; `autoShow: false` → Cmd-D only.
- [ ] Sequencing: plugin tasks merge → next plugin release; gsx task merges after (wire additions are backward-compatible — an old panel ignores unknown fields, so strict ordering is soft here; still release plugin first so the panel exists when gsx starts posting mid-cycle statuses more often). Scaffold pin bump npm-verified if a release happens.

## Final gate

Per-task reviews; final whole-branch adversarial review across both repos; full suites + `make lint`; exit codes read before any merge action.
