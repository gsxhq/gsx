package gen

import (
	"bytes"
	"context"
	"encoding/json"
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

	env, viteURL, err := resolveViteDevEnv([]string{"PATH=/bin"})
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

	env, viteURL, err := resolveViteDevEnv([]string{"PATH=/bin"})
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

	_, _, err = resolveViteDevEnv([]string{"VITE_PORT=5173"})
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
	postEvent(srv.URL, []byte(`{"event":"generated","ok":true}`))
	select {
	case b := <-got:
		if !strings.Contains(b, "generated") {
			t.Errorf("body = %s", b)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("postEvent did not reach server")
	}
}
