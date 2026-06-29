package gen

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
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
