package gen

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"sync"
	"time"
)

// statusSender serializes status POSTs through a single sender goroutine
// consuming a 1-slot latest-wins mailbox. Overlay (aggregateEvent/
// buildErrorEvent) and reload posts keep their existing fire-and-forget
// postBest path — only status posts go through here.
//
// Statuses are snapshots where the latest always wins, so deposit()
// overwrites any undelivered previous post outright rather than queuing
// behind it: a busy backlog coalesces down to whatever was last deposited
// before the sender gets to it, and the sender delivers every deposit when
// it's keeping up (nothing to coalesce). This is the fix for final-review
// finding I1: postBest's one-goroutine-per-post design has no ordering
// guarantee at all — Go's scheduler runs the most-recently-spawned goroutine
// first (the runnext slot), so back-to-back status posts arrived inverted far
// more often than not (measured: 86/150 back-to-back, 12/12 under a retry
// backlog).
//
// deposit is safe to call from any goroutine and never blocks. The sender
// goroutine exits when ctx is done — no leak across shutdown.
type statusSender struct {
	mu      sync.Mutex
	pending *statusPost
	wake    chan struct{}
	wg      sync.WaitGroup
}

// statusPost is one undelivered status post: the full target URL (base +
// /__gsx/event, already joined at deposit time so a later base-URL change —
// e.g. an .env-triggered Vite port change — never rewrites an
// already-queued post), its marshaled body, and the gate to re-check before
// every retry attempt.
type statusPost struct {
	url  string
	body []byte
	gate func() bool
}

// newStatusSender starts the sender goroutine and returns immediately.
func newStatusSender(ctx context.Context) *statusSender {
	s := &statusSender{wake: make(chan struct{}, 1)}
	s.wg.Add(1)
	go s.run(ctx)
	return s
}

// deposit overwrites any undelivered previous status with (url, body, gate)
// and wakes the sender. url == "" or a bare path (no scheme) no-ops,
// matching postBest's same-origin/disabled convention.
func (s *statusSender) deposit(url string, body []byte, gate func() bool) {
	if strings.HasPrefix(url, "/") || url == "" {
		return
	}
	s.mu.Lock()
	s.pending = &statusPost{url: url, body: body, gate: gate}
	s.mu.Unlock()
	select {
	case s.wake <- struct{}{}:
	default: // sender already woken/busy; it'll see the new pending itself
	}
}

func (s *statusSender) run(ctx context.Context) {
	defer s.wg.Done()
	client := devHTTPClient(2 * time.Second)
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.wake:
		}
		for p := s.takePending(); p != nil; p = s.takePending() {
			if !s.deliver(ctx, client, p) {
				return
			}
		}
	}
}

// takePending atomically removes and returns the mailbox's current content
// (nil if empty).
func (s *statusSender) takePending() *statusPost {
	s.mu.Lock()
	p := s.pending
	s.pending = nil
	s.mu.Unlock()
	return p
}

// deliver mirrors postBest's retry/backoff/gate semantics (up to 10 attempts,
// 150ms backoff, gate re-checked before every attempt) but runs
// synchronously in the sender goroutine rather than spawning a new one for
// every post — that's the whole point of statusSender: exactly one goroutine
// ever sends a status, so there is nothing left to reorder.
//
// A fresher deposit landing during the backoff sleep pre-empts p outright:
// the stale in-flight target is abandoned unsent and delivery continues on
// the fresher value with its own full 10-attempt budget. This is what makes
// the sender immune to the retry-backlog hazard (final-review I1, "12/12
// inverted" cold-start probe) — a stale non-idle status retrying against a
// front door that isn't up yet can never win a race against a newer one by
// having been queued first; the newest deposit always pre-empts.
//
// Returns false only when ctx is done (the caller should stop entirely).
func (s *statusSender) deliver(ctx context.Context, client *http.Client, p *statusPost) bool {
	attempts := 0
	for attempts < 10 {
		if p.gate != nil && !p.gate() {
			return true // gate closed: drop
		}
		resp, err := client.Post(p.url, "application/json", bytes.NewReader(p.body))
		if err == nil {
			resp.Body.Close()
			return true // delivered
		}
		attempts++
		select {
		case <-ctx.Done():
			return false
		case <-time.After(150 * time.Millisecond):
		}
		if fresh := s.takePending(); fresh != nil {
			p = fresh
			attempts = 0 // the fresher target gets its own full budget
		}
	}
	return true // exhausted retries; drop p
}
