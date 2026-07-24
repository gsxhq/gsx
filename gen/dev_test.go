//go:build !windows

package gen

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func TestDevExitsWhenExplicitVitePortIsInUse(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:5173")
	if err != nil {
		t.Skipf("default Vite port unavailable before test: %v", err)
	}
	defer l.Close()

	proj := t.TempDir()
	writeFile(t, proj, "go.mod", "module devdemo\n\ngo 1.24\n")
	writeFile(t, proj, ".env", "VITE_PORT=5173\n")
	// runDev resolves env from os.Environ() before the project .env, so an
	// ambient VITE_PORT (e.g. exported by the developer's shell for another
	// project) would silently override the pinned port under test.
	t.Setenv("VITE_PORT", "5173")

	var stdout, stderr bytes.Buffer
	code := runDev(nil, &stdout, &stderr, config{}, nil, proj)
	if code != 1 {
		t.Fatalf("exit code = %d, want 1; stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "VITE_PORT 5173 is already in use") {
		t.Fatalf("stderr = %q, want port-in-use error", stderr.String())
	}
	if strings.Contains(stdout.String(), "watching") {
		t.Fatalf("runDev should exit before watching when VITE_PORT is in use; stdout=%q", stdout.String())
	}
}

// TestDevTeardownAndRestart is a full-stack integration test for `gsx dev`:
//   - builds the gsx binary
//   - scaffolds a minimal go project with a .gsx file and a /healthz endpoint
//   - runs `gsx dev` with a stub front door and synthetic env
//   - asserts the Go server comes up on GO_PORT
//   - touches a .go file → expects a rebuild cycle (server stays healthy)
//   - sends SIGINT to gsx dev's process group → asserts the port is freed (clean teardown)
//
// The test uses group-SIGINT rather than a PTY because gsx dev puts each child
// (Vite, Go server) in its OWN process group via setProcGroup. Sending SIGINT to
// gsx dev's group therefore does NOT reach the children — gsx dev must explicitly
// tear them down. That is exactly the orphan-prevention behavior under test.
func TestDevTeardownAndRestart(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test: requires building gsx and a live Go server")
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not available")
	}

	// 1. Build the gsx binary from the module root (gen/ is one level below it).
	gsxRoot := repoRoot(t)
	bin := filepath.Join(t.TempDir(), "gsx")
	buildCmd := exec.Command("go", "build", "-o", bin, "./cmd/gsx")
	buildCmd.Dir = gsxRoot
	if out, err := buildCmd.CombinedOutput(); err != nil {
		t.Fatalf("build gsx: %v\n%s", err, out)
	}

	// 2. Scaffold a minimal module:
	//   - go.mod with a local replace directive for github.com/gsxhq/gsx
	//   - main.go: stdlib-only HTTP server with /healthz, respects GO_PORT
	//   - app.gsx: minimal component so discoverDirs finds a .gsx directory
	proj := t.TempDir()
	gomod := fmt.Sprintf(
		"module devdemo\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => %s\n",
		gsxRoot,
	)
	writeFile(t, proj, "go.mod", gomod)
	writeFile(t, proj, "main.go", devTestMainGo)
	writeFile(t, proj, "app.gsx", "package main\n\ncomponent Dummy() {\n\t<span>ok</span>\n}\n")

	// 3. Run `gsx dev` with a stub front door so no npm/vite is needed.
	//    The stub front-door keeps running long enough for the test; gsx dev kills
	//    it via killProcGroup on shutdown. GOFLAGS=-mod=mod lets the internal
	//    `go build` update go.sum for the replaced local gsx module as needed.
	// A dynamic port, not a fixed one: a fixed port made the test hostage to
	// whatever else was bound there — an unrelated local server answering
	// /healthz 200 made waitHealthy pass against the WRONG process and the
	// freed-port assertion fail forever.
	goPort := freePort(t)
	cmd := exec.Command(bin, "dev", "--web", "sleep 60")
	cmd.Dir = proj
	cmd.Env = devTestEnv(
		"BROWSER=none",
		"GO_PORT="+goPort,
		"VITE_DEV_URL=http://127.0.0.1",
		"GOFLAGS=-mod=mod",
	)
	// gsx dev must run in its own process group so the group-SIGINT below only
	// reaches gsx dev itself — not the test harness.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// Capture stdout (asserted on below); drain stderr so its pipe never blocks.
	var stdout lockedBuffer
	cmd.Stdout = &stdout
	cmd.Stderr = devNullWriter{}
	if err := cmd.Start(); err != nil {
		t.Fatalf("gsx dev start: %v", err)
	}
	defer stopDevGracefully(cmd)

	// 4. Wait for the Go server to bind GO_PORT. The initial cycle is slow
	//    (cold go/packages.Load + go build), so allow a generous timeout.
	if !waitHealthy(context.Background(), "http://localhost:"+goPort+"/healthz", 120*time.Second) {
		t.Fatalf("Go server on GO_PORT=%s never came up after gsx dev start", goPort)
	}

	// 5. Touch a .go file to trigger the dep-dirty rebuild path in gsx dev.
	//    The fsnotify debounce (120ms) fires, regenPending reopens the module,
	//    go build rebuilds the binary, and the server restarts. The old server
	//    keeps running during the build, so the port stays healthy throughout.
	trig := filepath.Join(proj, "zz_trigger.go")
	if err := os.WriteFile(trig, []byte("package main\n\nvar _devtrigger = 1\n"), 0o644); err != nil {
		t.Fatalf("write trigger: %v", err)
	}
	// Brief sleep so the fsnotify debounce fires before we poll again.
	time.Sleep(500 * time.Millisecond)
	// The port must remain healthy through the rebuild. The long timeout covers
	// the full cycle (debounce + reopen + go build + server restart) on slow CI.
	if !waitHealthy(context.Background(), "http://localhost:"+goPort+"/healthz", 120*time.Second) {
		t.Error("server not healthy after .go file change (rebuild cycle may have failed)")
	}

	// 6. Simulate Ctrl-C: send SIGINT to gsx dev's process GROUP (negative PID).
	//    Because gsx dev put the web stub and Go server into their own groups,
	//    they do NOT receive this signal — gsx dev must explicitly tear them down.
	//    That is the orphan-prevention property under test.
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGINT); err != nil {
		t.Fatalf("SIGINT to gsx dev group: %v", err)
	}

	// 7. Assert GO_PORT is freed within a generous timeout (gsx dev kills the server,
	//    which does a graceful Shutdown releasing the port, then exits).
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if !portListening(goPort) {
			// Port is free; reap gsx dev to avoid a zombie.
			_ = cmd.Wait()
			// The front-door-exit notice is for an UNEXPECTED exit (pushes get
			// suspended); vite exiting because shutdown killed it is expected
			// and must not print it.
			if strings.Contains(stdout.String(), "front door exited") {
				t.Errorf("intentional shutdown printed the front-door-exit notice\nstdout:\n%s", stdout.String())
			}
			return // clean teardown ✓
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Errorf("GO_PORT=%s still held 15s after group-SIGINT (teardown leaked; server not killed)", goPort)
}

