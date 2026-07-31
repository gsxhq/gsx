package gen

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestRunDevContextCancelShutsDownCleanly is the runDevContext seam's consumer:
// the dev loop runs fully in-process (upstream mode, --no-web: no front door,
// no managed Go child), and canceling the supplied context shuts it down
// exactly as a SIGINT would — return code 0, promptly.
//
// Serial (t.Setenv): VITE_DEV_URL must point at a closed local port so the
// session's status posts can never leak to a real Vite that happens to be
// running on this machine.
func TestRunDevContextCancelShutsDownCleanly(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping module-resolution test in -short mode")
	}
	t.Setenv("VITE_DEV_URL", "http://127.0.0.1:1")

	proj := t.TempDir()
	repo := repoRoot(t)
	writeFile(t, proj, "go.mod", "module devctx\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repo+"\n")
	writeFile(t, proj, "app.gsx", "package main\n\ncomponent Dummy() {\n\t<span>ok</span>\n}\n")
	// upstream: gsx manages no Go child; --no-web below: no front door either.
	writeFile(t, proj, "gsx.toml", "[dev]\nupstream = \"http://127.0.0.1:1\"\n")

	merged, configPath, err := resolveConfig(config{}, proj)
	if err != nil {
		t.Fatalf("resolveConfig: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var mu sync.Mutex
	var stdout, stderr bytes.Buffer
	lockedWrite := func(buf *bytes.Buffer) func([]byte) (int, error) {
		return func(p []byte) (int, error) {
			mu.Lock()
			defer mu.Unlock()
			return buf.Write(p)
		}
	}
	outW := writerFunc(lockedWrite(&stdout))
	errW := writerFunc(lockedWrite(&stderr))

	done := make(chan int, 1)
	go func() {
		done <- runDevContext(ctx, []string{"--no-web"}, outW, errW, merged, devTomlFor(configPath), proj)
	}()

	// Wait for the loop to be entered (the watching banner), then cancel.
	deadline := time.Now().Add(30 * time.Second)
	for {
		mu.Lock()
		entered := strings.Contains(stdout.String(), "watching "+filepath.Clean(proj))
		mu.Unlock()
		if entered {
			break
		}
		if time.Now().After(deadline) {
			mu.Lock()
			t.Fatalf("dev loop never announced watching; stdout=%q stderr=%q", stdout.String(), stderr.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()

	select {
	case code := <-done:
		if code != 0 {
			mu.Lock()
			t.Fatalf("runDevContext returned %d after cancel, want 0 (clean shutdown); stderr=%q", code, stderr.String())
		}
	case <-time.After(15 * time.Second):
		t.Fatal("runDevContext did not return within 15s of context cancel")
	}
}

// writerFunc adapts a function to io.Writer for the locked test buffers.
type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }
