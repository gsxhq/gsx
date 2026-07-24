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
	ctx := t.Context()
	out := make(chan string, 8)
	go pollCommands(ctx, srv.URL, "tok", func() bool { return true }, out, nil)

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
	ctx := t.Context()
	out := make(chan string, 1)
	go pollCommands(ctx, srv.URL, "tok", func() bool { return false }, out, nil)
	time.Sleep(600 * time.Millisecond)
	if calls.Load() != 0 {
		t.Errorf("polled %d times while gate down; want 0", calls.Load())
	}
}

func TestPollCommandsSurvivesServerDown(t *testing.T) {
	// Nothing listens at base: pollCommands must keep retrying with backoff,
	// not exit, and must return promptly on ctx cancellation even while
	// blocked inside a backoff sleep. Sleeping 2.5s lets the backoff climb
	// past the 1s tier into the 2s tier (1s -> 2s escalation) before we
	// cancel, so this pins ctx-aware sleeping: a naive time.Sleep(backoff)
	// implementation (ignoring ctx) would still be blocked well past the
	// 500ms deadline below.
	ctx, cancel := context.WithCancel(context.Background())
	out := make(chan string, 1)
	done := make(chan struct{})
	go func() {
		pollCommands(ctx, "http://127.0.0.1:1", "tok", func() bool { return true }, out, nil)
		close(done)
	}()
	time.Sleep(2500 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("pollCommands did not return within 500ms of ctx cancel (stuck in a non-ctx-aware backoff sleep?)")
	}
}

// TestPollCommandsSendsTokenHeader proves every request — the initial poll
// and subsequent re-polls — carries x-gsx-token: <token>, so a tokened
// front-door plugin can authenticate the mailbox drain.
func TestPollCommandsSendsTokenHeader(t *testing.T) {
	var seen atomic.Int32
	var mismatched atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen.Add(1)
		if r.Header.Get("x-gsx-token") != "secret-token" {
			mismatched.Store(true)
		}
		w.WriteHeader(204)
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	out := make(chan string, 1)
	done := make(chan struct{})
	go func() { pollCommands(ctx, srv.URL, "secret-token", func() bool { return true }, out, nil); close(done) }()
	time.Sleep(400 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("pollCommands did not return after ctx cancel")
	}

	if seen.Load() == 0 {
		t.Fatal("server never received a request")
	}
	if mismatched.Load() {
		t.Error("at least one request was missing x-gsx-token or had the wrong value")
	}
}

// TestPollCommandsBackoffOnErrorResponses proves pollCommands throttles on
// every kind of non-204 failure: HTTP error statuses (e.g. 500) and a 200
// whose body isn't valid JSON. Neither must busy-spin; both must escalate
// backoff exactly like a transport error.
func TestPollCommandsBackoffOnErrorResponses(t *testing.T) {
	tests := []struct {
		name    string
		handler http.HandlerFunc
	}{
		{
			name: "500 status",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusInternalServerError)
			},
		},
		{
			name: "200 with malformed JSON body",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte("not json"))
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				tc.handler(w, r)
			}))
			defer srv.Close()

			ctx, cancel := context.WithCancel(context.Background())
			out := make(chan string, 1)
			done := make(chan struct{})
			go func() { pollCommands(ctx, srv.URL, "tok", func() bool { return true }, out, nil); close(done) }()

			time.Sleep(1200 * time.Millisecond)
			cancel()
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				t.Fatal("pollCommands did not return after ctx cancel")
			}

			// Correct backoff (1s, then 2s, ...) yields roughly 2-3 requests
			// in 1.2s: one immediately, one after the first 1s sleep. A
			// busy-spinning implementation makes thousands.
			if n := calls.Load(); n > 4 {
				t.Errorf("polled %d times in 1.2s against a %s server; want <= 4 (busy-spin, no backoff applied)", n, tc.name)
			}
		})
	}
}

// TestPollCommandsOnContactFiresOnceOnFirstSuccess proves onContact fires
// exactly once, on the first successful response (200 or 204 — the same
// paths that already reset backoff), and is NOT invoked for the failing
// attempts (500, malformed-JSON 200) that precede it, nor again on any
// later success.
func TestPollCommandsOnContactFiresOnceOnFirstSuccess(t *testing.T) {
	srv, calls := cmdServer(t,
		func(w http.ResponseWriter) { w.WriteHeader(http.StatusInternalServerError) },
		// A token-mismatch 403 (foreign tokened plugin) must not count as contact.
		func(w http.ResponseWriter) { w.WriteHeader(http.StatusForbidden) },
		func(w http.ResponseWriter) { w.WriteHeader(http.StatusOK); _, _ = w.Write([]byte("not json")) },
		func(w http.ResponseWriter) { w.WriteHeader(204) }, // first success
		respondCmds("rebuild"), // later success
		func(w http.ResponseWriter) { w.WriteHeader(204) },
	)
	ctx, cancel := context.WithCancel(context.Background())
	out := make(chan string, 4)
	var contacted atomic.Int32
	done := make(chan struct{})
	go func() {
		pollCommands(ctx, srv.URL, "tok", func() bool { return true }, out, func() { contacted.Add(1) })
		close(done)
	}()

	select {
	case got := <-out:
		if got != "rebuild" {
			t.Fatalf("cmd = %q, want %q", got, "rebuild")
		}
	case <-time.After(15 * time.Second):
		// Three failing attempts back off 1s+2s+4s before the first success.
		t.Fatalf("timed out waiting for the rebuild cmd (calls=%d)", calls.Load())
	}
	// Give the loop a moment to also process the trailing 204 so a
	// double-fire on a later success would have had its chance to happen.
	time.Sleep(300 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("pollCommands did not return after ctx cancel")
	}

	if n := contacted.Load(); n != 1 {
		t.Errorf("onContact called %d times, want exactly 1", n)
	}
}

// TestPollCommandsOnContactNotCalledOnTransportError proves onContact never
// fires while every attempt is a connection-refused transport error (no
// success has ever occurred).
func TestPollCommandsOnContactNotCalledOnTransportError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	out := make(chan string, 1)
	var contacted atomic.Int32
	done := make(chan struct{})
	go func() {
		pollCommands(ctx, "http://127.0.0.1:1", "tok", func() bool { return true }, out, func() { contacted.Add(1) })
		close(done)
	}()
	time.Sleep(2500 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("pollCommands did not return within 500ms of ctx cancel")
	}

	if n := contacted.Load(); n != 0 {
		t.Errorf("onContact called %d times against an always-refused server, want 0", n)
	}
}

// TestPollCommandsOnContactNilSafe proves a nil onContact is safe: pollCommands
// must keep delivering commands normally without panicking.
func TestPollCommandsOnContactNilSafe(t *testing.T) {
	srv, calls := cmdServer(t,
		respondCmds("rebuild"),
		func(w http.ResponseWriter) { w.WriteHeader(204) },
	)
	ctx := t.Context()
	out := make(chan string, 8)
	go pollCommands(ctx, srv.URL, "tok", func() bool { return true }, out, nil)

	select {
	case got := <-out:
		if got != "rebuild" {
			t.Fatalf("cmd = %q, want %q", got, "rebuild")
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for cmd (calls=%d)", calls.Load())
	}
}
