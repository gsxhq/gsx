# HTTPS Dev Front Door Implementation Plan

> For agentic workers: use subagent-driven execution with focused tests and an independent adversarial review.

**Goal:** Preserve and support a trusted HTTPS Vite front door through the existing GSX dev loop.

**Architecture:** `VITE_DEV_URL` supplies the origin; Vite's native `server.https`
owns certificates. GSX preserves the scheme and uses its existing verified HTTP
clients. The plugin needs transport coverage and documentation, not a second TLS API.

**Tech Stack:** Go 1.27.1, Vite, TypeScript, native TLS and mkcert for local verification.

**Spec:** `docs/superpowers/specs/2026-11-02-dev-front-door-https-design.md`.

- [x] Extend `gen/devserver_test.go` for HTTPS, host/port precedence, held ports,
  IPv6 and rejected invalid origins; run the failing cases before the repair.
- [x] Repair `resolveViteDevEnv` in `gen/devserver.go`, preserving HTTP behavior.
- [x] Add real TLS coverage for management requests and verification failures;
  retain normal certificate validation and run the affected test clusters.
- [x] Update the init scaffold's displayed URL and dev-loop documentation;
  review `docs/ROADMAP.md` for the supported transport contract.
- [x] In the sibling Vite plugin, prove native HTTPS/WSS integration using a
  test-only certificate and document the native `server.https` setup.
- [x] Integrate EOS's optional local certificate configuration, propagate its
  HTTPS public origin, and verify the running main front door in the browser.
- [x] Obtain an independent adversarial review, run the authoritative final
  checks before any upstream merge, and commit the reviewed changes.
