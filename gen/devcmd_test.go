package gen

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// cmdServer serves /__gsx/cmd from a queue of canned responses.
func cmdServer(t *testing.T, responses ...func(w http.ResponseWriter)) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/__gsx/cmd" {
			w.WriteHeader(404)
			return
		}
		n := int(calls.Add(1)) - 1
		if n < len(responses) {
			responses[n](w)
			return
		}
		w.WriteHeader(204) // idle long-poll afterwards
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

func respondCmds(cmds ...string) func(http.ResponseWriter) {
	return func(w http.ResponseWriter) {
		b, _ := json.Marshal(map[string]any{"cmds": cmds})
		w.Header().Set("Content-Type", "application/json")
		w.Write(b)
	}
}

func TestPollCommandsDeliversAndRepolls(t *testing.T) {
	srv, calls := cmdServer(t,
		respondCmds("rebuild"),
		func(w http.ResponseWriter) { w.WriteHeader(204) },
		respondCmds("restart-server", "rebuild"),
	)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out := make(chan string, 8)
	go pollCommands(ctx, srv.URL, func() bool { return true }, out)

	want := []string{"rebuild", "restart-server", "rebuild"}
	for i, w := range want {
		select {
		case got := <-out:
			if got != w {
				t.Fatalf("cmd[%d] = %q, want %q", i, got, w)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("timed out waiting for cmd[%d] (calls=%d)", i, calls.Load())
		}
	}
}

func TestPollCommandsSuspendedWhileGateDown(t *testing.T) {
	srv, calls := cmdServer(t, respondCmds("rebuild"))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out := make(chan string, 1)
	go pollCommands(ctx, srv.URL, func() bool { return false }, out)
	time.Sleep(600 * time.Millisecond)
	if calls.Load() != 0 {
		t.Errorf("polled %d times while gate down; want 0", calls.Load())
	}
}

func TestPollCommandsSurvivesServerDown(t *testing.T) {
	// Nothing listens at base: pollCommands must keep retrying with backoff,
	// not exit; and honor ctx cancellation promptly.
	ctx, cancel := context.WithCancel(context.Background())
	out := make(chan string, 1)
	done := make(chan struct{})
	go func() { pollCommands(ctx, "http://127.0.0.1:1", func() bool { return true }, out); close(done) }()
	time.Sleep(300 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("pollCommands did not return after ctx cancel")
	}
}
