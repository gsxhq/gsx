# `gsx dev` Go-port selection Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `gsx dev` picks a free Go port when `GO_PORT` is unset — so two projects run side by side — and never counts its own children's listeners as port conflicts.

**Architecture:** One rule for both ports: unset floats from the default (`7777` Go, `5173` Vite), set is strict. A new `resolveGoPort` becomes the single owner of `GO_PORT` semantics and injects the resolved port into the environment handed to the spawned server; `resolveUpstream` stops reading `GO_PORT` and takes it as a parameter. Both resolvers gain a `held` parameter — the port our own child currently occupies — which is exempt from the busy check and reused by the auto-picker, so re-resolution after an `.env` edit neither self-conflicts nor drifts.

**Tech Stack:** Go 1.26.1, stdlib only in `gen/` for this work (`net`, `strconv`). Tests are standard `go test` (unit + `-short`-skipped integration tests that build the real `gsx` binary).

**Spec:** `docs/superpowers/specs/2026-07-25-dev-go-port-selection-design.md`

## Global Constraints

- Pin Go to `GO_VERSION` in `.github/workflows/ci.yml` (currently **1.26.1**); a different minor re-introduces `gofmt` drift.
- The root `gsx` runtime package stays stdlib-only. All work here is in `gen/` (tooling), which may use `golang.org/x/tools` — but this change needs nothing beyond stdlib.
- `make ci` is the authoritative gate (uncached, `-count=1`). `make check` is the inner loop. `make lint` must pass.
- Do **not** hand-edit `.x.go` or `.golden` files.
- Feature work happens in a git worktree on a feature branch — never commit to `main`.
- Existing behavior that must not regress: `[dev].upstream`, when set, is observational only — `gsx dev` neither picks nor injects a Go port in that case.
- Error messages are user-facing copy; use them verbatim as written in the tasks.

---

## Task 0: Worktree setup

**Files:** none (workspace only)

- [ ] **Step 1: Create the worktree and branch**

```bash
cd /Users/jackieli/personal/gsxhq/gsx
git worktree add ../gsx-dev-go-port -b feat/dev-go-port
cd ../gsx-dev-go-port
git status --short --branch   # expect: ## feat/dev-go-port, clean
```

Every later task runs from `../gsx-dev-go-port`. Confirm `git branch --show-current` prints `feat/dev-go-port` before the first commit of every task.

---

## Task 1: `portFree` helper and a labeled `nextAvailablePort`

Groundwork both resolvers need: a busy-check that treats one specific port as free, and a port scanner whose error messages don't hardcode "Vite".

**Files:**
- Modify: `gen/devserver.go:296-308` (`nextAvailablePort`), add `portFree` next to `portAvailable` (`gen/devserver.go:310`)
- Modify: `gen/devserver.go:281` (the one existing `nextAvailablePort` call)
- Test: `gen/devserver_test.go` (new tests; update the call at `gen/devserver_test.go:342`)

**Interfaces:**
- Consumes: existing `portAvailable(port string) bool`.
- Produces:
  - `func portFree(port, held string) bool`
  - `func nextAvailablePort(start, label string) (string, error)`

- [ ] **Step 1: Write the failing test**

Add to `gen/devserver_test.go`:

```go
func TestPortFreeTreatsHeldPortAsFree(t *testing.T) {
	port := freePort(t)
	l, err := net.Listen("tcp", "127.0.0.1:"+port)
	if err != nil {
		t.Skipf("could not hold port %s for test: %v", port, err)
	}
	defer l.Close()

	// Held by a stranger: a conflict.
	if portFree(port, "") {
		t.Errorf("portFree(%s, \"\") = true, want false (port is bound by another process)", port)
	}
	// Held by our own child: not a conflict.
	if !portFree(port, port) {
		t.Errorf("portFree(%s, %s) = false, want true (our own listener must not read as a conflict)", port, port)
	}
	// A different held port does not excuse a busy one.
	if portFree(port, "1") {
		t.Errorf("portFree(%s, \"1\") = true, want false", port)
	}
}

func TestNextAvailablePortLabelsItsErrors(t *testing.T) {
	if _, err := nextAvailablePort("65536", "Go server"); err == nil {
		t.Fatal("nextAvailablePort(65536) = nil error, want exhaustion error")
	} else if !strings.Contains(err.Error(), "Go server") {
		t.Errorf("error = %q, want it to name the Go server port", err)
	}
	if _, err := nextAvailablePort("not-a-port", "Vite dev"); err == nil {
		t.Fatal("nextAvailablePort(\"not-a-port\") = nil error, want an invalid-start error")
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./gen -run 'TestPortFree|TestNextAvailablePortLabels' -count=1`
Expected: FAIL — compile error, `undefined: portFree` and too many arguments to `nextAvailablePort`.

- [ ] **Step 3: Implement**

In `gen/devserver.go`, replace `nextAvailablePort`:

```go
// nextAvailablePort returns the first free port at or above start. label names
// the port's role ("Vite dev", "Go server") and appears in the error messages.
func nextAvailablePort(start, label string) (string, error) {
	port, err := strconv.Atoi(start)
	if err != nil {
		return "", fmt.Errorf("invalid %s start port %q", label, start)
	}
	for ; port <= 65535; port++ {
		candidate := strconv.Itoa(port)
		if portAvailable(candidate) {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("choose %s port: no free port at or above %s", label, start)
}
```

