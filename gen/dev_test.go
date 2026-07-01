//go:build !windows

package gen

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
	writeFile(t, proj, ".env", "VITE_PORT=5173\n")

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
	cmd.Env = append(os.Environ(),
		"BROWSER=none",
		"GO_PORT=7799",
		"VITE_DEV_URL=http://127.0.0.1:1",
		"GOFLAGS=-mod=mod",
	)
	// gsx dev must run in its own process group so the group-SIGINT below only
	// reaches gsx dev itself — not the test harness.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// Drain gsx dev output so its pipe never blocks.
	cmd.Stdout = devNullWriter{}
	cmd.Stderr = devNullWriter{}
	if err := cmd.Start(); err != nil {
		t.Fatalf("gsx dev start: %v", err)
	}
	defer func() {
		// Safety: if the test panics or returns early, kill gsx dev.
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		_ = cmd.Wait()
	}()

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
			return // clean teardown ✓
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Error("GO_PORT=7799 still held 15s after group-SIGINT (teardown leaked; server not killed)")
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