// TestDevEnvPrecedence pins shell-wins-over-.env for the dev loop: the
// project .env sets GO_PORT to one port, the gsx dev child's own environment
// (as the shell would export it) sets GO_PORT to a different port. Before
// mergeDotEnv, dev.go's `append(os.Environ(), loadDotEnv(workDir)...)` put the
// .env value LAST, so the spawned Go server (whose runtime env map is built
// last-entry-wins) bound the .env port while gsx dev's own envPort scan
// (first-match) health-checked the shell's port — the two disagreed and gsx
// dev could report the server down forever. This asserts the server actually
// comes up on the shell's port and the .env port never answers.
func TestDevEnvPrecedence(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test: requires building gsx and a live Go server")
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not available")
	}
	const portA = "7821" // set by the project .env
	const portB = "7823" // set by the gsx dev child's own environment (shell)

	gsxRoot := repoRoot(t)
	bin := filepath.Join(t.TempDir(), "gsx")
	buildCmd := exec.Command("go", "build", "-o", bin, "./cmd/gsx")
	buildCmd.Dir = gsxRoot
	if out, err := buildCmd.CombinedOutput(); err != nil {
		t.Fatalf("build gsx: %v\n%s", err, out)
	}

	proj := t.TempDir()
	gomod := fmt.Sprintf(
		"module devdemo\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => %s\n",
		gsxRoot,
	)
	writeFile(t, proj, "go.mod", gomod)
	writeFile(t, proj, "main.go", devTestMainGo)
	writeFile(t, proj, "app.gsx", "package main\n\ncomponent Dummy() {\n\t<span>ok</span>\n}\n")
	writeFile(t, proj, ".env", "GO_PORT="+portA+"\n")

	if portListening(portA) {
		t.Fatalf("port %s already in use (leaked scaffold server?)", portA)
	}
	if portListening(portB) {
		t.Fatalf("port %s already in use (leaked scaffold server?)", portB)
	}

	cmd := exec.Command(bin, "dev", "--web", "sleep 60")
	cmd.Dir = proj
	// devTestEnv scrubs VITE_PORT/VITE_DEV_URL from the ambient shell env but
	// deliberately NOT GO_PORT — GO_PORT=portB here stands in for the shell's
	// own export, distinct from the .env's portA.
	cmd.Env = devTestEnv(
		"BROWSER=none",
		"GO_PORT="+portB,
		"VITE_DEV_URL=http://127.0.0.1",
		"GOFLAGS=-mod=mod",
	)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	var stdout lockedBuffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stdout
	if err := cmd.Start(); err != nil {
		t.Fatalf("gsx dev start: %v", err)
	}
	defer stopDevGracefully(cmd)

	if !waitHealthy(context.Background(), "http://localhost:"+portB+"/healthz", 120*time.Second) {
		t.Fatalf("Go server never came up on shell GO_PORT=%s (shell should win over .env's %s); output:\n%s", portB, portA, stdout.String())
	}
	if portListening(portA) {
		t.Errorf(".env's GO_PORT=%s is listening — .env overrode the shell's GO_PORT=%s", portA, portB)
	}
}

