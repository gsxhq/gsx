// gen/frontdoor_test.go
package gen

import (
	"context"
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

func TestRestartPolicy(t *testing.T) {
	cases := []struct {
		rapid  int
		delay  time.Duration
		giveUp bool
	}{{0, 500 * time.Millisecond, false}, {1, 2 * time.Second, false}, {2, 5 * time.Second, false}, {3, 0, true}, {7, 0, true}}
	for _, c := range cases {
		d, g := restartPolicy(c.rapid)
		if d != c.delay || g != c.giveUp {
			t.Errorf("restartPolicy(%d) = %v,%v want %v,%v", c.rapid, d, g, c.delay, c.giveUp)
		}
	}
}

func TestVerifyFrontDoor(t *testing.T) {
	ours := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("x-gsx", "1")
		w.WriteHeader(204)
	}))
	defer ours.Close()
	foreign := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200) // SPA fallback: 200 without the header
	}))
	defer foreign.Close()
	if !verifyFrontDoor(context.Background(), ours.URL) {
		t.Error("our plugin endpoint must verify")
	}
	if verifyFrontDoor(context.Background(), foreign.URL) {
		t.Error("foreign 200 without x-gsx must NOT verify")
	}
	if verifyFrontDoor(context.Background(), "http://127.0.0.1:1") {
		t.Error("connection refused must NOT verify")
	}
}

// stateRecorder collects onState transitions.
type stateRecorder struct {
	mu     sync.Mutex
	states []frontStat
}

func (r *stateRecorder) record(s frontStat) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.states = append(r.states, s)
}
func (r *stateRecorder) list() []frontStat {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]frontStat(nil), r.states...)
}
func (r *stateRecorder) waitFor(t *testing.T, state string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		for _, s := range r.list() {
			if s.State == state {
				return
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("state %q never reached; got %+v", state, r.list())
}

// TestFrontDoorRestartsAndVerifies: the child exits once, the respawn stays up
// and the verify URL answers with x-gsx — the gate must reopen.
func TestFrontDoorRestartsAndVerifies(t *testing.T) {
	verify := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("x-gsx", "1")
		w.WriteHeader(204)
	}))
	defer verify.Close()

	dir := t.TempDir()
	marker := filepath.Join(dir, "runs")
	// First run appends a line and exits; later runs append and sleep.
	script := "echo run >> " + marker + "; n=$(wc -l < " + marker + "); [ $n -ge 2 ] && sleep 60; exit 0"
	rec := &stateRecorder{}
	fd := newFrontDoor(func() (*exec.Cmd, error) {
		c := exec.Command("sh", "-c", script)
		setProcGroup(c)
		return c, c.Start()
	}, verify.URL, rec.record, os.Stderr)
	if err := fd.start(); err != nil {
		t.Fatal(err)
	}
	defer fd.shutdown(5 * time.Second)

	if !fd.up() {
		t.Fatal("gate must be open for the first instance")
	}
	rec.waitFor(t, "restarting", 10*time.Second)
	rec.waitFor(t, "up", 10*time.Second) // respawn verified against verify.URL
	if !fd.up() {
		t.Error("gate must reopen after a verified respawn")
	}
	// The respawn's "echo run >> marker" is a freshly forked shell process
	// racing an in-process HTTP round trip to the (already-listening) verify
	// server: "up" can legitimately fire a beat before the fork is scheduled
	// to run its first line. Poll instead of a single read — this is eventual
	// consistency in the test's own side channel, not something frontDoor
	// promises to order against onState.
	deadline := time.Now().Add(5 * time.Second)
	var lines []string
	for time.Now().Before(deadline) {
		b, _ := os.ReadFile(marker)
		lines = splitLines(string(b))
		if len(lines) >= 2 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if len(lines) < 2 {
		t.Errorf("child ran %d times; want >= 2 (respawn)", len(lines))
	}
}

// TestFrontDoorGivesUpOnCrashLoop: child always exits immediately and nothing
// verifies — after 3 rapid exits the manager gives up and the gate stays shut.
func TestFrontDoorGivesUpOnCrashLoop(t *testing.T) {
	rec := &stateRecorder{}
	fd := newFrontDoor(func() (*exec.Cmd, error) {
		c := exec.Command("sh", "-c", "exit 0")
		setProcGroup(c)
		return c, c.Start()
	}, "http://127.0.0.1:1", rec.record, os.Stderr)
	if err := fd.start(); err != nil {
		t.Fatal(err)
	}
	defer fd.shutdown(time.Second)
	rec.waitFor(t, "given-up", 30*time.Second)
	if fd.up() {
		t.Error("gate must stay shut after give-up")
	}
}

// TestFrontDoorShutdownSilent: intentional shutdown must produce no
// restarting/given-up transitions.
func TestFrontDoorShutdownSilent(t *testing.T) {
	rec := &stateRecorder{}
	fd := newFrontDoor(func() (*exec.Cmd, error) {
		c := exec.Command("sleep", "60")
		setProcGroup(c)
		return c, c.Start()
	}, "http://127.0.0.1:1", rec.record, os.Stderr)
	if err := fd.start(); err != nil {
		t.Fatal(err)
	}
	fd.shutdown(5 * time.Second)
	time.Sleep(200 * time.Millisecond)
	for _, s := range rec.list() {
		if s.State == "restarting" || s.State == "given-up" {
			t.Errorf("shutdown produced transition %+v", s)
		}
	}
	if fd.up() {
		t.Error("gate must be shut after shutdown")
	}
}

func splitLines(s string) []string {
	var out []string
	for l := range strings.SplitSeq(s, "\n") {
		if l != "" {
			out = append(out, l)
		}
	}
	return out
}