Add below `portAvailable`:

```go
// portFree is portAvailable with one exemption: held — the port a child gsx
// dev itself spawned currently occupies — counts as free. Re-resolution (an
// .env edit while the loop runs) probes ports our own vite/server are sitting
// on, and without this a running front door reads as somebody else's conflict.
// held is "" when nothing of ours holds a port yet.
func portFree(port, held string) bool {
	if held != "" && port == held {
		return true
	}
	return portAvailable(port)
}
```

Update the existing call in `resolveViteDevEnv` (`gen/devserver.go:281`):

```go
		port, err = nextAvailablePort("5173", "Vite dev")
```

Update **both** existing test calls — `gen/devserver_test.go:342` and `gen/devserver_test.go:496`:

```go
	wantPort, err := nextAvailablePort("5173", "Vite dev")
```

Confirm none are left: `grep -rn 'nextAvailablePort("' gen/` must show only two-argument calls.

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./gen -run 'TestPortFree|TestNextAvailablePortLabels|TestResolveViteDevEnv' -count=1`
Expected: PASS (all `TestResolveViteDevEnv*` still green).

- [ ] **Step 5: Commit**

```bash
git add gen/devserver.go gen/devserver_test.go
git commit -m "gen: portFree exemption helper and labeled nextAvailablePort"
```

---

## Task 2: Held-port exemption for the Vite resolver

Fixes a bug that bites today: with an explicit `VITE_DEV_URL`/`VITE_PORT`, any `.env` edit during a run re-resolves, sees `gsx dev`'s **own** front door on that port, hard-errors `port … is already in use`, and skips the server restart — the browser gets an overlay instead of a reload. Auto-picked ports drift upward for the same reason.

**Files:**
- Modify: `gen/devserver.go:250-294` (`resolveViteDevEnv`)
- Modify: `gen/dev.go:66` (startup call) and `gen/dev.go:464` (`.env`-fire call), plus a new `heldVite` variable
- Test: `gen/devserver_test.go` (unit), `gen/dev_test.go` (integration)

**Interfaces:**
- Consumes: `portFree(port, held string) bool`, `nextAvailablePort(start, label string)` from Task 1.
- Produces: `func resolveViteDevEnv(env []string, host, held string) ([]string, string, string, error)` — returns `(env, viteURL, warning, err)` exactly as before.

- [ ] **Step 1: Write the failing unit tests**

Add to `gen/devserver_test.go`:

```go
func TestResolveViteDevEnvAcceptsOwnHeldPort(t *testing.T) {
	port := freePort(t)
	l, err := net.Listen("tcp", "127.0.0.1:"+port)
	if err != nil {
		t.Skipf("could not hold port %s for test: %v", port, err)
	}
	defer l.Close()

	// Our own front door is on this port: a re-resolution must accept the pin.
	_, viteURL, _, err := resolveViteDevEnv([]string{"VITE_PORT=" + port, "PATH=/bin"}, "", port)
	if err != nil {
		t.Fatalf("explicit VITE_PORT held by our own front door was rejected: %v", err)
	}
	if want := "http://localhost:" + port; viteURL != want {
		t.Fatalf("viteURL = %q, want %s", viteURL, want)
	}

	// Same for a URL-derived pin.
	if _, _, _, err := resolveViteDevEnv([]string{"VITE_DEV_URL=http://mstudio:" + port, "PATH=/bin"}, "", port); err != nil {
		t.Fatalf("VITE_DEV_URL port held by our own front door was rejected: %v", err)
	}

	// A stranger on the same port must still fail: held is an exemption for
	// OUR child only, not a blanket disable of the busy check.
	if _, _, _, err := resolveViteDevEnv([]string{"VITE_PORT=" + port, "PATH=/bin"}, "", ""); err == nil {
		t.Fatal("busy VITE_PORT with no held port = nil error, want 'already in use'")
	}
}