// stopDevGracefully tears down a gsx dev child: SIGINT first so gsx dev kills
// its OWN children (they live in separate process groups — a bare SIGKILL to
// gsx dev's group would leak the scaffold Go server, which then shadows the
// next run's server on GO_PORT), SIGKILL as a backstop.
func stopDevGracefully(cmd *exec.Cmd) {
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGINT)
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		<-done
	}
}

// portListening reports whether anything is accepting TCP connections on
// localhost:port. Used to assert that GO_PORT is released after teardown.
func portListening(port string) bool {
	c, err := net.DialTimeout("tcp", "localhost:"+port, 200*time.Millisecond)
	if err != nil {
		return false
	}
	c.Close()
	return true
}

// devNullWriter is an io.Writer that discards all output. Used to drain gsx
// dev's stdout/stderr so its internal pipes never block.
type devNullWriter struct{}

func (devNullWriter) Write(p []byte) (int, error) { return len(p), nil }

// devTestMainGo is the source for the minimal Go server scaffolded by
// TestDevTeardownAndRestart. It serves /healthz on GO_PORT and shuts down
// gracefully on SIGTERM (which gsx dev sends via killProcGroup).
const devTestMainGo = `package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	port := os.Getenv("GO_PORT")
	if port == "" {
		port = "7777"
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/pid", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, "%d", os.Getpid())
	})
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	srv := &http.Server{Addr: ":" + port, Handler: mux}
	go func() { _ = srv.ListenAndServe() }()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	<-ctx.Done()

	shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutCtx)
}
`

// devTestMainGoADDR is a sibling of devTestMainGo that listens on ADDR (e.g.
// ":8890") instead of GO_PORT — used by TestDevUpstreamSingleSource to prove
// gsx dev's health probe/status follow a resolved [dev].upstream ${ADDR}
// reference rather than the GO_PORT default (ADDR is already ":port"-shaped,
// so unlike devTestMainGo it is used as Addr directly, no ":" + port).
const devTestMainGoADDR = `package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	addr := os.Getenv("ADDR")
	if addr == "" {
		addr = ":7777"
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/pid", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, "%d", os.Getpid())
	})
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	srv := &http.Server{Addr: addr, Handler: mux}
	go func() { _ = srv.ListenAndServe() }()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	<-ctx.Done()

	shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutCtx)
}
`

