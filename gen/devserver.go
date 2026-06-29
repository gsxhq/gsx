package gen

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// prefixWriter prefixes each complete line written to it with "[name] ". Writes
// to w are serialized via the shared mu so concurrent children don't interleave.
type prefixWriter struct {
	name string
	w    io.Writer
	mu   *sync.Mutex
	buf  []byte
}

func (p *prefixWriter) Write(b []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.buf = append(p.buf, b...)
	for {
		i := bytes.IndexByte(p.buf, '\n')
		if i < 0 {
			break
		}
		line := strings.TrimRight(string(p.buf[:i]), "\r")
		fmt.Fprintf(p.w, "[%s] %s\n", p.name, line)
		p.buf = p.buf[i+1:]
	}
	return len(b), nil
}

// loadDotEnv parses KEY=VALUE lines from dir/.env (blanks and #-comments
// skipped). Returns nil if the file is absent.
func loadDotEnv(dir string) []string {
	b, err := os.ReadFile(filepath.Join(dir, ".env"))
	if err != nil {
		return nil
	}
	var out []string
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		out = append(out, line)
	}
	return out
}

// envPort reads KEY's value from a KEY=VALUE env slice, returning def if absent.
func envPort(env []string, key, def string) string {
	pre := key + "="
	for _, e := range env {
		if strings.HasPrefix(e, pre) {
			return strings.TrimPrefix(e, pre)
		}
	}
	return def
}

// envValue is envPort by another name for non-port keys (VITE_DEV_URL).
func envValue(env []string, key, def string) string { return envPort(env, key, def) }

// waitHealthy polls url until it returns any HTTP status (2xx–5xx ⇒ the server
// is accepting connections) or timeout elapses. A refused connection retries.
func waitHealthy(ctx context.Context, url string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	client := &http.Client{Timeout: 500 * time.Millisecond}
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return false
		default:
		}
		if resp, err := client.Get(url); err == nil {
			resp.Body.Close()
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return false
}

// aggregateEvent marshals a batch of cycle results into one `generated` event
// (the shape the vite plugin's /__gsx/event consumes): ok = all OK, diagnostics
// = concat, written = concat base names, durationMs = sum.
func aggregateEvent(results []cycleResult) []byte {
	ok := true
	var written []string
	diags := []json.RawMessage{}
	var dur int64
	for _, r := range results {
		ok = ok && r.OK
		written = append(written, baseNames(r.Written)...)
		dur += r.durationMs()
		raw := rawDiagnostics(r.Diags) // a JSON array
		var arr []json.RawMessage
		if json.Unmarshal(raw, &arr) == nil {
			diags = append(diags, arr...)
		}
	}
	ev := map[string]any{
		"event":       "generated",
		"ok":          ok,
		"durationMs":  dur,
		"written":     written,
		"diagnostics": diags,
	}
	b, _ := json.Marshal(ev)
	return b
}

// postEvent best-effort POSTs body to base+/__gsx/event (overlay state).
func postEvent(base string, body []byte) { postBest(base+"/__gsx/event", body) }

// postReload best-effort POSTs to base+/__reload (browser full-reload).
func postReload(base string) { postBest(base+"/__reload", nil) }

// postBest POSTs with a few short retries so a not-yet-up Vite isn't fatal
// (mirrors vite.NotifyReload's cold-start handling). dev-only; base "" no-ops.
func postBest(url string, body []byte) {
	if strings.HasPrefix(url, "/") || url == "" {
		return
	}
	go func() {
		client := &http.Client{Timeout: 2 * time.Second}
		for i := 0; i < 10; i++ {
			resp, err := client.Post(url, "application/json", bytes.NewReader(body))
			if err == nil {
				resp.Body.Close()
				return
			}
			time.Sleep(150 * time.Millisecond)
		}
	}()
}

// devServer supervises the built Go server child with build-then-swap semantics.
type devServer struct {
	build     []string // argv to build (writes the binary)
	run       []string // argv to run the built binary
	env       []string
	out       io.Writer // combined terminal+log writer for server output
	healthURL string    // e.g. http://localhost:7777/healthz

	mu  sync.Mutex
	cmd *exec.Cmd
}

// rebuild builds the server; on build failure the currently-running server is
// LEFT RUNNING (go build does not replace the binary on failure) and the build
// error is returned. On success it stops the old server and starts the new one.
func (d *devServer) rebuild(ctx context.Context) error {
	bcmd := exec.CommandContext(ctx, d.build[0], d.build[1:]...)
	bcmd.Env, bcmd.Stdout, bcmd.Stderr = d.env, d.out, d.out
	if err := bcmd.Run(); err != nil {
		fmt.Fprintf(d.out, "build failed: %v\n", err)
		return err
	}
	return d.restartNoBuild(ctx)
}

// restartNoBuild stops any running server and starts d.run fresh (used after a
// successful build and on .env changes).
func (d *devServer) restartNoBuild(ctx context.Context) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.cmd != nil {
		killProcGroup(d.cmd, 5*time.Second)
		d.cmd = nil
	}
	cmd := exec.Command(d.run[0], d.run[1:]...)
	cmd.Env, cmd.Stdout, cmd.Stderr = d.env, d.out, d.out
	setProcGroup(cmd)
	if err := cmd.Start(); err != nil {
		fmt.Fprintf(d.out, "start failed: %v\n", err)
		return err
	}
	d.cmd = cmd
	return nil
}

func (d *devServer) stop() {
	d.mu.Lock()
	defer d.mu.Unlock()
	killProcGroup(d.cmd, 5*time.Second)
	d.cmd = nil
}
