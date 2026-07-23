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

	env, viteURL, err := resolveViteDevEnv([]string{"PATH=/bin"}, "")
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
	if envValue(env, "VITE_DEV_URL", "") != viteURL {
		t.Fatalf("VITE_DEV_URL env and returned URL differ: env=%q url=%q", envValue(env, "VITE_DEV_URL", ""), viteURL)
	}
}

func TestResolveViteDevEnvHost(t *testing.T) {
	env, viteURL, err := resolveViteDevEnv([]string{"VITE_PORT=0", "PATH=/bin"}, "mstudio")
	if err != nil {
		// port 0 is "available" (ephemeral); if the platform rejects it, fall back.
		env, viteURL, err = resolveViteDevEnv([]string{"PATH=/bin"}, "mstudio")
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
	// With no [dev].host, the hostname comes from VITE_DEV_URL in the env.
	_, viteURL, err := resolveViteDevEnv([]string{"VITE_DEV_URL=http://mstudio:4000", "PATH=/bin"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(viteURL, "http://mstudio:") {
		t.Fatalf("viteURL = %q, want host mstudio from VITE_DEV_URL", viteURL)
	}
	// An explicit [dev].host wins over VITE_DEV_URL.
	_, viteURL2, err := resolveViteDevEnv([]string{"VITE_DEV_URL=http://mstudio:4000", "PATH=/bin"}, "override")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(viteURL2, "http://override:") {
		t.Fatalf("viteURL = %q, want [dev].host override to win", viteURL2)
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

	env, viteURL, err := resolveViteDevEnv([]string{"PATH=/bin"}, "")
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

	_, _, err = resolveViteDevEnv([]string{"VITE_PORT=5173"}, "")
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