// TestDevUpstreamSingleSource pins the top-down [dev].upstream design end to
// end: gsx.toml carries "upstream = \"http://localhost${ADDR}\"" and the
// project's .env sets ADDR — GO_PORT appears NOWHERE in this test's env or
// config. It asserts:
//   - the scaffold server (listening on ADDR) is probed healthy at the
//     RESOLVED origin (proving the probe followed [dev].upstream, not the
//     GO_PORT default it would otherwise fall back to);
//   - a status event carries that same origin/port (the panel wire shape);
//   - the front door's spawned env carries GSX_DEV_UPSTREAM=<resolved origin>
//     and GSX_DEV_LOG=<absolute [dev].log path> (the vite-plugin cross-repo
//     contract lines).
func TestDevUpstreamSingleSource(t *testing.T) {
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

	proj := t.TempDir()
	gomod := fmt.Sprintf(
		"module devdemo\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => %s\n",
		gsxRoot,
	)
	writeFile(t, proj, "go.mod", gomod)
	writeFile(t, proj, "main.go", devTestMainGoADDR)
	writeFile(t, proj, "app.gsx", "package main\n\ncomponent Dummy() {\n\t<span>ok</span>\n}\n")

	port := freePort(t)
	addr := ":" + port
	writeFile(t, proj, ".env", "ADDR="+addr+"\n")
	writeFile(t, proj, "gsx.toml", "[dev]\nupstream = \"http://localhost${ADDR}\"\nlog = \"tmp/dev.log\"\n")
	// The front door's actual job here is just to prove GSX_DEV_UPSTREAM and
	// GSX_DEV_LOG reach its env — it writes both (one per line) to a marker
	// file rather than serving Vite. Two whitespace-separated argv (no
	// embedded quoting), so the --web flag's splitArgv (strings.Fields)
	// parses it correctly.
	writeFile(t, proj, "webstub.sh", "#!/bin/sh\n{ echo \"$GSX_DEV_UPSTREAM\"; echo \"$GSX_DEV_LOG\"; } > marker.txt\nsleep 600\n")

	if portListening(port) {
		t.Fatalf("port %s already in use (leaked scaffold server?)", port)
	}

	cmd := exec.Command(bin, "dev", "--web", "sh webstub.sh")
	cmd.Dir = proj
	// Deliberately NO GO_PORT anywhere in the child env: proves the probe
	// follows the resolved [dev].upstream, not the GO_PORT default.
	cmd.Env = devTestEnv(
		"BROWSER=none",
		"VITE_DEV_URL=http://127.0.0.1",
		"GOFLAGS=-mod=mod",
	)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	var stdout lockedBuffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stdout
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer stopDevGracefully(cmd)

	wantOrigin := "http://localhost:" + port
	if !waitHealthy(context.Background(), wantOrigin+"/healthz", 120*time.Second) {
		t.Fatalf("Go server on ADDR=%s (resolved upstream %s) never came up; output:\n%s", addr, wantOrigin, stdout.String())
	}

	// Learn the resolved front-door URL from the "watching … — open <url>" line.
	var viteURL string
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if m := regexp.MustCompile(`open (http://\S+)`).FindStringSubmatch(stdout.String()); m != nil {
			viteURL = m[1]
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if viteURL == "" {
		t.Fatalf("front-door URL not printed; stdout=%q", stdout.String())
	}
	u, err := url.Parse(viteURL)
	if err != nil {
		t.Fatal(err)
	}

	// Fake vite plugin bound at the resolved front-door address: long-poll
	// queue (to force a fresh status push once we're listening) + event
	// recorder, same pattern as TestDevPanelCommands.
	cmdQ := make(chan string, 4)
	var statusEvents lockedBuffer
	fake := &http.Server{
		Addr: net.JoinHostPort(u.Hostname(), u.Port()),
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("x-gsx", "1")
			switch r.URL.Path {
			case "/__gsx/cmd":
				select {
				case c := <-cmdQ:
					fmt.Fprintf(w, `{"cmds":[%q]}`, c)
				case <-time.After(2 * time.Second):
					w.WriteHeader(204)
				}
			case "/__gsx/event":
				body, _ := io.ReadAll(r.Body)
				if strings.Contains(string(body), `"event":"status"`) {
					statusEvents.Write(append(body, '\n'))
				}
				w.WriteHeader(204)
			default:
				w.WriteHeader(204)
			}
		}),
	}
	fl, err := net.Listen("tcp", fake.Addr)
	if err != nil {
		t.Fatalf("bind resolved front-door port %s: %v", fake.Addr, err)
	}
	go func() { _ = fake.Serve(fl) }()
	defer fake.Close()

	// Force a fresh status push now that the fake recorder is listening
	// (the cold startup post race-started before we could bind here).
	cmdQ <- "restart-server"

	wantUpstreamFrag := fmt.Sprintf(`"upstream":"%s"`, wantOrigin)
	wantPortFrag := fmt.Sprintf(`"port":"%s"`, port)
	deadline = time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(statusEvents.String(), wantUpstreamFrag) && strings.Contains(statusEvents.String(), wantPortFrag) {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if !strings.Contains(statusEvents.String(), wantUpstreamFrag) {
		t.Errorf("no status event carried %q; events:\n%s\nstdout:\n%s", wantUpstreamFrag, statusEvents.String(), stdout.String())
	}
	if !strings.Contains(statusEvents.String(), wantPortFrag) {
		t.Errorf("no status event carried %q; events:\n%s", wantPortFrag, statusEvents.String())
	}
	if !strings.Contains(statusEvents.String(), `"healthy":true`) {
		t.Errorf("no healthy status observed; status log:\n%s", statusEvents.String())
	}

	// Assert the front door's spawned env carried GSX_DEV_UPSTREAM and
	// GSX_DEV_LOG.
	markerPath := filepath.Join(proj, "marker.txt")
	var markerContent string
	markerDeadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(markerDeadline) {
		if b, err := os.ReadFile(markerPath); err == nil {
			markerContent = strings.TrimSpace(string(b))
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	markerLines := strings.Split(markerContent, "\n")
	if markerLines[0] != wantOrigin {
		t.Errorf("GSX_DEV_UPSTREAM in front-door env = %q, want %q", markerLines[0], wantOrigin)
	}
	// GSX_DEV_LOG must be absolute and name the file gsx dev actually
	// writes (TempDir may be behind a symlink on macOS, so compare by
	// stat-ing the env's own path rather than string-equality against proj).
	if len(markerLines) < 2 || markerLines[1] == "" {
		t.Fatalf("GSX_DEV_LOG missing from front-door env; marker=%q", markerContent)
	}
	gotLog := markerLines[1]
	if !filepath.IsAbs(gotLog) {
		t.Errorf("GSX_DEV_LOG = %q, want an absolute path", gotLog)
	}
	if filepath.Base(gotLog) != "dev.log" || filepath.Base(filepath.Dir(gotLog)) != "tmp" {
		t.Errorf("GSX_DEV_LOG = %q, want .../tmp/dev.log", gotLog)
	}
	if _, err := os.Stat(gotLog); err != nil {
		t.Errorf("GSX_DEV_LOG names %q but stat failed: %v (env must point at the file actually written)", gotLog, err)
	}
}

func TestKillProcGroupOwnedReapsViaExternalWaiter(t *testing.T) {
	cmd := exec.Command("sleep", "60")
	setProcGroup(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	// The monitor goroutine owns Wait, mirroring runDev's front-door monitor.
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	killProcGroupOwned(cmd, done, 5*time.Second)
	select {
	case <-done:
	default:
		t.Fatal("child not reaped after killProcGroupOwned returned")
	}
}

// Once the owning monitor has reaped the child (done closed), its pid may have
// been recycled by the OS to an unrelated process — killProcGroupOwned must not
// signal it. The victim process group stands in for whoever received the
// recycled pid: a Cmd whose recorded Process.Pid now belongs to the victim.
func TestKillProcGroupOwnedSkipsReapedChild(t *testing.T) {
	victim := exec.Command("sleep", "60")
	setProcGroup(victim)
	if err := victim.Start(); err != nil {
		t.Fatal(err)
	}
	victimExited := make(chan struct{})
	go func() { _ = victim.Wait(); close(victimExited) }()
	defer func() {
		_ = syscall.Kill(-victim.Process.Pid, syscall.SIGKILL)
		<-victimExited
	}()

	reaped := &exec.Cmd{Process: &os.Process{Pid: victim.Process.Pid}}
	done := make(chan struct{})
	close(done)
	killProcGroupOwned(reaped, done, 5*time.Second)

	select {
	case <-victimExited:
		t.Fatal("killProcGroupOwned signaled a reaped child's pid: the unrelated process group holding the recycled pid was killed")
	case <-time.After(500 * time.Millisecond):
	}
}

// devTestEnv returns os.Environ() with VITE_PORT / VITE_DEV_URL scrubbed
// (an ambient value from the developer's shell must not leak into the gsx dev
// under test) plus the given overrides.
func devTestEnv(extra ...string) []string {
	var env []string
	for _, e := range os.Environ() {
		if strings.HasPrefix(e, "VITE_PORT=") || strings.HasPrefix(e, "VITE_DEV_URL=") {
			continue
		}
		env = append(env, e)
	}
	return append(env, extra...)
}

// TestDevStopsPostingAfterWebExit reproduces cross-project overlay pollution:
// gsx dev resolves its front-door port once at startup; when the managed front
// door later exits, any other process (typically another project's vite dev
// server) can bind that port, and gsx dev's overlay/reload posts would land in
// a stranger's browser session. After the managed front door exits, gsx dev
// must stop posting.
func TestDevStopsPostingAfterWebExit(t *testing.T) {
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

	proj := t.TempDir()
	gomod := fmt.Sprintf(
		"module devdemo\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => %s\n",
		gsxRoot,
	)
	writeFile(t, proj, "go.mod", gomod)
	writeFile(t, proj, "main.go", devTestMainGo)
	writeFile(t, proj, "app.gsx", "package main\n\ncomponent Dummy() {\n\t<span>ok</span>\n}\n")

	// A leaked scaffold server from an earlier run would answer /healthz and
	// shadow this run's server (its ListenAndServe error is silent).
	if portListening("7811") {
		t.Fatal("port 7811 already in use (leaked scaffold server from an earlier run?)")
	}

	// The front door exits after 1s — long before the cold first cycle ends.
	cmd := exec.Command(bin, "dev", "--web", "sleep 1")
	cmd.Dir = proj
	cmd.Env = devTestEnv(
		"BROWSER=none",
		"GO_PORT=7811",
		"VITE_DEV_URL=http://127.0.0.1",
		"GOFLAGS=-mod=mod",
	)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	var stdout, stderrBuf lockedBuffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderrBuf
	if err := cmd.Start(); err != nil {
		t.Fatalf("gsx dev start: %v", err)
	}
	defer stopDevGracefully(cmd)

	if !waitHealthy(context.Background(), "http://localhost:7811/healthz", 120*time.Second) {
		t.Fatal("Go server on GO_PORT=7811 never came up after gsx dev start")
	}

	// Learn the resolved front-door URL from the "watching … — open <url>" line.
	var viteURL string
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if m := regexp.MustCompile(`open (http://\S+)`).FindStringSubmatch(stdout.String()); m != nil {
			viteURL = m[1]
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if viteURL == "" {
		t.Fatalf("front-door URL not printed; stdout=%q", stdout.String())
	}
	u, err := url.Parse(viteURL)
	if err != nil {
		t.Fatal(err)
	}

	// Wait until gsx dev's front door has given up: the "sleep 1" front door
	// exits every ~1s, so each respawn (an equally short-lived "sleep 1" that
	// never answers /__gsx/cmd) fails to verify, and after 3 rapid restart
	// attempts the frontDoor manager gives up for good — the gate then stays
	// permanently shut. Then let the startup posts' retry window drain: a push
	// issued while the front door was still alive may legitimately be delivered
	// a few seconds later (postBest retries + client timeout), and must not be
	// counted against the gate.
	deadline = time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) && !strings.Contains(stdout.String(), "giving up after repeated failures") {
		time.Sleep(100 * time.Millisecond)
	}
	if !strings.Contains(stdout.String(), "giving up after repeated failures") {
		t.Fatalf("gsx dev's front door never gave up; stdout=%q", stdout.String())
	}
	time.Sleep(4 * time.Second)

	// The front door is dead; bind its port as "another project's vite" and
	// record anything gsx dev still posts there.
	var posts atomic.Int32
	var postLog lockedBuffer
	recorder := &http.Server{
		Addr: net.JoinHostPort("127.0.0.1", u.Port()),
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPost {
				posts.Add(1)
				body, _ := io.ReadAll(r.Body)
				fmt.Fprintf(&postLog, "%s %s %s\n", time.Now().Format("15:04:05.000"), r.URL.Path, body)
			}
			w.WriteHeader(204)
		}),
	}
	rl, err := net.Listen("tcp", recorder.Addr)
	if err != nil {
		t.Fatalf("bind resolved front-door port %s: %v", recorder.Addr, err)
	}
	go func() { _ = recorder.Serve(rl) }()
	defer recorder.Close()

	// Change the server source so the completed cycle is observable: the
	// rebuilt+restarted server answers /gen2.
	writeFile(t, proj, "main.go", strings.Replace(devTestMainGo,
		"mux.HandleFunc(\"/healthz\"",
		"mux.HandleFunc(\"/gen2\", func(w http.ResponseWriter, _ *http.Request) {\n\t\tw.WriteHeader(http.StatusOK)\n\t})\n\tmux.HandleFunc(\"/healthz\"", 1))
	// The pre-rebuild server answers /gen2 with 404 (waitHealthy accepts any
	// response, so it cannot detect the swap); only an explicit 200 proves the
	// rebuilt+restarted server is up and the cycle completed.
	gen2OK := false
	cli := &http.Client{Timeout: 500 * time.Millisecond}
	deadline = time.Now().Add(120 * time.Second)
	for time.Now().Before(deadline) {
		if resp, err := cli.Get("http://localhost:7811/gen2"); err == nil {
			code := resp.StatusCode
			resp.Body.Close()
			if code == http.StatusOK {
				gen2OK = true
				break
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !gen2OK {
		onDisk, _ := os.ReadFile(filepath.Join(proj, "main.go"))
		t.Fatalf("rebuilt server never served /gen2 (rebuild cycle did not complete)\nposts=%d rewriteApplied=%v healthz=%v\nposts received:\n%s\ngsx dev stdout:\n%s\nstderr:\n%s",
			posts.Load(), strings.Contains(string(onDisk), "/gen2"),
			waitHealthy(context.Background(), "http://localhost:7811/healthz", time.Second),
			postLog.String(), stdout.String(), stderrBuf.String())
	}

	if n := posts.Load(); n != 0 {
		t.Errorf("gsx dev posted %d event(s) to the re-bound front-door port after its web process exited; want 0\nposts received:\n%s\ngsx dev stdout:\n%s", n, postLog.String(), stdout.String())
	}
}

// lockedBuffer is a mutex-guarded bytes.Buffer usable as an exec.Cmd output.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// TestDevPanelCommands drives gsx dev through the command channel: the test
// plays the vite plugin (serves /__gsx/cmd from a queue, records /__gsx/event
// posts) and asserts restart-server restarts the Go server and status events
// arrive.
//
// gsx dev resolves its own front-door URL (host from VITE_DEV_URL if given;
// the port from VITE_PORT, else VITE_DEV_URL's own port, else its
// auto-picker — see resolveViteDevEnv). The env here deliberately gives
// VITE_DEV_URL no port, so the fake plugin still can't pre-bind an arbitrary
// port that way: it must learn the real resolved URL from gsx dev's own
// "watching … — open <url>" banner and bind there, exactly like
// TestDevStopsPostingAfterWebExit.
func TestDevPanelCommands(t *testing.T) {
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

	proj := t.TempDir()
	gomod := fmt.Sprintf(
		"module devdemo\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => %s\n",
		gsxRoot,
	)
	writeFile(t, proj, "go.mod", gomod)
	writeFile(t, proj, "main.go", devTestMainGo)
	writeFile(t, proj, "app.gsx", "package main\n\ncomponent Dummy() {\n\t<span>ok</span>\n}\n")
	if portListening("7813") {
		t.Fatal("port 7813 already in use (leaked scaffold server?)")
	}

	cmd := exec.Command(bin, "dev", "--web", "sleep 600")
	cmd.Dir = proj
	cmd.Env = devTestEnv(
		"BROWSER=none",
		"GO_PORT=7813",
		"VITE_DEV_URL=http://127.0.0.1",
		"GOFLAGS=-mod=mod",
	)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	var stdout lockedBuffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stdout
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer stopDevGracefully(cmd)

	if !waitHealthy(context.Background(), "http://localhost:7813/healthz", 120*time.Second) {
		t.Fatalf("server never came up; output:\n%s", stdout.String())
	}

	// Learn the resolved front-door URL from the "watching … — open <url>" line.
	var viteURL string
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if m := regexp.MustCompile(`open (http://\S+)`).FindStringSubmatch(stdout.String()); m != nil {
			viteURL = m[1]
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if viteURL == "" {
		t.Fatalf("front-door URL not printed; stdout=%q", stdout.String())
	}
	u, err := url.Parse(viteURL)
	if err != nil {
		t.Fatal(err)
	}

	// Fake vite plugin bound at the resolved front-door address: long-poll
	// queue + event recorder, x-gsx stamped.
	cmdQ := make(chan string, 4)
	var statusEvents lockedBuffer
	fake := &http.Server{
		Addr: net.JoinHostPort(u.Hostname(), u.Port()),
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("x-gsx", "1")
			switch r.URL.Path {
			case "/__gsx/cmd":
				select {
				case c := <-cmdQ:
					fmt.Fprintf(w, `{"cmds":[%q]}`, c)
				case <-time.After(2 * time.Second):
					w.WriteHeader(204)
				}
			case "/__gsx/event":
				body, _ := io.ReadAll(r.Body)
				if strings.Contains(string(body), `"event":"status"`) {
					statusEvents.Write(append(body, '\n'))
				}
				w.WriteHeader(204)
			default:
				w.WriteHeader(204)
			}
		}),
	}
	fl, err := net.Listen("tcp", fake.Addr)
	if err != nil {
		t.Fatalf("bind resolved front-door port %s: %v", fake.Addr, err)
	}
	go func() { _ = fake.Serve(fl) }()
	defer fake.Close()

	pidOf := func() string {
		resp, err := http.Get("http://localhost:7813/pid")
		if err != nil {
			return ""
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return string(b)
	}
	before := pidOf()
	if before == "" {
		t.Fatal("could not read /pid")
	}

	cmdQ <- "restart-server"
	restartDeadline := time.Now().Add(60 * time.Second)
	restarted := false
	for time.Now().Before(restartDeadline) {
		if p := pidOf(); p != "" && p != before {
			restarted = true
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if !restarted {
		t.Errorf("restart-server did not restart the Go server (pid stayed %s)\noutput:\n%s", before, stdout.String())
	}

	// Status events observed (startup + restart transitions at minimum).
	if !strings.Contains(statusEvents.String(), `"event":"status"`) {
		t.Errorf("no status events posted; output:\n%s", stdout.String())
	}
	if !strings.Contains(statusEvents.String(), `"healthy":true`) {
		t.Errorf("no healthy status observed; status log:\n%s", statusEvents.String())
	}
}

// TestDevPanelRebuildCommand drives gsx dev's panel "rebuild" command through
// the same fake-vite pattern as TestDevPanelCommands: it asserts the log
// gains "gsx dev: panel: rebuild" AND the Go server actually restarts (pid
// change via /pid) even though nothing on disk changed — rebuild forces
// dirty.depDirty so cycle(true) always rebuilds+restarts.
func TestDevPanelRebuildCommand(t *testing.T) {
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

	proj := t.TempDir()
	gomod := fmt.Sprintf(
		"module devdemo\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => %s\n",
		gsxRoot,
	)
	writeFile(t, proj, "go.mod", gomod)
	writeFile(t, proj, "main.go", devTestMainGo)
	writeFile(t, proj, "app.gsx", "package main\n\ncomponent Dummy() {\n\t<span>ok</span>\n}\n")
	if portListening("7825") {
		t.Fatal("port 7825 already in use (leaked scaffold server?)")
	}

	cmd := exec.Command(bin, "dev", "--web", "sleep 600")
	cmd.Dir = proj
	cmd.Env = devTestEnv(
		"BROWSER=none",
		"GO_PORT=7825",
		"VITE_DEV_URL=http://127.0.0.1",
		"GOFLAGS=-mod=mod",
	)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	var stdout lockedBuffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stdout
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer stopDevGracefully(cmd)

	if !waitHealthy(context.Background(), "http://localhost:7825/healthz", 120*time.Second) {
		t.Fatalf("server never came up; output:\n%s", stdout.String())
	}

	// Learn the resolved front-door URL from the "watching … — open <url>" line.
	var viteURL string
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if m := regexp.MustCompile(`open (http://\S+)`).FindStringSubmatch(stdout.String()); m != nil {
			viteURL = m[1]
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if viteURL == "" {
		t.Fatalf("front-door URL not printed; stdout=%q", stdout.String())
	}
	u, err := url.Parse(viteURL)
	if err != nil {
		t.Fatal(err)
	}

	cmdQ := make(chan string, 4)
	fake := &http.Server{
		Addr: net.JoinHostPort(u.Hostname(), u.Port()),
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("x-gsx", "1")
			switch r.URL.Path {
			case "/__gsx/cmd":
				select {
				case c := <-cmdQ:
					fmt.Fprintf(w, `{"cmds":[%q]}`, c)
				case <-time.After(2 * time.Second):
					w.WriteHeader(204)
				}
			default:
				w.WriteHeader(204)
			}
		}),
	}
	fl, err := net.Listen("tcp", fake.Addr)
	if err != nil {
		t.Fatalf("bind resolved front-door port %s: %v", fake.Addr, err)
	}
	go func() { _ = fake.Serve(fl) }()
	defer fake.Close()

	pidOf := func() string {
		resp, err := http.Get("http://localhost:7825/pid")
		if err != nil {
			return ""
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return string(b)
	}
	before := pidOf()
	if before == "" {
		t.Fatal("could not read /pid")
	}

	cmdQ <- "rebuild"
	restartDeadline := time.Now().Add(60 * time.Second)
	restarted := false
	for time.Now().Before(restartDeadline) {
		if p := pidOf(); p != "" && p != before {
			restarted = true
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if !restarted {
		t.Errorf("rebuild did not restart the Go server (pid stayed %s)\noutput:\n%s", before, stdout.String())
	}
	if !strings.Contains(stdout.String(), "gsx dev: panel: rebuild") {
		t.Errorf("log missing %q; output:\n%s", "gsx dev: panel: rebuild", stdout.String())
	}
}

// TestDevEnvErrorPostsOverlay is the final-review fix-round-1 regression test
// for the envErr branch of the .env-fire path (gen/dev.go): a resolveViteDevEnv
// failure (e.g. an .env edit that sets VITE_PORT to a port already in use)
// must post an error event to the browser overlay, exactly like the sibling
// resolveUpstream (upErr) branch just below it — before this fix, envErr only
// logged to the terminal.
//
// It also guards a bug the naive fix would otherwise reintroduce: the .env-fire
// code assigns resolveViteDevEnv's four return values directly into the
// function-scoped env/viteURL variables (`env, viteURL, envWarning, envErr =
// resolveViteDevEnv(...)`); on error those returns are the zero values, so a
// naive "just add post()" would clobber viteURL to "" first — and post()'s
// underlying postBest() treats an empty base URL as a same-origin path and
// no-ops, silently defeating the fix it was meant to implement. This test
// pins that the overlay post actually reaches the front door AND that a
// subsequent, successful .env edit still reaches it too (proving env/viteURL
// survive an intervening error untouched).
func TestDevEnvErrorPostsOverlay(t *testing.T) {
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
	// Held for the whole test so VITE_PORT=blockedPort deterministically fails
	// resolveViteDevEnv's portAvailable check — this is the "already in use"
	// .env edit under test, not a transient race.
	blockedPort := freePort(t)
	bl, err := net.Listen("tcp", "127.0.0.1:"+blockedPort)
	if err != nil {
		t.Fatalf("hold blocked port %s: %v", blockedPort, err)
	}
	defer bl.Close()

	goodEnv := "GO_PORT=" + goPort + "\nVITE_PORT=" + vitePort + "\n"
	writeFile(t, proj, ".env", goodEnv)

	if portListening(goPort) {
		t.Fatalf("port %s already in use (leaked scaffold server?)", goPort)
	}

	cmd := exec.Command(bin, "dev", "--web", "sleep 600")
	cmd.Dir = proj
	// No VITE_DEV_URL/VITE_PORT in the shell env: VITE_PORT is pinned via .env
	// alone so every env recompute (startup and every later .env fire) resolves
	// to the SAME port deterministically — no auto-pick variance to guard against.
	cmd.Env = devTestEnv("BROWSER=none", "GOFLAGS=-mod=mod")
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

	wantViteURL := "http://localhost:" + vitePort
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) && !strings.Contains(stdout.String(), "open "+wantViteURL) {
		time.Sleep(100 * time.Millisecond)
	}
	if !strings.Contains(stdout.String(), "open "+wantViteURL) {
		t.Fatalf("front-door URL %q not printed (VITE_PORT pinning failed?); output:\n%s", wantViteURL, stdout.String())
	}

	// Fake vite plugin bound at the pinned front-door address: records every
	// /__gsx/event POST body and every /__reload hit.
	var events lockedBuffer
	var reloads atomic.Int32
	fake := &http.Server{
		Addr: net.JoinHostPort("localhost", vitePort),
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("x-gsx", "1")
			switch r.URL.Path {
			case "/__gsx/event":
				body, _ := io.ReadAll(r.Body)
				events.Write(append(body, '\n'))
			case "/__reload":
				reloads.Add(1)
			case "/__gsx/cmd":
				time.Sleep(50 * time.Millisecond)
			}
			w.WriteHeader(204)
		}),
	}
	fl, err := net.Listen("tcp", fake.Addr)
	if err != nil {
		t.Fatalf("bind resolved front-door port %s: %v", fake.Addr, err)
	}
	go func() { _ = fake.Serve(fl) }()
	defer fake.Close()

	// The .env edit under test: VITE_PORT now points at the held blockedPort —
	// resolveViteDevEnv must fail with "already in use", and the failure must
	// be posted to the browser overlay (not just logged).
	writeFile(t, proj, ".env", "GO_PORT="+goPort+"\nVITE_PORT="+blockedPort+"\n")

	wantErrFrag := "VITE_PORT " + blockedPort + " is already in use"
	deadline = time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) && !strings.Contains(events.String(), wantErrFrag) {
		time.Sleep(100 * time.Millisecond)
	}
	if !strings.Contains(events.String(), wantErrFrag) {
		t.Fatalf("no error event posted for the broken VITE_PORT edit (want %q); events:\n%s\nstdout:\n%s",
			wantErrFrag, events.String(), stdout.String())
	}
	if !strings.Contains(events.String(), `"ok":false`) {
		t.Errorf("posted error event should be ok:false (buildErrorEvent shape); events:\n%s", events.String())
	}

	// Recovery: revert .env to the original, valid VITE_PORT. If envErr's
	// failed resolveViteDevEnv had clobbered the shared env/viteURL variables,
	// this cycle's reload() would silently target an empty/wrong base URL and
	// the fake listener (still bound at the ORIGINAL vitePort) would never see
	// it — proving env/viteURL survived the intervening error untouched.
	writeFile(t, proj, ".env", goodEnv)

	deadline = time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) && reloads.Load() == 0 {
		time.Sleep(100 * time.Millisecond)
	}
	if reloads.Load() == 0 {
		t.Fatalf("no /__reload after reverting .env to a valid VITE_PORT — env/viteURL likely corrupted by the prior error; stdout:\n%s", stdout.String())
	}
	if !waitHealthy(context.Background(), "http://localhost:"+goPort+"/healthz", 30*time.Second) {
		t.Fatalf("server not healthy after recovery; output:\n%s", stdout.String())
	}
}
