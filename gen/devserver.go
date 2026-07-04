package gen

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
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
		line = normalizePrefixedLine(p.name, line)
		fmt.Fprintf(p.w, "[%s] %s\n", p.name, line)
		p.buf = p.buf[i+1:]
	}
	return len(b), nil
}

func normalizePrefixedLine(name, line string) string {
	if name == "vite" {
		return strings.Replace(line, " [vite] [gsx]", " [gsx]", 1)
	}
	return line
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
	if v, ok := envLookup(env, key); ok {
		return v
	}
	return def
}

func envLookup(env []string, key string) (string, bool) {
	pre := key + "="
	for _, e := range env {
		if v, ok := strings.CutPrefix(e, pre); ok {
			return v, true
		}
	}
	return "", false
}

// envValue is envPort by another name for non-port keys (VITE_DEV_URL).
func envValue(env []string, key, def string) string { return envPort(env, key, def) }

func resolveViteDevEnv(env []string, host string) ([]string, string, error) {
	// When [dev].host is unset, honor the hostname from an existing VITE_DEV_URL
	// (typically a gitignored .env) so a per-machine dev hostname needs no
	// committed config. Only the host is taken — the port still comes from
	// VITE_PORT or the auto-picker, so a stale URL never pins a busy port.
	if host == "" {
		if raw := envValue(env, "VITE_DEV_URL", ""); raw != "" {
			if u, err := url.Parse(raw); err == nil && u.Hostname() != "" {
				host = u.Hostname()
			}
		}
	}
	port, ok := envLookup(env, "VITE_PORT")
	var err error
	if ok {
		if _, err := strconv.Atoi(port); err != nil {
			return nil, "", fmt.Errorf("invalid VITE_PORT %q", port)
		}
		if !portAvailable(port) {
			return nil, "", fmt.Errorf("VITE_PORT %s is already in use", port)
		}
	} else {
		port, err = nextAvailablePort("5173")
	}
	if err != nil {
		return nil, "", err
	}
	if host == "" {
		host = "localhost"
	}
	viteURL := "http://" + host + ":" + port
	env = setEnvValue(env, "VITE_PORT", port)
	env = setEnvValue(env, "VITE_DEV_URL", viteURL)
	return env, viteURL, nil
}

func nextAvailablePort(start string) (string, error) {
	port, err := strconv.Atoi(start)
	if err != nil {
		return "", fmt.Errorf("invalid VITE_PORT %q", start)
	}
	for ; port <= 65535; port++ {
		candidate := strconv.Itoa(port)
		if portAvailable(candidate) {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("choose Vite dev port: no free port at or above %s", start)
}

func portAvailable(port string) bool {
	for _, addr := range []string{"127.0.0.1:" + port, "[::1]:" + port} {
		l, err := net.Listen("tcp", addr)
		if err == nil {
			l.Close()
			continue
		}
		if errors.Is(err, syscall.EADDRINUSE) {
			return false
		}
	}
	return true
}

func setEnvValue(env []string, key, value string) []string {
	pre := key + "="
	for i, e := range env {
		if strings.HasPrefix(e, pre) {
			out := slices.Clone(env)
			out[i] = pre + value
			return out
		}
	}
	return append(env, pre+value)
}

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
	written := []string{}
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
		for range 10 {
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
	// dir is the project working dir; child build/run commands run here.
	dir       string
	build     []string // argv to build (writes the binary)
	run       []string // argv to run the built binary
	env       []string
	out       io.Writer // combined terminal+log writer for server output
	buildOut  io.Writer // build diagnostics; defaults to out when nil
	healthURL string    // e.g. http://localhost:7777/healthz

	mu  sync.Mutex
	cmd *exec.Cmd
}

// rebuild builds the server; on build failure the currently-running server is
// LEFT RUNNING (go build does not replace the binary on failure) and the build
// error is returned along with captured compiler output. On success it stops the
// old server and starts the new one.
func (d *devServer) rebuild(ctx context.Context) (string, error) {
	var buf bytes.Buffer
	bcmd := exec.CommandContext(ctx, d.build[0], d.build[1:]...)
	bcmd.Dir, bcmd.Env, bcmd.Stdout, bcmd.Stderr = d.dir, d.env, &buf, &buf
	if err := bcmd.Run(); err != nil {
		writeBuildFailure(d.effectiveBuildOut(), buf.String(), err)
		return buf.String(), err
	}
	return "", d.restartNoBuild()
}

func (d *devServer) effectiveBuildOut() io.Writer {
	if d.buildOut != nil {
		return d.buildOut
	}
	return d.out
}

func writeBuildFailure(w io.Writer, output string, err error) {
	if w == nil {
		return
	}
	fmt.Fprintln(w, "error build")
	output = strings.TrimSpace(output)
	if output == "" {
		if err != nil {
			fmt.Fprintf(w, "  %v\n", err)
		}
		return
	}
	for line := range strings.SplitSeq(output, "\n") {
		line = strings.TrimRight(line, "\r")
		if line != "" {
			fmt.Fprintf(w, "  %s\n", line)
		}
	}
}

// buildErrorEvent makes an ok:false "generated" event whose single error
// diagnostic carries msg, so the vite plugin renders it in the overlay. Used
// for go-build and operational failures that codegen's type-check doesn't catch.
func buildErrorEvent(msg string) []byte {
	ev := map[string]any{
		"event": "generated", "ok": false, "durationMs": 0,
		"written": []string{},
		"diagnostics": []map[string]any{{
			"file":     "build",
			"range":    map[string]any{"start": map[string]int{"line": 1, "col": 1}, "end": map[string]int{"line": 1, "col": 1}},
			"severity": "error",
			"message":  strings.TrimSpace(msg),
		}},
	}
	b, _ := json.Marshal(ev)
	return b
}

// restartNoBuild stops any running server and starts d.run fresh (used after a
// successful build and on .env changes).
func (d *devServer) restartNoBuild() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.cmd != nil {
		killProcGroup(d.cmd, 5*time.Second)
		d.cmd = nil
	}
	cmd := exec.Command(d.run[0], d.run[1:]...)
	cmd.Dir, cmd.Env, cmd.Stdout, cmd.Stderr = d.dir, d.env, d.out, d.out
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
	if d.cmd == nil {
		return
	}
	killProcGroup(d.cmd, 5*time.Second)
	d.cmd = nil
}
