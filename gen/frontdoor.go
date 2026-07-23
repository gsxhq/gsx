package gen

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"sync"
	"time"
)

// frontDoor owns the managed vite child: it monitors exits, respawns with
// backoff, verifies a respawn is really our plugin (x-gsx header) before
// reopening the push/poll gate, and gives up on a crash loop. The first
// instance opens the gate immediately (its port was vetted by portAvailable;
// postBest retries cover the cold start).
type frontDoor struct {
	spawn     func() (*exec.Cmd, error)
	verifyURL string
	onState   func(frontStat)
	logw      io.Writer

	mu        sync.Mutex
	cmd       *exec.Cmd
	exited    chan struct{} // current instance; closed once its Wait returns
	open      bool          // push/poll gate
	restarts  int
	done      bool          // given up or shut down
	shutdownC chan struct{} // closed by shutdown(); aborts backoff sleeps
}

// frontDoorRapidWindow: an instance that lived at least this long resets the
// rapid-exit counter. Also the boundary for "rapid".
const frontDoorRapidWindow = 30 * time.Second

// frontDoorVerifyWindow bounds how long a respawn may take to verify before it
// is killed and counted as a rapid exit.
const frontDoorVerifyWindow = 5 * time.Second

// restartPolicy maps consecutive rapid exits to the backoff before the next
// attempt, or giveUp.
func restartPolicy(rapidExits int) (time.Duration, bool) {
	switch rapidExits {
	case 0:
		return 500 * time.Millisecond, false
	case 1:
		return 2 * time.Second, false
	case 2:
		return 5 * time.Second, false
	}
	return 0, true
}

// verifyFrontDoor reports whether url is served by OUR vite plugin: the
// /__gsx/cmd endpoint stamps x-gsx: 1 on every response. A foreign listener
// (or vite's SPA fallback answering 200 for unknown paths) lacks it.
func verifyFrontDoor(ctx context.Context, url string) bool {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url+"/__gsx/cmd?wait=0", nil)
	if err != nil {
		return false
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.Header.Get("x-gsx") == "1"
}

func newFrontDoor(spawn func() (*exec.Cmd, error), verifyURL string, onState func(frontStat), logw io.Writer) *frontDoor {
	return &frontDoor{spawn: spawn, verifyURL: verifyURL, onState: onState, logw: logw, shutdownC: make(chan struct{})}
}

// start launches the first instance and its supervisor. The gate opens
// immediately (first-instance semantics).
func (f *frontDoor) start() error {
	cmd, err := f.spawn()
	if err != nil {
		return err
	}
	exited := make(chan struct{})
	f.mu.Lock()
	f.cmd, f.exited, f.open = cmd, exited, true
	f.mu.Unlock()
	go func() { _ = cmd.Wait(); close(exited) }()
	go f.run(exited, time.Now())
	return nil
}

func (f *frontDoor) up() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.open
}

// shutdown suppresses restarts and kills the current instance. Safe to call
// once; runDev calls it from shutdownProcesses.
func (f *frontDoor) shutdown(timeout time.Duration) {
	f.mu.Lock()
	if f.done {
		f.mu.Unlock()
		return
	}
	f.done = true
	f.open = false
	cmd, exited := f.cmd, f.exited
	close(f.shutdownC)
	f.mu.Unlock()
	if cmd != nil {
		killProcGroupOwned(cmd, exited, timeout)
	}
}

// run supervises instances for the frontDoor's lifetime: wait for the current
// instance to exit, apply the restart policy, respawn, verify, repeat — until
// verified instances keep living, give-up, or shutdown. Every path out of an
// iteration leaves open == false except verified-up. Each instance's Wait is
// owned by the small goroutine that closes its exited channel.
func (f *frontDoor) run(exited chan struct{}, started time.Time) {
	rapid := 0 // consecutive exits where the instance lived < frontDoorRapidWindow
	for {
		<-exited
		f.mu.Lock()
		if f.done {
			f.mu.Unlock()
			return
		}
		f.open = false
		f.mu.Unlock()
		if time.Since(started) < frontDoorRapidWindow {
			rapid++
		} else {
			rapid = 1
		}
		delay, giveUp := restartPolicy(rapid - 1)
		if giveUp {
			f.giveUp()
			return
		}
		f.transition(frontStat{State: "restarting", Restarts: f.restartCount()})
		fmt.Fprintf(f.logw, "gsx dev: front door exited — restarting in %s\n", delay)
		select {
		case <-f.shutdownC:
			return
		case <-time.After(delay):
		}
		next, err := f.spawn()
		if err != nil {
			fmt.Fprintf(f.logw, "gsx dev: front door restart failed: %v\n", err)
			// No instance to wait on: synthesize an immediate rapid exit.
			exited = closedChan()
			started = time.Now()
			continue
		}
		nextExited := make(chan struct{})
		go func(c *exec.Cmd, ch chan struct{}) { _ = c.Wait(); close(ch) }(next, nextExited)
		f.mu.Lock()
		if f.done {
			f.mu.Unlock()
			killProcGroupOwned(next, nextExited, 2*time.Second)
			return
		}
		f.cmd, f.exited = next, nextExited
		f.restarts++
		f.mu.Unlock()
		exited, started = nextExited, time.Now()
		if f.verifyRespawn(nextExited) {
			f.mu.Lock()
			ok := !f.done
			f.open = ok
			f.mu.Unlock()
			if ok {
				f.transition(frontStat{State: "up", Restarts: f.restartCount()})
			}
		} else {
			// Never verified (foreign port owner, drifted port, or died during
			// the window): kill it; the loop's <-exited counts it as rapid.
			fmt.Fprintln(f.logw, "gsx dev: restarted front door did not verify (no x-gsx endpoint at its URL) — killing it")
			f.mu.Lock()
			cur, curExited := f.cmd, f.exited
			f.mu.Unlock()
			killProcGroupOwned(cur, curExited, 2*time.Second)
		}
	}
}

func closedChan() chan struct{} {
	ch := make(chan struct{})
	close(ch)
	return ch
}

func (f *frontDoor) restartCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.restarts
}

// giveUp declares the crash loop lost and suppresses further restarts. It
// checks-and-sets f.done under the lock so a concurrent shutdown() that wins
// the race is authoritative: shutdown must never be followed by a spurious
// given-up notice (onState is documented to never fire for shutdown).
func (f *frontDoor) giveUp() {
	f.mu.Lock()
	if f.done {
		f.mu.Unlock()
		return
	}
	f.done = true
	f.open = false
	f.mu.Unlock()
	fmt.Fprintln(f.logw, "gsx dev: front door exited — giving up after repeated failures; suspending browser reload/overlay pushes")
	f.transition(frontStat{State: "given-up", Restarts: f.restartCount()})
}

// verifyRespawn polls verifyFrontDoor until success, instance exit, shutdown,
// or the verify window elapses.
func (f *frontDoor) verifyRespawn(exited <-chan struct{}) bool {
	deadline := time.Now().Add(frontDoorVerifyWindow)
	for time.Now().Before(deadline) {
		select {
		case <-f.shutdownC:
			return false
		case <-exited:
			return false
		default:
		}
		if verifyFrontDoor(context.Background(), f.verifyURL) {
			return true
		}
		time.Sleep(250 * time.Millisecond)
	}
	return false
}

func (f *frontDoor) transition(s frontStat) {
	if f.onState != nil {
		f.onState(s)
	}
}
