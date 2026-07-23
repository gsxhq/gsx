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

// TestGenDevTokenFormatAndUniqueness pins the shape runDev relies on: a
// 16-byte value hex-encoded (32 lowercase hex chars), fresh each call.
func TestGenDevTokenFormatAndUniqueness(t *testing.T) {
	a, err := genDevToken()
	if err != nil {
		t.Fatalf("genDevToken: %v", err)
	}
	if len(a) != 32 {
		t.Errorf("len(token) = %d, want 32 (16 bytes hex-encoded)", len(a))
	}
	for _, r := range a {
		if !strings.Contains("0123456789abcdef", string(r)) {
			t.Fatalf("token %q has non-hex-lowercase rune %q", a, r)
		}
	}
	b, err := genDevToken()
	if err != nil {
		t.Fatalf("genDevToken: %v", err)
	}
	if a == b {
		t.Error("two calls to genDevToken produced the same token")
	}
}

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

// TestVerifyFrontDoor pins the x-gsx-token pairing contract: verifyFrontDoor
// sends the request header x-gsx-token and requires the response's x-gsx
// header to EQUAL the token — not just be present. A tokened plugin echoes
// the token only when it saw the matching request header; an older plugin
// (predating GSX_DEV_TOKEN) always echoes the literal "1" and must therefore
// fail verification against any real token (that mismatch is exactly how a
// foreign gsx dev's respawn verification correctly fails against our front
// door, and vice versa — see the compat note in frontdoor.go).
func TestVerifyFrontDoor(t *testing.T) {
	const token = "abc123deadbeef"

	tokened := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-gsx-token") == token {
			w.Header().Set("x-gsx", token)
		} else {
			w.Header().Set("x-gsx", "1") // no/wrong request token: untokened-plugin fallback
		}
		w.WriteHeader(204)
	}))
	defer tokened.Close()
	oldPlugin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("x-gsx", "1") // pre-token plugin: always "1", ignores request headers
		w.WriteHeader(204)
	}))
	defer oldPlugin.Close()
	wrongToken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("x-gsx", "some-other-processes-token")
		w.WriteHeader(204)
	}))
	defer wrongToken.Close()
	foreign := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200) // SPA fallback: 200 without the header
	}))
	defer foreign.Close()

	if !verifyFrontDoor(context.Background(), tokened.URL, token) {
		t.Error("plugin echoing our exact token must verify")
	}
	if verifyFrontDoor(context.Background(), foreign.URL, "") {
		t.Error("an empty token must never verify — a header-less response echoes \"\" and would match")
	}
	if verifyFrontDoor(context.Background(), oldPlugin.URL, token) {
		t.Error("older plugin always echoing \"1\" must NOT verify against a real token")
	}
	if verifyFrontDoor(context.Background(), wrongToken.URL, token) {
		t.Error("a different process's token must NOT verify")
	}
	if verifyFrontDoor(context.Background(), foreign.URL, token) {
		t.Error("foreign 200 without x-gsx must NOT verify")
	}
	if verifyFrontDoor(context.Background(), "http://127.0.0.1:1", token) {
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
	const token = "restart-verify-token"
	verify := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-gsx-token") == token {
			w.Header().Set("x-gsx", token)
		} else {
			w.Header().Set("x-gsx", "1")
		}
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
	}, verify.URL, token, rec.record, os.Stderr)
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
	}, "http://127.0.0.1:1", "crash-loop-token", rec.record, os.Stderr)
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
	}, "http://127.0.0.1:1", "shutdown-silent-token", rec.record, os.Stderr)
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
