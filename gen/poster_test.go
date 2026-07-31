package gen

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// TestPosterDeliversAndDrainReturns proves the happy path: a post reaches the
// server (delivery is asynchronous, so the test waits for it rather than for
// drain — drain called concurrently with a not-yet-started attempt may
// legitimately abandon it), and a subsequent drain neither blocks nor
// duplicates the delivery.
func TestPosterDeliversAndDrainReturns(t *testing.T) {
	t.Parallel()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
	}))
	defer srv.Close()

	p := newPoster(context.Background())
	p.post(srv.URL+"/__gsx/event", []byte(`{}`), nil)
	deadline := time.Now().Add(3 * time.Second)
	for hits.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := hits.Load(); got != 1 {
		t.Fatalf("delivered %d posts before drain, want 1", got)
	}
	p.drain()
	if got := hits.Load(); got != 1 {
		t.Fatalf("delivered %d posts after drain, want exactly 1", got)
	}
}

// TestPosterDrainAbandonsRetries proves shutdown is bounded: a post whose
// target never answers is retrying when drain is called; drain must abandon the
// remaining retries and return promptly instead of sitting out the full
// 10x150ms retry window plus client timeout.
func TestPosterDrainAbandonsRetries(t *testing.T) {
	t.Parallel()
	p := newPoster(context.Background())
	// 127.0.0.1:1 refuses connections immediately, so the goroutine sits in the
	// retry/backoff loop, not in a connect timeout.
	p.post("http://127.0.0.1:1/__gsx/event", []byte(`{}`), nil)
	time.Sleep(50 * time.Millisecond) // let it enter the retry loop

	start := time.Now()
	p.drain()
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Fatalf("drain took %v, want prompt abandonment of pending retries", elapsed)
	}
}

// TestPosterDrainAbortsInFlightRequest proves drain cancels a request that is
// mid-flight against a hung server, not just future retries.
func TestPosterDrainAbortsInFlightRequest(t *testing.T) {
	t.Parallel()
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release // hang until the test finishes
	}))
	defer func() { close(release); srv.Close() }()

	p := newPoster(context.Background())
	p.post(srv.URL+"/__gsx/event", []byte(`{}`), nil)
	time.Sleep(50 * time.Millisecond) // let the request reach the handler

	start := time.Now()
	p.drain()
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Fatalf("drain took %v, want in-flight request aborted", elapsed)
	}
}

// TestPosterNoAttemptAfterDrain pins the guarantee runDev's teardown relies
// on: once drain has returned, no NEW request is ever initiated. (A request the
// server has already received cannot be un-received — the assertion counts
// attempt initiations at handler entry.) Regression guard for the
// unjoined-goroutine shape this replaced, where a fire-and-forget retry loop
// could keep initiating posts into a port a stranger had since rebound.
func TestPosterNoAttemptAfterDrain(t *testing.T) {
	t.Parallel()
	var attempts atomic.Int32
	blocked := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1) // counted at ENTRY: an initiation, not a completion
		<-blocked
	}))
	defer func() { close(blocked); srv.Close() }()

	p := newPoster(context.Background())
	p.post(srv.URL+"/__gsx/event", []byte(`{}`), nil)
	deadline := time.Now().Add(3 * time.Second)
	for attempts.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	p.drain()
	before := attempts.Load()
	time.Sleep(400 * time.Millisecond) // past two retry periods
	if got := attempts.Load(); got != before {
		t.Fatalf("attempts rose from %d to %d after drain returned, want no new initiations", before, got)
	}
}

// TestPosterGateStopsAttempts keeps the existing gate contract: a false gate
// suppresses delivery entirely.
func TestPosterGateStopsAttempts(t *testing.T) {
	t.Parallel()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
	}))
	defer srv.Close()

	p := newPoster(context.Background())
	p.post(srv.URL+"/__gsx/event", []byte(`{}`), func() bool { return false })
	p.drain()
	if got := hits.Load(); got != 0 {
		t.Fatalf("gated post delivered %d times, want 0", got)
	}
}
