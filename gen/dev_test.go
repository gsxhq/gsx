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
	cmd := exec.Command(bin, "dev", "--web", "sleep 60")
	cmd.Dir = proj
	cmd.Env = devTestEnv(
		"BROWSER=none",
		"GO_PORT=7799",
		"VITE_DEV_URL=http://127.0.0.1:1",
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
	if !waitHealthy(context.Background(), "http://localhost:7799/healthz", 120*time.Second) {
		t.Fatal("Go server on GO_PORT=7799 never came up after gsx dev start")
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
	if !waitHealthy(context.Background(), "http://localhost:7799/healthz", 120*time.Second) {
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
		if !portListening("7799") {
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
	t.Error("GO_PORT=7799 still held 15s after group-SIGINT (teardown leaked; server not killed)")
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
		"VITE_DEV_URL=http://127.0.0.1:1",
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
