package gen

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"
)

// TestStatusSenderDeliversDepositsInOrder is the final-review I1 ordering
// probe (reviewer's shape: rapid-fire N deposits against a real HTTP server):
// with the old one-goroutine-per-post postBest, Go's scheduler runs the
// most-recently-spawned goroutine first (the runnext slot), so back-to-back
// status posts arrived inverted far more often than not (86/150 in the
// reviewer's probe). The ordered sender must never invert: every delivered
// value must be strictly increasing (the 1-slot mailbox coalesces a busy
// backlog to a suffix, never reorders it), and the very last thing delivered
// must be the last thing deposited — nothing arrives after it.
func TestStatusSenderDeliversDepositsInOrder(t *testing.T) {
	var mu sync.Mutex
	var received []int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		n, _ := strconv.Atoi(string(b))
		mu.Lock()
		received = append(received, n)
		mu.Unlock()
		w.WriteHeader(204)
	}))
	defer srv.Close()

	sender := newStatusSender(t.Context())

	const n = 150
	for i := 1; i <= n; i++ {
		sender.deposit(srv.URL+"/__gsx/event", []byte(strconv.Itoa(i)), nil)
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		done := len(received) > 0 && received[len(received)-1] == n
		mu.Unlock()
		if done {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	// Give any (incorrect) trailing post a moment to arrive, so a bug that
	// delivers something after the last deposit is actually caught rather
	// than raced past.
	time.Sleep(200 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	if len(received) == 0 {
		t.Fatal("no status delivered")
	}
	if got := received[len(received)-1]; got != n {
		t.Fatalf("final received = %d, want %d (the last deposited); full sequence: %v", got, n, received)
	}
	for i := 1; i < len(received); i++ {
		if received[i] <= received[i-1] {
			t.Fatalf("received out of order at index %d: %v", i, received)
		}
	}
}

// TestStatusSenderRetryBacklogDeliversLatestAtBindTime is the final-review I1
// retry-backlog probe: the front door (Vite) can bind well after gsx dev has
// already deposited several statuses into a dead address. Nothing can
// possibly be delivered before the bind (the address refuses every
// connection), so whatever the sender is retrying at bind time is entirely
// superseded by the rest of the rapid-fire burst (see deliver's supersede
// check) long before the bind happens. The first thing the newly-bound
// server ever receives must be the latest deposit, not one of the
// intermediate stale ones — this is the fix for the reviewer's "12/12
// inverted" cold-start probe (a stale non-idle phase landing after the
// terminal idle repost).
func TestStatusSenderRetryBacklogDeliversLatestAtBindTime(t *testing.T) {
	port := freePort(t)
	url := "http://127.0.0.1:" + port + "/__gsx/event"

	sender := newStatusSender(t.Context())

	// Nothing is listening yet. The first deposit is picked up immediately
	// and the sender starts retrying it against a refused connection; the
	// rest land within microseconds, well inside that first attempt's 150ms
	// backoff, so the very next backoff check supersedes to "5" long before
	// anything binds.
	for i := 1; i <= 5; i++ {
		sender.deposit(url, []byte(strconv.Itoa(i)), nil)
	}

	// Bind well before any retry budget could exhaust (10 attempts x 150ms
	// ~= 1.5s from whenever "5" took over, itself within ~150ms of the
	// burst) so the race is "does the pre-empted stale target ever reach the
	// wire", not "did the sender give up first".
	time.Sleep(300 * time.Millisecond)

	var mu sync.Mutex
	var received []int
	mux := http.NewServeMux()
	mux.HandleFunc("/__gsx/event", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		n, _ := strconv.Atoi(string(b))
		mu.Lock()
		received = append(received, n)
		mu.Unlock()
		w.WriteHeader(204)
	})
	l, err := net.Listen("tcp", "127.0.0.1:"+port)
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = http.Serve(l, mux) }()
	defer l.Close()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		got := len(received)
		mu.Unlock()
		if got > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(received) == 0 {
		t.Fatal("no status delivered after bind")
	}
	if received[0] != 5 {
		t.Errorf("first delivered = %d, want 5 (latest at bind time), not a stale queued one; full sequence: %v", received[0], received)
	}
}

// TestStatusSenderExitsOnContextDone pins the sender's shutdown lifecycle: no
// goroutine leak once ctx is cancelled (e.g. gsx dev shutting down).
func TestStatusSenderExitsOnContextDone(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	sender := newStatusSender(ctx)
	sender.deposit("http://127.0.0.1:1/__gsx/event", []byte("x"), nil) // unreachable; retries briefly then gives up
	cancel()

	done := make(chan struct{})
	go func() {
		sender.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("statusSender goroutine did not exit after ctx cancellation")
	}
}
