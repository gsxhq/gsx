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

// mergeDotEnv appends dotenv entries onto base, skipping any whose KEY
// already appears in base. This gives the shell precedence over the
// project .env: gsx dev's linear envPort/envLookup scan (effectively
// first-match) and the spawned Go server's runtime env map (Go builds it
// last-entry-wins) would disagree on the merge produced by a plain
// append(base, dotenv...) whenever a key is duplicated — the two readers
// must never see a duplicate key to begin with. Malformed dotenv entries
// (no '=') are skipped; they can't be evaluated for a KEY and would carry
// through as-is otherwise.
func mergeDotEnv(base, dotenv []string) []string {
	out := base
	for _, e := range dotenv {
		key, _, ok := strings.Cut(e, "=")
		if !ok {
			continue
		}
		if _, present := envLookup(out, key); present {
			continue
		}
		out = append(out, e)
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

// expandEnvRefs replaces every ${NAME} reference in s with NAME's value
// looked up in env (a KEY=VALUE slice, e.g. the merged shell+.env env
// resolveViteDevEnv works from). Expansion is a single pass: an expanded
// value is never itself re-scanned for further ${...} references. A bare $
// or $VAR with no braces is left untouched — only ${...} is special, and
// there is no escape mechanism.
//
// An unset variable, a variable that is SET BUT EMPTY, an empty name (${}),
// or an unterminated ${ (no closing }) is a startup error rather than a
// silent empty string; the error names the offending reference/variable so a
// bad gsx.toml [dev].upstream fails loudly. Set-but-empty gets the same
// treatment as unset: e.g. ADDR="" in .env would otherwise silently collapse
// "http://localhost${ADDR}" to "http://localhost" — a valid origin (port 80)
// with no diagnostic, exactly the undiagnosable "server down" this function
// exists to prevent.
func expandEnvRefs(s string, env []string) (string, error) {
	var b strings.Builder
	rest := s
	for {
		i := strings.Index(rest, "${")
		if i < 0 {
			b.WriteString(rest)
			break
		}
		b.WriteString(rest[:i])
		after := rest[i+2:]
		name, tail, ok := strings.Cut(after, "}")
		if !ok {
			return "", fmt.Errorf("unterminated %q in %q", rest[i:], s)
		}
		if name == "" {
			return "", fmt.Errorf("empty %q reference in %q", "${}", s)
		}
		v, set := envLookup(env, name)
		if !set {
			return "", fmt.Errorf("unset env var %q referenced in %q", name, s)
		}
		if v == "" {
			return "", fmt.Errorf("env var %q referenced in %q is set but empty", name, s)
		}
		b.WriteString(v)
		rest = tail
	}
	return b.String(), nil
}

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

// resolveUpstream resolves [dev].upstream/health into the origin gsx dev
// probes and reports, and the healthURL it probes — the single source of
// truth also injected into the vite child as GSX_DEV_UPSTREAM (origin only,
// no path) so the plugin's devFallback() and gsx dev's panel/probe never
// drift from independently-guessed env vars.
//
// upstream is observational only: it never changes where the app listens.
// Empty upstream defaults to http://localhost:<goPort>, the port resolveGoPort
// resolved (and injected into the server's env) — GO_PORT has exactly one
// reader, and it is not this function. A non-empty upstream is ${VAR}-expanded
// (expandEnvRefs) against env, then parsed as an absolute http/https URL; a
// non-empty path/query/fragment is rejected since upstream must be an origin.
// health defaults to "/healthz" and must be an absolute path; healthURL is
// origin+health. port is u.Port() (may be empty when the URL carries none).
func resolveUpstream(upstream, health string, env []string, goPort string) (origin, healthURL, port string, err error) {
	if health == "" {
		health = "/healthz"
	}
	if !strings.HasPrefix(health, "/") {
		return "", "", "", fmt.Errorf("[dev].health %q must start with \"/\"", health)
	}

	if upstream == "" {
		if goPort == "" {
			return "", "", "", fmt.Errorf("no Go server port resolved for the default upstream")
		}
		origin = "http://localhost:" + goPort
		return origin, origin + health, goPort, nil
	}

	expanded, err := expandEnvRefs(upstream, env)
	if err != nil {
		return "", "", "", fmt.Errorf("[dev].upstream: %w", err)
	}

	u, err := url.Parse(expanded)
	if err != nil {
		return "", "", "", fmt.Errorf("[dev].upstream %q: invalid URL: %w", expanded, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", "", "", fmt.Errorf("[dev].upstream %q: scheme must be http or https", expanded)
	}
	if u.Host == "" {
		return "", "", "", fmt.Errorf("[dev].upstream %q: missing host", expanded)
	}
	if u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return "", "", "", fmt.Errorf("[dev].upstream %q: must be an origin only (no path/query/fragment)", expanded)
	}
	if strings.HasSuffix(u.Host, ":") && u.Port() == "" {
		// A literal trailing ":" already in [dev].upstream itself (no ${...}
		// reference at all, e.g. a hardcoded "http://localhost:" in gsx.toml)
		// round-trips through url.Parse unnoticed (Host "localhost:", Port()
		// ""), and Go's http client then silently dials port 80 — an
		// undiagnosable "server down". Reject it, naming the template and its
		// expansion. Set-but-empty ${VAR} references (e.g.
		// "http://localhost:${ADDR}" with ADDR="") are now caught earlier, by
		// expandEnvRefs itself, naming the variable — this guard only remains
		// reachable for a literal, var-free trailing colon.
		return "", "", "", fmt.Errorf("[dev].upstream %q expands to %q: host %q has an empty port (bare trailing \":\") — check the referenced env var(s) aren't empty", upstream, expanded, u.Host)
	}

	origin = u.Scheme + "://" + u.Host
	return origin, origin + health, u.Port(), nil
}

// resolveViteDevEnv resolves the Vite dev server's host:port and folds it
// back into env as VITE_PORT/VITE_DEV_URL for the spawned front door and Go
// server to agree on.
//
// Precedence: VITE_PORT > VITE_DEV_URL's port > auto-pick from 5173. An
// explicit port — either form — fails loudly (hard error) if it's already
// bound; only a truly port-less configuration (no VITE_PORT, and no port on
// VITE_DEV_URL) auto-picks. When [dev].host is unset, the hostname from an
// existing VITE_DEV_URL (typically a gitignored .env) is honored too, so a
// per-machine dev hostname needs no committed config; that lookup is
// independent of the port precedence above. If VITE_PORT and VITE_DEV_URL's
// port disagree, VITE_PORT wins and the non-empty warning return names the
// override — the caller should print it once. held is the port gsx dev's own
// front door is currently on (empty at startup): it is exempt from the busy
// check and is what the auto-picker returns, so re-resolving after an .env
// edit neither conflicts with our own child nor drifts to a port nothing
// listens on.
func resolveViteDevEnv(env []string, host, held string) ([]string, string, string, error) {
	var urlPort string
	if raw := envValue(env, "VITE_DEV_URL", ""); raw != "" {
		if u, err := url.Parse(raw); err == nil && u.Hostname() != "" {
			if host == "" {
				host = u.Hostname()
			}
			urlPort = u.Port()
		}
	}

	var warning string
	port, hasVitePort := envLookup(env, "VITE_PORT")
	var err error
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

	if host == "" {
		host = "localhost"
	}
	viteURL := "http://" + host + ":" + port
	env = setEnvValue(env, "VITE_PORT", port)
	env = setEnvValue(env, "VITE_DEV_URL", viteURL)
	return env, viteURL, warning, nil
}

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

func portAvailable(port string) bool {
	// The wildcard probe catches listeners bound to *:port (e.g. a running
	// vite): with SO_REUSEADDR (set by net.Listen) the specific-address probes
	// can succeed alongside a wildcard listener on at least macOS. The specific
	// probes in turn catch listeners the wildcard probe may coexist with.
	for _, addr := range []string{":" + port, "127.0.0.1:" + port, "[::1]:" + port} {
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

// devHTTPClient returns a client for the dev loop's chatter with the Vite dev
// server and the backend (health polls, overlay pushes, command polls, front
// door verification), with
// keep-alives disabled. An idle pooled connection to Vite (Node) is torn down
// by Node's keepAliveTimeout, and the teardown can emit an unsolicited
// "HTTP/1.1 400 Bad Request" that Go's transport logs to the terminal as
// "Unsolicited response received on idle HTTP channel" — alarming noise in
// every gsx dev session. These clients make at most one localhost request
// every few seconds, so per-request connections cost nothing.
func devHTTPClient(timeout time.Duration) *http.Client {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.DisableKeepAlives = true
	return &http.Client{Timeout: timeout, Transport: t}
}

// waitHealthy polls url until it returns any HTTP status (2xx–5xx ⇒ the server
// is accepting connections) or timeout elapses. A refused connection retries.
func waitHealthy(ctx context.Context, url string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	client := devHTTPClient(500 * time.Millisecond)
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
	removed := []string{}
	diags := []json.RawMessage{}
	var dur int64
	for _, r := range results {
		ok = ok && r.OK
		written = append(written, baseNames(r.Written)...)
		removed = append(removed, baseNames(r.Removed)...)
		dur += r.durationMs()
		raw := rawDiagnostics(r.Diags) // a JSON array
		var arr []json.RawMessage
		if json.Unmarshal(raw, &arr) == nil {
			diags = append(diags, arr...)
		}
		// A hard operational error with no diagnostics (e.g. a skeleton parse
		// error Module.Generate returns as a plain error, not a parse-diagnostic)
		// would otherwise vanish: the event goes out ok:false with an empty
		// diagnostics array and the vite overlay shows nothing. Fold it into a
		// synthetic error diagnostic so the overlay carries the message. Mirrors
		// watchemit's cycle() fallback for the generate --watch path.
		if !r.OK && len(r.Diags) == 0 && r.Err != nil {
			if b, err := json.Marshal(syntheticErrorDiag("gsx", r.Err.Error())); err == nil {
				diags = append(diags, b)
			}
		}
	}
	ev := map[string]any{
		"event":       "generated",
		"ok":          ok,
		"durationMs":  dur,
		"written":     written,
		"removed":     removed,
		"diagnostics": diags,
	}
	// firstReload's empty-string return omits the key entirely — never a
	// rendered empty reason (see cycleResult.Reload's doc).
	if reload := firstReload(results); reload != "" {
		ev["reload"] = reload
	}
	b, _ := json.Marshal(ev)
	return b
}

// firstReload returns the first non-empty cycleResult.Reload across results,
// "" if none. A batch's world-reload reason is module-wide, not per-dir (see
// regenDirs' stamping convention), so at most one result in a batch normally
// carries a non-empty Reload — but "first non-empty" stays correct even for a
// batch spanning multiple modules, each of which stamps independently.
func firstReload(results []cycleResult) string {
	for _, r := range results {
		if r.Reload != "" {
			return r.Reload
		}
	}
	return ""
}

// reportHardErrors echoes each cycle's hard operational error (Err set with no
// Diags) to w. These are the failures aggregateEvent folds into the browser
// overlay; mirroring them on the terminal keeps gsx dev from appearing to hang
// silently when codegen returns a plain error (e.g. a skeleton parse error) that
// isn't a source diagnostic. Type-check diagnostics are not echoed here — they
// already reach the developer through the overlay.
func reportHardErrors(w io.Writer, results []cycleResult) {
	for _, r := range results {
		if !r.OK && len(r.Diags) == 0 && r.Err != nil {
			fmt.Fprintf(w, "gsx: %v\n", r.Err)
		}
	}
}

// postEvent best-effort POSTs body to base+/__gsx/event (overlay state).
func (p *poster) postEvent(base string, body []byte, gate func() bool) {
	p.post(base+"/__gsx/event", body, gate)
}

// postReload best-effort POSTs to base+/__reload (browser full-reload).
func (p *poster) postReload(base string, gate func() bool) { p.post(base+"/__reload", nil, gate) }

// poster owns every best-effort browser push a dev session fires. Each post
// still runs in its own goroutine (delivery must not block the watch loop),
// but the goroutines are joined: drain aborts pending retries AND in-flight
// requests, then waits, so no push can land after teardown — previously the
// fire-and-forget loop could retry for up to 1.5s past runDev's return and
// deliver into a port a stranger had since rebound (the gate narrows that
// window but cannot close it; only joining does).
type poster struct {
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// newPoster derives the poster's lifetime from ctx: session shutdown (SIGINT /
// SIGTERM / test cancel) aborts deliveries even before drain is called.
func newPoster(ctx context.Context) *poster {
	p := &poster{}
	p.ctx, p.cancel = context.WithCancel(ctx)
	return p
}

// drain stops retrying, aborts any in-flight request, and waits for every post
// goroutine to exit. Idempotent; returns promptly (bounded by request
// cancellation, not by the retry schedule).
func (p *poster) drain() {
	p.cancel()
	p.wg.Wait()
}

// post POSTs with a few short retries so a not-yet-up Vite isn't fatal
// (mirrors vite.NotifyReload's cold-start handling). dev-only; base "" no-ops.
// gate, when non-nil, is re-checked before every attempt: the retry window can
// straddle the managed front door's exit, after which the port may belong to a
// stranger and the push must not be delivered.
func (p *poster) post(url string, body []byte, gate func() bool) {
	if strings.HasPrefix(url, "/") || url == "" {
		return
	}
	p.wg.Go(func() {
		client := devHTTPClient(2 * time.Second)
		for range 10 {
			if p.ctx.Err() != nil {
				return
			}
			if gate != nil && !gate() {
				return
			}
			req, err := http.NewRequestWithContext(p.ctx, http.MethodPost, url, bytes.NewReader(body))
			if err != nil {
				return
			}
			req.Header.Set("Content-Type", "application/json")
			resp, err := client.Do(req)
			if err == nil {
				resp.Body.Close()
				return
			}
			select {
			case <-p.ctx.Done():
				return
			case <-time.After(150 * time.Millisecond):
			}
		}
	})
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

// buildFailureMessage is the overlay counterpart of writeBuildFailure: the
// compiler output when there is any, otherwise the error itself. Operational
// failures (exec errors, a failed server start) produce no compiler output,
// and posting buildErrorEvent(output) alone would show an empty overlay.
func buildFailureMessage(output string, err error) string {
	if strings.TrimSpace(output) != "" {
		return output
	}
	if err != nil {
		return err.Error()
	}
	return ""
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

// syntheticErrorDiag builds a single error-severity diagnostic (line 1, col 1)
// carrying msg under the given file label, in the shape the vite plugin's
// overlay consumes. Used for operational failures codegen's type-check doesn't
// surface as source diagnostics (go-build errors, and hard cycleResult.Err
// values with no Diags — e.g. a skeleton parse error Module.Generate returns as
// a plain error rather than a parse-diagnostic).
func syntheticErrorDiag(file, msg string) map[string]any {
	return map[string]any{
		"file":     file,
		"range":    map[string]any{"start": map[string]int{"line": 1, "col": 1}, "end": map[string]int{"line": 1, "col": 1}},
		"severity": "error",
		"message":  strings.TrimSpace(msg),
	}
}

// buildErrorEvent makes an ok:false "generated" event whose single error
// diagnostic carries msg, so the vite plugin renders it in the overlay. Used
// for go-build and operational failures that codegen's type-check doesn't catch.
func buildErrorEvent(msg string) []byte {
	ev := map[string]any{
		"event": "generated", "ok": false, "durationMs": 0,
		"written":     []string{},
		"diagnostics": []map[string]any{syntheticErrorDiag("build", msg)},
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