func TestResolveViteDevEnvAutoPickReusesHeldPort(t *testing.T) {
	// Port-less config with a port already picked for this session: re-resolve
	// to the SAME port. Probing would find our own vite there and drift to the
	// next one, leaving viteURL pointing at a port nobody listens on.
	held := freePort(t)
	_, viteURL, warning, err := resolveViteDevEnv([]string{"PATH=/bin"}, "", held)
	if err != nil {
		t.Fatal(err)
	}
	if warning != "" {
		t.Errorf("warning = %q, want none", warning)
	}
	if want := "http://localhost:" + held; viteURL != want {
		t.Fatalf("viteURL = %q, want %s (auto-pick must reuse the held port)", viteURL, want)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./gen -run TestResolveViteDevEnv -count=1`
Expected: FAIL — compile error, too many arguments to `resolveViteDevEnv`.

- [ ] **Step 3: Implement the resolver change**

In `gen/devserver.go`, change the signature and the three port branches. Extend the existing doc comment's precedence paragraph with the held rule:

```go
// Precedence: VITE_PORT > VITE_DEV_URL's port > auto-pick from 5173. An
// explicit port — either form — fails loudly (hard error) if it's already
// bound; only a truly port-less configuration (no VITE_PORT, and no port on
// VITE_DEV_URL) auto-picks. held is the port gsx dev's own front door is
// currently on (empty at startup): it is exempt from the busy check and is
// what the auto-picker returns, so re-resolving after an .env edit neither
// conflicts with our own child nor drifts to a port nothing listens on.
func resolveViteDevEnv(env []string, host, held string) ([]string, string, string, error) {
```

```go
	switch {
	case hasVitePort:
		if _, convErr := strconv.Atoi(port); convErr != nil {
			return nil, "", "", fmt.Errorf("invalid VITE_PORT %q", port)
		}
		if urlPort != "" && urlPort != port {
			warning = fmt.Sprintf("VITE_PORT=%s overrides VITE_DEV_URL's :%s", port, urlPort)
		}
		if !portFree(port, held) {
			return nil, "", "", fmt.Errorf("VITE_PORT %s is already in use", port)
		}
	case urlPort != "":
		if !portFree(urlPort, held) {
			return nil, "", "", fmt.Errorf("VITE_DEV_URL port %s is already in use", urlPort)
		}
		port = urlPort
	default:
		if held != "" {
			port = held
			break
		}
		port, err = nextAvailablePort("5173", "Vite dev")
		if err != nil {
			return nil, "", "", err
		}
	}
```

- [ ] **Step 4: Update the existing test call sites**

Every existing `resolveViteDevEnv(...)` call in `gen/devserver_test.go` gains a third argument `""` (no held port): lines 347, 367, 370, 388, 401, 417, 437, 454, 471, 501, 521. For example:

```go
	env, viteURL, warning, err := resolveViteDevEnv([]string{"PATH=/bin"}, "", "")
```

Confirm none are left: `go vet ./gen` must be clean.

- [ ] **Step 5: Wire the call sites in `gen/dev.go`**

At `gen/dev.go:65-73`, pass no held port at startup and record what was chosen:

```go
	env := mergeDotEnv(os.Environ(), loadDotEnv(workDir))
	env, viteURL, warning, err := resolveViteDevEnv(env, dc.host, "")
	if err != nil {
		fmt.Fprintf(stderr, "gsx dev: %v\n", err)
		return 1
	}
	// heldVite is the port our own front door occupies; every later
	// re-resolution passes it so the running child is not mistaken for a
	// foreign listener (see portFree).
	heldVite := envPort(env, "VITE_PORT", "")
	if warning != "" {
		fmt.Fprintf(stderr, "gsx dev: %s\n", warning)
	}
```

In the `.env`-fire block at `gen/dev.go:464`, pass `heldVite` and update it after the assignment at `gen/dev.go:484`:

```go
				resolvedEnv, newViteURL, envWarning, envErr := resolveViteDevEnv(newEnv, dc.host, heldVite)
```

```go
				env, viteURL = resolvedEnv, newViteURL
				heldVite = envPort(env, "VITE_PORT", "")
```

- [ ] **Step 6: Run the unit tests**

Run: `go test ./gen -run 'TestResolveViteDevEnv|TestPortFree' -count=1`
Expected: PASS.

- [ ] **Step 7: Write the failing integration test**

This is one-learning's exact shape: an explicit front-door port that a **real** front-door process binds. The existing `TestDevEnvErrorPostsOverlay` misses the bug precisely because its front door (`--web "sleep 600"`) binds nothing.

Add the fake front door source next to `devTestMainGo` in `gen/dev_test.go`:

```go
// devTestFrontDoorGo stands in for vite in tests that need the front-door port
// to be genuinely BOUND (a `sleep` front door holds nothing, which is why the
// self-conflict bug hid for so long). It binds VITE_PORT — injected into the
// front door's environment by resolveViteDevEnv — appends every request path
// to $FAKE_LOG, and echoes GSX_DEV_TOKEN in x-gsx so gsx dev recognizes it as
// its own child.
const devTestFrontDoorGo = `package main

import (
	"fmt"
	"net/http"
	"os"
)

func main() {
	logPath := os.Getenv("FAKE_LOG")
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if f, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644); err == nil {
			fmt.Fprintln(f, r.URL.Path)
			f.Close()
		}
		w.Header().Set("x-gsx", os.Getenv("GSX_DEV_TOKEN"))
		w.WriteHeader(http.StatusNoContent)
	})
	_ = http.ListenAndServe("localhost:"+os.Getenv("VITE_PORT"), nil)
}
`

// buildDevTestFrontDoor compiles devTestFrontDoorGo into its own stdlib-only
// module and returns the binary path.
func buildDevTestFrontDoor(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	writeFile(t, dir, "go.mod", "module fakefrontdoor\n\ngo 1.26.1\n")
	writeFile(t, dir, "main.go", devTestFrontDoorGo)
	bin := filepath.Join(dir, "frontdoor")
	cmd := exec.Command("go", "build", "-o", bin, ".")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build fake front door: %v\n%s", err, out)
	}
	return bin
}
```

And the test itself:

```go
// TestDevEnvEditKeepsBoundFrontDoorPort pins the held-port exemption end to
// end: with an explicit VITE_DEV_URL port that the managed front door actually
// binds, an .env edit must restart-and-reload. Before portFree, re-resolution
// probed the port, found OUR OWN front door on it, and failed the whole
// .env-fire path with "already in use" — one-learning (VITE_DEV_URL pinned to
// http://mstudio:4000) hit this on every .env edit.
func TestDevEnvEditKeepsBoundFrontDoorPort(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test: requires building gsx and a live Go server")
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not available")
	}

	gsxRoot := repoRoot(t)
	bin := filepath.Join(t.TempDir(), "gsx")
	buildCmd := exec.Command("go", "build", "-o", bin, "./cmd/gsx")
	buildCmd.Dir = gsxRoot
	if out, err := buildCmd.CombinedOutput(); err != nil {
		t.Fatalf("build gsx: %v\n%s", err, out)
	}
	frontDoor := buildDevTestFrontDoor(t)

	proj := t.TempDir()
	gomod := fmt.Sprintf(
		"module devdemo\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => %s\n",
		gsxRoot,
	)
	writeFile(t, proj, "go.mod", gomod)
	writeFile(t, proj, "main.go", devTestMainGo)
	writeFile(t, proj, "app.gsx", "package main\n\ncomponent Dummy() {\n\t<span>ok</span>\n}\n")

	goPort := freePort(t)
	vitePort := freePort(t)
	goodEnv := "GO_PORT=" + goPort + "\nVITE_DEV_URL=http://localhost:" + vitePort + "\n"
	writeFile(t, proj, ".env", goodEnv)

	fakeLog := filepath.Join(t.TempDir(), "frontdoor.log")
	cmd := exec.Command(bin, "dev", "--web", frontDoor)
	cmd.Dir = proj
	cmd.Env = devTestEnv("BROWSER=none", "GOFLAGS=-mod=mod", "FAKE_LOG="+fakeLog)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	var stdout lockedBuffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stdout
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer stopDevGracefully(cmd)

	if !waitHealthy(context.Background(), "http://localhost:"+goPort+"/healthz", 120*time.Second) {
		t.Fatalf("server never came up; output:\n%s", stdout.String())
	}
	// Wait for the front door to have been hit at least once, so we know it is
	// bound to vitePort before the .env edit.
	waitFor(t, 30*time.Second, func() bool {
		b, _ := os.ReadFile(fakeLog)
		return len(b) > 0
	})
	if err := os.Truncate(fakeLog, 0); err != nil {
		t.Fatal(err)
	}

	// The edit under test: any change at all, with both ports unchanged.
	writeFile(t, proj, ".env", goodEnv+"FOO=bar\n")

	waitFor(t, 30*time.Second, func() bool {
		b, _ := os.ReadFile(fakeLog)
		return strings.Contains(string(b), "/__reload")
	})
	if strings.Contains(stdout.String(), "already in use") {
		t.Fatalf("gsx dev reported its own front door's port as in use after an .env edit; output:\n%s", stdout.String())
	}
	if !waitHealthy(context.Background(), "http://localhost:"+goPort+"/healthz", 30*time.Second) {
		t.Fatalf("server not healthy after the .env edit; output:\n%s", stdout.String())
	}
}
```

`waitFor` (`gen/watch_integration_test.go:185`) fails the test when the condition never holds, so a missing `/__reload` is a failure, not a hang.

- [ ] **Step 8: Verify the integration test fails on the old code and passes on the new**

Run: `git stash push gen/devserver.go gen/dev.go && go test ./gen -run TestDevEnvEditKeepsBoundFrontDoorPort -count=1 -timeout 600s; git stash pop`
Expected: FAIL before the fix (no `/__reload`, or "already in use" in the output). Then:

Run: `go test ./gen -run TestDevEnvEditKeepsBoundFrontDoorPort -count=1 -timeout 600s`
Expected: PASS.

If the stashed run does not fail, stop and report — the test is not reproducing the bug and must be fixed before continuing.

- [ ] **Step 9: Run the whole dev suite for regressions**

Run: `go test ./gen -run TestDev -count=1 -timeout 900s`
Expected: PASS, including `TestDevEnvErrorPostsOverlay` and `TestDevEnvPrecedence`.

- [ ] **Step 10: Commit**

```bash
git add gen/devserver.go gen/devserver_test.go gen/dev.go gen/dev_test.go
git commit -m "gen: never treat gsx dev's own front door as a port conflict"
```

---

## Task 3: `resolveGoPort` — float the Go port when `GO_PORT` is unset

**Files:**
- Modify: `gen/devserver.go:181-235` (`resolveUpstream` signature + default branch), add `resolveGoPort` above it
- Modify: `gen/dev.go:74-82` (startup) and `gen/dev.go:459-512` (`.env` fire)
- Test: `gen/devserver_test.go` (unit, plus the existing `TestResolveUpstream` table), `gen/dev_test.go` (integration)

**Interfaces:**
- Consumes: `portFree`, `nextAvailablePort` (Task 1); `setEnvValue`, `envLookup` (existing).
- Produces:
  - `func resolveGoPort(env []string, upstreamSet bool, held string) ([]string, string, error)` — returns `(env with GO_PORT injected, port, err)`; port is `""` and env untouched when `upstreamSet`.
  - `func resolveUpstream(upstream, health string, env []string, goPort string) (origin, healthURL, port string, err error)`

- [ ] **Step 1: Write the failing unit tests**

Add to `gen/devserver_test.go`:

```go
func TestResolveGoPort(t *testing.T) {
	busy := freePort(t)
	l, err := net.Listen("tcp", "127.0.0.1:"+busy)
	if err != nil {
		t.Skipf("could not hold port %s for test: %v", busy, err)
	}
	defer l.Close()
	free := freePort(t)

	cases := []struct {
		name          string
		env           []string
		upstreamSet   bool
		held          string
		wantPort      string
		wantEnvPort   string // GO_PORT expected in the returned env; "" = absent
		wantErrSubstr string
	}{
		{
			// [dev].upstream means the user places the backend: no pick, and no
			// GO_PORT injected into an app that may not even read it.
			name:        "upstream set is hands off",
			env:         []string{"GO_PORT=" + busy},
			upstreamSet: true,
			wantPort:    "",
			wantEnvPort: busy, // untouched, not injected
		},
		{
			name:        "explicit free port accepted and kept",
			env:         []string{"GO_PORT=" + free},
			wantPort:    free,
			wantEnvPort: free,
		},
		{
			name:          "explicit busy port errors",
			env:           []string{"GO_PORT=" + busy},
			wantErrSubstr: "already in use",
		},
		{
			name:        "explicit port held by our own server accepted",
			env:         []string{"GO_PORT=" + busy},
			held:        busy,
			wantPort:    busy,
			wantEnvPort: busy,
		},
		{
			name:          "set but empty errors",
			env:           []string{"GO_PORT="},
			wantErrSubstr: "GO_PORT",
		},
		{
			name:          "non-numeric errors",
			env:           []string{"GO_PORT=web"},
			wantErrSubstr: "invalid GO_PORT",
		},
		{
			name:        "absent reuses the held port",
			env:         []string{"PATH=/bin"},
			held:        busy,
			wantPort:    busy,
			wantEnvPort: busy,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env, port, err := resolveGoPort(tc.env, tc.upstreamSet, tc.held)
			if tc.wantErrSubstr != "" {
				if err == nil {
					t.Fatalf("resolveGoPort(%v) = (%v, %q, nil); want error containing %q", tc.env, env, port, tc.wantErrSubstr)
				}
				if !strings.Contains(err.Error(), tc.wantErrSubstr) {
					t.Errorf("error = %q, want substring %q", err, tc.wantErrSubstr)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveGoPort(%v) error = %v", tc.env, err)
			}
			if port != tc.wantPort {
				t.Errorf("port = %q, want %q", port, tc.wantPort)
			}
			if got := envPort(env, "GO_PORT", ""); got != tc.wantEnvPort {
				t.Errorf("GO_PORT in env = %q, want %q", got, tc.wantEnvPort)
			}
		})
	}
}

func TestResolveGoPortSkipsBoundDefaultPort(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:7777")
	if err != nil {
		t.Skipf("default Go port unavailable before test: %v", err)
	}
	defer l.Close()
	want, err := nextAvailablePort("7777", "Go server")
	if err != nil {
		t.Fatal(err)
	}

	env, port, err := resolveGoPort([]string{"PATH=/bin"}, false, "")
	if err != nil {
		t.Fatal(err)
	}
	if port != want {
		t.Fatalf("port = %q, want %s (7777 is bound)", port, want)
	}
	if got := envPort(env, "GO_PORT", ""); got != want {
		t.Fatalf("GO_PORT injected into env = %q, want %s — the spawned server binds this value", got, want)
	}
}

func TestResolveUpstreamRequiresGoPortForDefaultOrigin(t *testing.T) {
	// The default origin is built from the resolved Go port; an empty one would
	// produce "http://localhost:", which url.Parse accepts and Go's http client
	// then dials as port 80 — the undiagnosable "server down" this guards.
	if _, _, _, err := resolveUpstream("", "", nil, ""); err == nil {
		t.Fatal("resolveUpstream with no upstream and no Go port = nil error, want a diagnostic")
	}
}
```

Update `TestResolveUpstream` (`gen/devserver_test.go:180`): add a `goPort` field to the case struct, pass it through the call, and move the `GO_PORT`-derived rows out (they now belong to `resolveGoPort`):

```go
		{
			name:          "absent upstream uses the resolved Go port",
			upstream:      "",
			goPort:        "7777",
			wantOrigin:    "http://localhost:7777",
			wantHealthURL: "http://localhost:7777/healthz",
			wantPort:      "7777",
		},
		{
			name:          "absent upstream honors a pinned Go port",
			upstream:      "",
			goPort:        "8081",
			wantOrigin:    "http://localhost:8081",
			wantHealthURL: "http://localhost:8081/healthz",
			wantPort:      "8081",
		},
```

Delete the two rows named `"absent upstream no GO_PORT defaults to 7777"` / `"absent upstream honors GO_PORT"` they replace, and the row named `"default upstream with GO_PORT present but empty errors"` (that case is now `TestResolveGoPort`'s "set but empty errors"). The call becomes:

```go
			origin, healthURL, port, err := resolveUpstream(tc.upstream, tc.health, tc.env, tc.goPort)
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./gen -run 'TestResolveGoPort|TestResolveUpstream' -count=1`
Expected: FAIL — `undefined: resolveGoPort`, and wrong argument count for `resolveUpstream`.

- [ ] **Step 3: Implement `resolveGoPort`**

Add to `gen/devserver.go`, immediately above `resolveUpstream`:

```go
// resolveGoPort resolves the port the Go server listens on and folds it back
// into env as GO_PORT, so the spawned server binds exactly the port gsx dev
// probes. Ports and their consumers must never disagree: the scaffold's
// main.go reads GO_PORT, and gen/dev.go hands this env to the server child.
//
// An explicit GO_PORT is strict — a busy port is a startup error, never a
// silent relocation, because a pinned port is usually load-bearing (absolute
// URLs, OAuth callbacks, a sibling worktree's fixed address). With GO_PORT
// unset the port floats, which is what lets two projects run side by side.
//
// held is the port our own server currently occupies (empty at startup): it is
// exempt from the busy check and is what the picker reuses, so re-resolving
// after an .env edit neither conflicts with our own child nor drifts. See
// portFree.
//
// upstreamSet reports whether gsx.toml declares [dev].upstream. That means the
// user places the backend themselves, so gsx dev neither picks a port nor
// injects GO_PORT into an app that may not read it — upstream stays purely
// observational.
func resolveGoPort(env []string, upstreamSet bool, held string) ([]string, string, error) {
	if upstreamSet {
		return env, "", nil
	}
	if port, ok := envLookup(env, "GO_PORT"); ok {
		if port == "" {
			// Distinct from absent: "http://localhost:" + "" round-trips past
			// url.Parse and Go's http client then dials port 80 — an
			// undiagnosable "server down".
			return nil, "", fmt.Errorf("GO_PORT is set but empty — unset it or give it a port number")
		}
		if _, err := strconv.Atoi(port); err != nil {
			return nil, "", fmt.Errorf("invalid GO_PORT %q", port)
		}
		if !portFree(port, held) {
			return nil, "", fmt.Errorf("GO_PORT %s is already in use — unset it (or comment it out in .env) to let gsx dev pick a free port", port)
		}
		return env, port, nil
	}
	port := held
	if port == "" {
		var err error
		port, err = nextAvailablePort("7777", "Go server")
		if err != nil {
			return nil, "", err
		}
	}
	return setEnvValue(env, "GO_PORT", port), port, nil
}
```

- [ ] **Step 4: Rework `resolveUpstream`'s default branch**

Replace the doc-comment sentence about the default and the `upstream == ""` branch (`gen/devserver.go:174-200`):

```go
// upstream is observational only: it never changes where the app listens.
// Empty upstream defaults to http://localhost:<goPort>, the port resolveGoPort
// resolved (and injected into the server's env) — GO_PORT has exactly one
// reader, and it is not this function.
```

```go
	if upstream == "" {
		if goPort == "" {
			return "", "", "", fmt.Errorf("no Go server port resolved for the default upstream")
		}
		origin = "http://localhost:" + goPort
		return origin, origin + health, goPort, nil
	}
```

and the signature:

```go
func resolveUpstream(upstream, health string, env []string, goPort string) (origin, healthURL, port string, err error) {
```

- [ ] **Step 5: Wire `gen/dev.go` startup**

Replace `gen/dev.go:74-82` with (note `bindPort` is the resolved listen port, `goPort` stays the upstream-derived one the panel reports):

```go
	var tdUpstream, tdHealth string
	if td != nil {
		tdUpstream, tdHealth = td.Upstream, td.Health
	}
	env, bindPort, err := resolveGoPort(env, tdUpstream != "", "")
	if err != nil {
		fmt.Fprintf(stderr, "gsx dev: %v\n", err)
		return 1
	}
	heldGo := bindPort
	origin, healthURL, goPort, err := resolveUpstream(tdUpstream, tdHealth, env, bindPort)
	if err != nil {
		fmt.Fprintf(stderr, "gsx dev: %v\n", err)
		return 1
	}
```

The `tdUpstream`/`tdHealth` declaration moves above the `resolveGoPort` call; delete the old block that declared it after `resolveViteDevEnv`.

- [ ] **Step 6: Wire the `.env`-fire path**

In `gen/dev.go`, the fire block computes all three resolutions into temporaries and commits them only when every one succeeds — a partial commit would leave `env` carrying a new Vite port with a stale Go port. Replace the body from `env, viteURL = resolvedEnv, newViteURL` (`gen/dev.go:484`) through `srv.healthURL = healthURL` (`gen/dev.go:506`) with:

```go
				goEnv, newBind, goErr := resolveGoPort(resolvedEnv, tdUpstream != "", heldGo)
				if goErr != nil {
					// Same treatment as envErr/upErr: a broken GO_PORT edit
					// must not crash the loop or corrupt the last-known-good
					// env — log, overlay, keep everything as it was.
					fmt.Fprintf(stderr, "gsx dev: %v\n", goErr)
					post(buildErrorEvent(goErr.Error()))
					overlayUp = true
					continue
				}
				newOrigin, newHealthURL, newPort, upErr := resolveUpstream(tdUpstream, tdHealth, goEnv, newBind)
				if upErr != nil {
					// A broken [dev].upstream (e.g. a now-unset ${VAR}, or an
					// env edit that produces a bare trailing ":") must not crash
					// a running dev loop or corrupt the last-known-good probe
					// target: log + overlay it and keep everything (server env,
					// healthURL, status) exactly as it was — mirrors the
					// envErr handling just above.
					fmt.Fprintf(stderr, "gsx dev: %v\n", upErr)
					post(buildErrorEvent(upErr.Error()))
					overlayUp = true
					continue
				}
				env, viteURL = goEnv, newViteURL
				heldVite = envPort(env, "VITE_PORT", "")
				heldGo = newBind
				if envWarning != "" {
					fmt.Fprintf(stderr, "gsx dev: %s\n", envWarning)
				}
				origin, healthURL, goPort = newOrigin, newHealthURL, newPort
				// Vite reads .env itself (loadEnv + native .env watch), so only the Go server is restarted here.
				srv.env = env
				status.Server.Port = goPort
				status.Server.Upstream = origin
				srv.healthURL = healthURL
```

The existing `resolveUpstream` call and the `env, viteURL = resolvedEnv, newViteURL` / `envWarning` lines this replaces are removed; `resolvedEnv` now flows into `resolveGoPort` instead of straight into `env`. Keep the `envErr` branch above exactly as it is.

- [ ] **Step 7: Run the unit tests**

Run: `go test ./gen -run 'TestResolveGoPort|TestResolveUpstream|TestResolveViteDevEnv' -count=1`
Expected: PASS.

- [ ] **Step 8: Write the failing integration test**

Two projects, neither pinning `GO_PORT`, must come up on different ports. Add the port-reporting line to `devTestMainGo` (`gen/dev_test.go:336`, right after `port` is computed) — existing tests never set `PORT_FILE`, so they are unaffected:

```go
	if pf := os.Getenv("PORT_FILE"); pf != "" {
		_ = os.WriteFile(pf, []byte(port), 0o644)
	}
```

Add the helper and the test to `gen/dev_test.go`:

```go
// devTestEnvNoGoPort is devTestEnv with GO_PORT also scrubbed, for tests that
// exercise the unset-GO_PORT auto-pick path. devTestEnv deliberately keeps
// GO_PORT (TestDevEnvPrecedence needs the shell's value to survive).
func devTestEnvNoGoPort(extra ...string) []string {
	var env []string
	for _, e := range devTestEnv() {
		if strings.HasPrefix(e, "GO_PORT=") {
			continue
		}
		env = append(env, e)
	}
	return append(env, extra...)
}

// TestDevTwoProjectsPickDistinctGoPorts is the reported bug: with GO_PORT
// unset, two gsx dev loops must not fight over 7777. Each scaffold server
// writes the port it actually bound to PORT_FILE, so this asserts the injected
// GO_PORT reached the child, not just that gsx dev picked something.
func TestDevTwoProjectsPickDistinctGoPorts(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test: requires building gsx and two live Go servers")
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not available")
	}

	gsxRoot := repoRoot(t)
	bin := filepath.Join(t.TempDir(), "gsx")
	buildCmd := exec.Command("go", "build", "-o", bin, "./cmd/gsx")
	buildCmd.Dir = gsxRoot
	if out, err := buildCmd.CombinedOutput(); err != nil {
		t.Fatalf("build gsx: %v\n%s", err, out)
	}
	gomod := fmt.Sprintf(
		"module devdemo\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => %s\n",
		gsxRoot,
	)

	portFiles := make([]string, 2)
	for i := range 2 {
		proj := t.TempDir()
		writeFile(t, proj, "go.mod", gomod)
		writeFile(t, proj, "main.go", devTestMainGo)
		writeFile(t, proj, "app.gsx", "package main\n\ncomponent Dummy() {\n\t<span>ok</span>\n}\n")
		// No .env at all: nothing pins GO_PORT, so both loops auto-pick.
		portFiles[i] = filepath.Join(t.TempDir(), "port")

		cmd := exec.Command(bin, "dev", "--web", "sleep 600")
		cmd.Dir = proj
		cmd.Env = devTestEnvNoGoPort("BROWSER=none", "GOFLAGS=-mod=mod", "PORT_FILE="+portFiles[i])
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		var stdout lockedBuffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stdout
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		defer stopDevGracefully(cmd)
		t.Cleanup(func() {
			if t.Failed() {
				t.Logf("project %d output:\n%s", i, stdout.String())
			}
		})
	}

	ports := make([]string, 2)
	for i, pf := range portFiles {
		waitFor(t, 180*time.Second, func() bool {
			b, err := os.ReadFile(pf)
			return err == nil && len(b) > 0
		})
		b, err := os.ReadFile(pf)
		if err != nil {
			t.Fatalf("read port file %d: %v", i, err)
		}
		ports[i] = string(b)
	}

	if ports[0] == ports[1] {
		t.Fatalf("both dev loops bound port %s — the second must pick a free one", ports[0])
	}
	for i, p := range ports {
		if !waitHealthy(context.Background(), "http://localhost:"+p+"/healthz", 60*time.Second) {
			t.Errorf("project %d server on port %s never became healthy", i, p)
		}
	}
}
```

- [ ] **Step 9: Verify the integration test fails on the old code and passes on the new**

Run: `git stash push gen/devserver.go gen/dev.go && go test ./gen -run TestDevTwoProjectsPickDistinctGoPorts -count=1 -timeout 900s; git stash pop`
Expected: FAIL before the fix (both loops report 7777, or the second server never becomes healthy). Then:

Run: `go test ./gen -run TestDevTwoProjectsPickDistinctGoPorts -count=1 -timeout 900s`
Expected: PASS.

- [ ] **Step 10: Run the whole dev suite**

Run: `go test ./gen -run TestDev -count=1 -timeout 900s`
Expected: PASS. `TestDevEnvErrorPostsOverlay` is the canary for Step 6: its recovery step re-resolves a `GO_PORT` our own server is holding, which only passes because `heldGo` is threaded through.

- [ ] **Step 11: Commit**

```bash
git add gen/devserver.go gen/devserver_test.go gen/dev.go gen/dev_test.go
git commit -m "gen: pick a free Go port when GO_PORT is unset"
```

---

## Task 4: Scaffold, docs, and the examples sibling

**Files:**
- Modify: `gen/templates/init/simple/dot-env`, `gen/templates/init/simple/dot-env.example`
- Modify: `docs/guide/config.md:91-95` and `docs/guide/config.md:110-119`
- Modify (separate repo): `~/personal/gsxhq/gsx-examples/streaming-partial/.env`

**Interfaces:**
- Consumes: the behavior shipped in Tasks 2 and 3. No code changes.

- [ ] **Step 1: Un-pin the scaffold's Go port**

Both `gen/templates/init/simple/dot-env` and `gen/templates/init/simple/dot-env.example` currently contain exactly `GO_PORT=7777`. Replace with:

```
# Backend port. Unset, gsx dev picks a free one so several projects can run
# at once; uncomment to pin it (a pinned port that's busy is an error).
# GO_PORT=7777
```

- [ ] **Step 2: Verify a scaffolded project still starts**

```bash
cd $(mktemp -d)
go run /Users/jackieli/personal/gsxhq/gsx-dev-go-port/cmd/gsx init .
grep -n "GO_PORT" .env
```
Expected: the `GO_PORT` line is present but commented.

- [ ] **Step 3: Run the init tests**

Run: `go test ./gen -run TestInit -count=1`
Expected: PASS.

- [ ] **Step 4: Document the rule**

In `docs/guide/config.md`, replace the port-precedence paragraph at lines 91-95 with (concise — behavior only, no rationale):

```markdown
An existing `VITE_DEV_URL` can supply the hostname when `host` is unset. Port
precedence: `VITE_PORT` wins, then a port in `VITE_DEV_URL`, then automatic
selection. If both are set and disagree, `VITE_PORT` wins and `gsx dev` logs a
warning.

Both ports follow one rule: unset means `gsx dev` picks a free port (from
`5173` for Vite, `7777` for the backend) and injects it into the processes it
starts, so several projects run side by side; set means the port is used as
given, and a busy one is a startup error.
```

Then, in the `upstream`/`health` section, replace the sentence beginning "The default, when `upstream` is unset, is …" (lines 111-113) with:

```markdown
The default, when `upstream` is unset, is `http://localhost:` plus the backend
port resolved above. Setting `upstream` hands port selection to you: `gsx dev`
neither picks a backend port nor sets `GO_PORT` for the server it starts.
```

- [ ] **Step 5: Update the examples sibling**

```bash
cd ~/personal/gsxhq/gsx-examples
```

In `streaming-partial/.env`, comment out `GO_PORT=7777` the same way as the scaffold. Verify two examples can then run at once (or, if only one example exists, that it still starts):

```bash
cd streaming-partial && go run github.com/gsxhq/gsx/cmd/gsx dev   # Ctrl-C after it reports healthy
```

Commit in that repo separately:

```bash
git add streaming-partial/.env
git commit -m "streaming-partial: let gsx dev pick a free backend port"
```

- [ ] **Step 6: Commit the gsx repo changes**

```bash
cd /Users/jackieli/personal/gsxhq/gsx-dev-go-port
git add gen/templates/init/simple/dot-env gen/templates/init/simple/dot-env.example docs/guide/config.md
git commit -m "init,docs: unpin the scaffold backend port and document port selection"
```

---

## Task 5: Full gate

**Files:** none (verification only)

- [ ] **Step 1: Format and lint**

Run: `make lint`
Expected: clean.

- [ ] **Step 2: Run the authoritative gate**

Run: `make ci`
Expected: PASS end to end. Do not proceed on a red or partially-run gate — read the exit status, do not infer success from the last lines of output.

Quiesce any dev loops running on this machine first: `make ci`'s dev integration tests bind real ports, and a running `gsx dev` (yours or another worktree's) can collide.

- [ ] **Step 3: Push and open the PR**

```bash
git push -u origin feat/dev-go-port
gh pr create --title "gsx dev: pick a free Go port when GO_PORT is unset" --body "$(cat <<'EOF'
`gsx dev` auto-picked a free Vite port but never a Go port, so two projects
could not run at once — both scaffolded backends pinned `GO_PORT=7777`.

- `resolveGoPort` is the single owner of `GO_PORT`: unset floats from 7777 and
  is injected into the spawned server's env; set is strict (a busy port is a
  startup error). `[dev].upstream`, when set, is untouched — no pick, no
  injection.
- Both resolvers now take the port our own child holds and exempt it from the
  busy check. This fixes a live bug: with an explicit front-door port, any
  `.env` edit re-resolved, found our own vite on it, and failed the restart
  with "already in use".
- The scaffold's `.env` ships `GO_PORT` commented out; existing projects need
  no migration.
EOF
)"
```

---

## Notes for the reviewer

- `resolveUpstream` no longer reads `GO_PORT`; if you see a second reader appear, that is the bug this refactor exists to prevent.
- The `held` exemption is deliberately narrow: it excuses exactly one port, the one our own child occupies. `TestResolveViteDevEnvAcceptsOwnHeldPort` pins that a stranger on the same port still fails.
- `--web "sleep 600"` front doors bind nothing. Any test about front-door port conflicts must use `devTestFrontDoorGo`.
