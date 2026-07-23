package gen

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gsxhq/gsx/internal/diag"
)

func TestPrefixWriterLineBuffers(t *testing.T) {
	var out bytes.Buffer
	var mu sync.Mutex
	pw := &prefixWriter{name: "server", w: &out, mu: &mu}
	pw.Write([]byte("hel"))
	pw.Write([]byte("lo\nwor"))
	pw.Write([]byte("ld\n"))
	got := out.String()
	if got != "[server] hello\n[server] world\n" {
		t.Errorf("got %q", got)
	}
}

func TestPrefixWriterNormalizesViteLoggerPrefix(t *testing.T) {
	var out bytes.Buffer
	var mu sync.Mutex
	pw := &prefixWriter{name: "vite", w: &out, mu: &mu}
	pw.Write([]byte("17:51:41 [vite] [gsx] error /tmp/main.go\n"))
	pw.Write([]byte("6:02:44 PM [vite] [gsx] error build\n"))
	got := out.String()
	want := "" +
		"[vite] 17:51:41 [gsx] error /tmp/main.go\n" +
		"[vite] 6:02:44 PM [gsx] error build\n"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestWriteBuildFailureFormatsAsGsxDiagnostic(t *testing.T) {
	var out bytes.Buffer
	writeBuildFailure(&out, "# hello-gsx\n./main.go:69:2: undefined: hello\n", nil)
	got := out.String()
	want := "error build\n  # hello-gsx\n  ./main.go:69:2: undefined: hello\n"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestLoadDotEnv(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".env"),
		[]byte("# comment\nGO_PORT=7777\n\nVITE_DEV_URL=http://localhost:5173\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	env := loadDotEnv(dir)
	if envPort(env, "GO_PORT", "x") != "7777" {
		t.Errorf("GO_PORT not parsed: %v", env)
	}
}

// TestMergeDotEnv pins the shell-wins-over-.env precedence: mergeDotEnv only
// appends dotenv entries whose KEY is absent from base, so a duplicate KEY=
// pair (shell env map wins last-entry-wins; gsx dev's linear scan wins
// first-match) never occurs in the merged slice — both readers then agree.
func TestMergeDotEnv(t *testing.T) {
	t.Run("dotenv-only key is added", func(t *testing.T) {
		got := mergeDotEnv([]string{"PATH=/bin"}, []string{"GO_PORT=7777"})
		if envPort(got, "GO_PORT", "") != "7777" {
			t.Errorf("GO_PORT not merged in: %v", got)
		}
	})

	t.Run("shell-present key is not overridden", func(t *testing.T) {
		got := mergeDotEnv([]string{"GO_PORT=9000"}, []string{"GO_PORT=7777"})
		if envPort(got, "GO_PORT", "") != "9000" {
			t.Errorf("shell GO_PORT was overridden by .env: %v", got)
		}
		n := 0
		for _, e := range got {
			if strings.HasPrefix(e, "GO_PORT=") {
				n++
			}
		}
		if n != 1 {
			t.Errorf("expected exactly one GO_PORT= entry, got %d: %v", n, got)
		}
	})

	t.Run("malformed entries without '=' are skipped", func(t *testing.T) {
		got := mergeDotEnv([]string{"PATH=/bin"}, []string{"NOTANASSIGNMENT", "GO_PORT=7777"})
		if envPort(got, "GO_PORT", "") != "7777" {
			t.Errorf("well-formed entry not merged: %v", got)
		}
		for _, e := range got {
			if e == "NOTANASSIGNMENT" {
				t.Errorf("malformed entry leaked into merged env: %v", got)
			}
		}
	})

	t.Run("empty dotenv returns base unchanged", func(t *testing.T) {
		base := []string{"PATH=/bin", "GO_PORT=9000"}
		got := mergeDotEnv(base, nil)
		if len(got) != len(base) {
			t.Fatalf("got %v, want unchanged %v", got, base)
		}
		for i, e := range got {
			if e != base[i] {
				t.Errorf("got[%d]=%q, want %q", i, e, base[i])
			}
		}
	})
}

// TestExpandEnvRefs pins ${VAR} expansion semantics for [dev].upstream: single
// pass (no re-expansion of expanded values), bare $/$VAR left untouched, and
// unset/malformed references are startup errors naming the offending var.
func TestExpandEnvRefs(t *testing.T) {
	env := []string{"ADDR=:8890", "SCHEME=http", "P=9000"}

	cases := []struct {
		name    string
		in      string
		want    string
		wantErr string // substring expected in error, "" means no error
	}{
		{name: "single var concatenation", in: "http://localhost${ADDR}", want: "http://localhost:8890"},
		{name: "multi var", in: "${SCHEME}://x:${P}", want: "http://x:9000"},
		{name: "unset var errors naming it", in: "${NOPE}", wantErr: "NOPE"},
		{name: "bare dollar sign untouched", in: "$ADDR literal", want: "$ADDR literal"},
		{name: "empty braces error", in: "${}", wantErr: "${}"},
		{name: "unterminated brace errors", in: "${X", wantErr: "${X"},
		{name: "no refs unchanged", in: "http://localhost:7777", want: "http://localhost:7777"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := expandEnvRefs(tc.in, env)
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("expandEnvRefs(%q) = %q, nil; want error containing %q", tc.in, got, tc.wantErr)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Errorf("expandEnvRefs(%q) error = %q, want substring %q", tc.in, err.Error(), tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("expandEnvRefs(%q) unexpected error: %v", tc.in, err)
			}
			if got != tc.want {
				t.Errorf("expandEnvRefs(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestResolveUpstream pins [dev].upstream/health resolution: empty upstream
// falls back to today's http://localhost:${GO_PORT|7777} behavior; a
// non-empty upstream is ${VAR}-expanded, parsed, and must be an origin only
// (http/https, host, no path/query/fragment).
func TestResolveUpstream(t *testing.T) {
	cases := []struct {
		name          string
		upstream      string
		health        string
		env           []string
		wantOrigin    string
		wantHealthURL string
		wantPort      string
		wantErrSubstr string
	}{
		{
			name:          "absent upstream no GO_PORT defaults to 7777",
			upstream:      "",
			env:           nil,
			wantOrigin:    "http://localhost:7777",
			wantHealthURL: "http://localhost:7777/healthz",
			wantPort:      "7777",
		},
		{
			name:          "absent upstream honors GO_PORT",
			upstream:      "",
			env:           []string{"GO_PORT=8081"},
			wantOrigin:    "http://localhost:8081",
			wantHealthURL: "http://localhost:8081/healthz",
			wantPort:      "8081",
		},
		{
			name:          "upstream expands ADDR",
			upstream:      "http://localhost${ADDR}",
			env:           []string{"ADDR=:8890"},
			wantOrigin:    "http://localhost:8890",
			wantHealthURL: "http://localhost:8890/healthz",
			wantPort:      "8890",
		},
		{
			name:          "explicit upstream with no port",
			upstream:      "http://mstudio",
			wantOrigin:    "http://mstudio",
			wantHealthURL: "http://mstudio/healthz",
			wantPort:      "",
		},
		{
			name:          "path in upstream errors",
			upstream:      "http://localhost:8890/foo",
			wantErrSubstr: "path",
		},
		{
			name:          "non-http scheme errors",
			upstream:      "ftp://localhost:8890",
			wantErrSubstr: "scheme",
		},
		{
			name:          "unset var in upstream errors",
			upstream:      "http://localhost${NOPE}",
			wantErrSubstr: "NOPE",
		},
		{
			name:          "custom health path suffixes healthURL",
			upstream:      "http://localhost:8890",
			health:        "/live",
			wantOrigin:    "http://localhost:8890",
			wantHealthURL: "http://localhost:8890/live",
			wantPort:      "8890",
		},
		{
			// A present-but-empty env var expanding into a literal ":" in the
			// template produces "http://localhost:" — url.Parse accepts it
			// silently (Host "localhost:", Port ""), and Go's http client would
			// then dial port 80 without complaint, giving an undiagnosable
			// "server down". This must be a resolution error naming the value.
			name:          "explicit upstream with present-but-empty var yields bare trailing colon errors",
			upstream:      "http://localhost:${ADDR}",
			env:           []string{"ADDR="},
			wantErrSubstr: "empty port",
		},
		{
			name:          "default upstream with GO_PORT present but empty errors",
			upstream:      "",
			env:           []string{"GO_PORT="},
			wantErrSubstr: "GO_PORT",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			origin, healthURL, port, err := resolveUpstream(tc.upstream, tc.health, tc.env)
			if tc.wantErrSubstr != "" {
				if err == nil {
					t.Fatalf("resolveUpstream(%q, %q) = (%q, %q, %q), nil; want error containing %q",
						tc.upstream, tc.health, origin, healthURL, port, tc.wantErrSubstr)
				}
				if !strings.Contains(err.Error(), tc.wantErrSubstr) {
					t.Errorf("resolveUpstream(%q, %q) error = %q, want substring %q",
						tc.upstream, tc.health, err.Error(), tc.wantErrSubstr)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveUpstream(%q, %q) unexpected error: %v", tc.upstream, tc.health, err)
			}
			if origin != tc.wantOrigin {
				t.Errorf("origin = %q, want %q", origin, tc.wantOrigin)
			}
			if healthURL != tc.wantHealthURL {
				t.Errorf("healthURL = %q, want %q", healthURL, tc.wantHealthURL)
			}
			if port != tc.wantPort {
				t.Errorf("port = %q, want %q", port, tc.wantPort)
			}
		})
	}
}

// freePort binds an ephemeral port, reads back the port the OS assigned, and
// releases it immediately. The window between release and the caller's own
// use is a theoretical race (acceptable here, same tolerance as the other
// port-probing tests in this file) but avoids colliding on a fixed number
// across parallel test suites.
func freePort(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_, port, err := net.SplitHostPort(l.Addr().String())
	if err != nil {
		l.Close()
		t.Fatal(err)
	}
	l.Close()
	return port
}

func TestResolveViteDevEnvSkipsBoundDefaultPort(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:5173")
	if err != nil {
		t.Skipf("default Vite port unavailable before test: %v", err)
	}
	defer l.Close()
	wantPort, err := nextAvailablePort("5173")
	if err != nil {
		t.Fatal(err)
	}

	env, viteURL, warning, err := resolveViteDevEnv([]string{"PATH=/bin"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if warning != "" {
		t.Errorf("warning = %q, want none", warning)
	}
	wantURL := "http://localhost:" + wantPort
	if viteURL != wantURL {
		t.Fatalf("viteURL = %q, want %s", viteURL, wantURL)
	}
	if got := envPort(env, "VITE_PORT", ""); got != wantPort {
		t.Fatalf("VITE_PORT = %q, want %s", got, wantPort)
	}
	if envValue(env, "VITE_DEV_URL", "") != viteURL {
		t.Fatalf("VITE_DEV_URL env and returned URL differ: env=%q url=%q", envValue(env, "VITE_DEV_URL", ""), viteURL)
	}
}

func TestResolveViteDevEnvHost(t *testing.T) {
	env, viteURL, _, err := resolveViteDevEnv([]string{"VITE_PORT=0", "PATH=/bin"}, "mstudio")
	if err != nil {
		// port 0 is "available" (ephemeral); if the platform rejects it, fall back.
		env, viteURL, _, err = resolveViteDevEnv([]string{"PATH=/bin"}, "mstudio")
	}
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(viteURL, "http://mstudio:") {
		t.Fatalf("viteURL = %q, want http://mstudio:<port>", viteURL)
	}
	if got := envValue(env, "VITE_DEV_URL", ""); got != viteURL {
		t.Fatalf("VITE_DEV_URL = %q, want %q", got, viteURL)
	}
}

func TestResolveViteDevEnvHostFromDevURL(t *testing.T) {
	port := freePort(t)
	devURL := "VITE_DEV_URL=http://mstudio:" + port
	// With no [dev].host, the hostname comes from VITE_DEV_URL in the env, and
	// (with VITE_PORT unset) so does the port.
	_, viteURL, warning, err := resolveViteDevEnv([]string{devURL, "PATH=/bin"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if warning != "" {
		t.Errorf("warning = %q, want none", warning)
	}
	wantURL := "http://mstudio:" + port
	if viteURL != wantURL {
		t.Fatalf("viteURL = %q, want %s (VITE_DEV_URL's port honored)", viteURL, wantURL)
	}
	// An explicit [dev].host wins over VITE_DEV_URL's hostname; the URL's port
	// is still honored.
	_, viteURL2, warning2, err := resolveViteDevEnv([]string{devURL, "PATH=/bin"}, "override")
	if err != nil {
		t.Fatal(err)
	}
	if warning2 != "" {
		t.Errorf("warning = %q, want none", warning2)
	}
	wantURL2 := "http://override:" + port
	if viteURL2 != wantURL2 {
		t.Fatalf("viteURL = %q, want %s ([dev].host override wins on host, URL port still honored)", viteURL2, wantURL2)
	}
}

func TestResolveViteDevEnvPortlessURLAutoPicks(t *testing.T) {
	// A VITE_DEV_URL with no port keeps today's behavior exactly: hostname
	// hint only, port still comes from the auto-picker.
	_, viteURL, warning, err := resolveViteDevEnv([]string{"VITE_DEV_URL=http://mstudio", "PATH=/bin"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if warning != "" {
		t.Errorf("warning = %q, want none", warning)
	}
	if !strings.HasPrefix(viteURL, "http://mstudio:") {
		t.Fatalf("viteURL = %q, want http://mstudio:<auto-picked port>", viteURL)
	}
}

func TestResolveViteDevEnvHonorsURLPortWhenBusy(t *testing.T) {
	port := freePort(t)
	l, err := net.Listen("tcp", "127.0.0.1:"+port)
	if err != nil {
		t.Skipf("could not hold port %s for test: %v", port, err)
	}
	defer l.Close()

	_, _, _, err = resolveViteDevEnv([]string{"VITE_DEV_URL=http://mstudio:" + port, "PATH=/bin"}, "")
	if err == nil {
		t.Fatal("expected VITE_DEV_URL's busy explicit port to fail")
	}
	if !strings.Contains(err.Error(), port) || !strings.Contains(err.Error(), "already in use") {
		t.Fatalf("error = %q, want a port-in-use message naming %s", err, port)
	}
	// The message must attribute the pin to its source: a URL-derived pin
	// reported as a VITE_PORT problem would send the user to the wrong line.
	if !strings.Contains(err.Error(), "VITE_DEV_URL") {
		t.Fatalf("error = %q, want the pin attributed to VITE_DEV_URL", err)
	}
}

func TestResolveViteDevEnvVitePortAgreesWithURLNoWarning(t *testing.T) {
	port := freePort(t)
	env := []string{"VITE_PORT=" + port, "VITE_DEV_URL=http://mstudio:" + port, "PATH=/bin"}
	_, viteURL, warning, err := resolveViteDevEnv(env, "")
	if err != nil {
		t.Fatal(err)
	}
	if warning != "" {
		t.Errorf("warning = %q, want none (VITE_PORT and VITE_DEV_URL agree)", warning)
	}
	want := "http://mstudio:" + port
	if viteURL != want {
		t.Fatalf("viteURL = %q, want %s", viteURL, want)
	}
}

func TestResolveViteDevEnvVitePortOverridesDisagreeingURL(t *testing.T) {
	vitePort := freePort(t)
	urlPort := freePort(t)
	env := []string{"VITE_PORT=" + vitePort, "VITE_DEV_URL=http://mstudio:" + urlPort, "PATH=/bin"}
	_, viteURL, warning, err := resolveViteDevEnv(env, "")
	if err != nil {
		t.Fatal(err)
	}
	want := "http://mstudio:" + vitePort
	if viteURL != want {
		t.Fatalf("viteURL = %q, want %s (VITE_PORT wins)", viteURL, want)
	}
	if warning == "" {
		t.Fatal("expected a warning that VITE_PORT overrides VITE_DEV_URL's port")
	}
	if strings.Count(warning, "\n") != 0 {
		t.Fatalf("warning = %q, want exactly one line (emitted once)", warning)
	}
	if !strings.Contains(warning, "VITE_PORT="+vitePort) || !strings.Contains(warning, ":"+urlPort) {
		t.Fatalf("warning = %q, want it to name both the winning VITE_PORT=%s and overridden :%s", warning, vitePort, urlPort)
	}
}

func TestResolveViteDevEnvSkipsIPv6BoundDefaultPort(t *testing.T) {
	l, err := net.Listen("tcp", "[::1]:5173")
	if err != nil {
		t.Skipf("IPv6 default Vite port unavailable before test: %v", err)
	}
	defer l.Close()
	wantPort, err := nextAvailablePort("5173")
	if err != nil {
		t.Fatal(err)
	}

	env, viteURL, _, err := resolveViteDevEnv([]string{"PATH=/bin"}, "")
	if err != nil {
		t.Fatal(err)
	}
	wantURL := "http://localhost:" + wantPort
	if viteURL != wantURL {
		t.Fatalf("viteURL = %q, want %s", viteURL, wantURL)
	}
	if got := envPort(env, "VITE_PORT", ""); got != wantPort {
		t.Fatalf("VITE_PORT = %q, want %s", got, wantPort)
	}
}

func TestResolveViteDevEnvRejectsBoundExplicitVitePort(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:5173")
	if err != nil {
		t.Skipf("default Vite port unavailable before test: %v", err)
	}
	defer l.Close()

	_, _, _, err = resolveViteDevEnv([]string{"VITE_PORT=5173"}, "")
	if err == nil {
		t.Fatal("expected bound explicit VITE_PORT to fail")
	}
	if !strings.Contains(err.Error(), "VITE_PORT 5173 is already in use") {
		t.Fatalf("error = %q, want port-in-use message", err)
	}
}

func TestWaitHealthy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	if !waitHealthy(context.Background(), srv.URL+"/healthz", 2*time.Second) {
		t.Error("expected healthy")
	}
	if waitHealthy(context.Background(), "http://127.0.0.1:1/healthz", 300*time.Millisecond) {
		t.Error("expected unhealthy (closed port)")
	}
}

func TestAggregateEvent(t *testing.T) {
	results := []cycleResult{
		{Dir: "a", Written: []string{"/x/a.x.go"}, OK: true, DurMs: 2},
		{Dir: "b", Written: []string{"/x/b.x.go"}, OK: true, DurMs: 3},
	}
	var ev map[string]any
	if err := json.Unmarshal(aggregateEvent(results), &ev); err != nil {
		t.Fatal(err)
	}
	if ev["event"] != "generated" || ev["ok"] != true {
		t.Errorf("bad event: %v", ev)
	}
	w, _ := json.Marshal(ev["written"])
	if !strings.Contains(string(w), "a.x.go") || !strings.Contains(string(w), "b.x.go") {
		t.Errorf("written missing: %s", w)
	}
}

// A cycle that fails with a hard operational error (r.Err set, no Diags — e.g.
// the "imports must appear before other declarations" skeleton error that
// Module.Generate returns as a plain error, not a parse-diagnostic) must not be
// dropped: the overlay event has to carry the message, or gsx dev fails silently
// (ok:false but empty diagnostics → blank Vite overlay). Mirrors watchemit's
// cycle() fallback for the same case.
func TestAggregateEventSurfacesHardError(t *testing.T) {
	results := []cycleResult{
		{Dir: "a", OK: false, Err: errors.New("app.gsx:3:1: imports must appear before other declarations")},
	}
	var ev map[string]any
	if err := json.Unmarshal(aggregateEvent(results), &ev); err != nil {
		t.Fatal(err)
	}
	if ev["ok"] != false {
		t.Fatalf("ok = %v, want false", ev["ok"])
	}
	diags, _ := json.Marshal(ev["diagnostics"])
	if !strings.Contains(string(diags), `"severity":"error"`) ||
		!strings.Contains(string(diags), "imports must appear before other declarations") {
		t.Errorf("overlay dropped the hard error: diagnostics=%s", diags)
	}
}

func TestReportHardErrors(t *testing.T) {
	var out bytes.Buffer
	reportHardErrors(&out, []cycleResult{
		{Dir: "a", OK: true, Written: []string{"a.x.go"}},
		{Dir: "b", OK: false, Err: errors.New("app.gsx:3:1: imports must appear before other declarations")},
		// A not-OK result whose failure IS a source diagnostic must not be echoed
		// here (it reaches the developer via the overlay); only hard errors do.
		{Dir: "c", OK: false, Diags: []diag.Diagnostic{{Message: "type error"}}},
	})
	got := out.String()
	if !strings.Contains(got, "imports must appear before other declarations") {
		t.Errorf("hard error not surfaced: %q", got)
	}
	if strings.Contains(got, "type error") {
		t.Errorf("diagnostic-backed failure should not be echoed here: %q", got)
	}
}

func TestStopBeforeStart(t *testing.T) {
	d := &devServer{}
	d.stop() // must not panic when no server was ever started
}

func TestBuildErrorEvent(t *testing.T) {
	var ev map[string]any
	if err := json.Unmarshal(buildErrorEvent("embedbad.go: pattern x: no matching files"), &ev); err != nil {
		t.Fatal(err)
	}
	if ev["event"] != "generated" || ev["ok"] != false {
		t.Fatalf("bad event: %v", ev)
	}
	diags, _ := json.Marshal(ev["diagnostics"])
	if !strings.Contains(string(diags), `"severity":"error"`) || !strings.Contains(string(diags), "no matching files") {
		t.Errorf("diagnostic missing error/message: %s", diags)
	}
}

func TestDevServerBuildsInDir(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a binary")
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain unavailable")
	}
	proj := t.TempDir()
	must := func(name, content string) {
		if err := os.WriteFile(filepath.Join(proj, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	must("go.mod", "module devdirtest\n\ngo 1.24\n")
	must("main.go", "package main\n\nfunc main() {}\n")
	bin := filepath.Join(t.TempDir(), "out")
	d := &devServer{
		dir:   proj,
		build: []string{"go", "build", "-o", bin, "."},
		run:   []string{bin},
		out:   io.Discard,
	}
	// rebuild must succeed even though the test's cwd is NOT proj.
	if out, err := d.rebuild(context.Background()); err != nil {
		t.Fatalf("rebuild in dir failed: %v\n%s", err, out)
	}
	d.stop()
	if _, err := os.Stat(bin); err != nil {
		t.Errorf("binary not built in project dir: %v", err)
	}
}

func TestPostEventReachesServer(t *testing.T) {
	got := make(chan string, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("/__gsx/event", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got <- string(b)
		w.WriteHeader(204)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	postEvent(srv.URL, []byte(`{"event":"generated","ok":true}`), nil)
	select {
	case b := <-got:
		if !strings.Contains(b, "generated") {
			t.Errorf("body = %s", b)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("postEvent did not reach server")
	}
}

func TestPortAvailableDetectsWildcardListener(t *testing.T) {
	// A dev server (vite binds *:PORT) must make the port unavailable. The
	// specific-address probes alone miss this on macOS: SO_REUSEADDR (set by
	// net.Listen) permits binding 127.0.0.1:PORT / [::1]:PORT alongside an
	// existing wildcard listener.
	l, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	_, port, err := net.SplitHostPort(l.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	if portAvailable(port) {
		t.Errorf("portAvailable(%s) = true while a wildcard listener holds the port", port)
	}
}

func TestBuildFailureMessagePrefersCompilerOutput(t *testing.T) {
	out := "# devdemo\nmain.go:3:1: undefined: x\n"
	if got := buildFailureMessage(out, errors.New("exit status 1")); got != out {
		t.Errorf("buildFailureMessage = %q, want compiler output %q", got, out)
	}
}

func TestBuildFailureMessageFallsBackToError(t *testing.T) {
	err := errors.New("fork/exec tmp/server: no such file or directory")
	if got := buildFailureMessage("  \n", err); got != err.Error() {
		t.Errorf("buildFailureMessage = %q, want %q", got, err.Error())
	}
}
