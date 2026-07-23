//go:build windows

package gen

import (
	"os/exec"
	"strconv"
	"syscall"
	"time"
)

// setProcGroup starts the child in a new process group so it (and its children)
// can be terminated as a tree via taskkill /T.
func setProcGroup(c *exec.Cmd) {
	c.SysProcAttr = &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
}

// killProcGroup terminates the child's whole tree with `taskkill /T`, escalating
// to `/F` after timeout, and waits for the child to be reaped. (Job objects are
// a future hardening; taskkill /T /F is the v1 implementation.)
// Callers must not call c.Wait() after killProcGroup — this function takes sole ownership of waiting for (and reaping) c.
func killProcGroup(c *exec.Cmd, timeout time.Duration) {
	if c == nil || c.Process == nil {
		return
	}
	done := make(chan struct{})
	go func() { _ = c.Wait(); close(done) }()
	killProcGroupOwned(c, done, timeout)
}

// killProcGroupOwned is killProcGroup for a child whose Wait is owned by an
// external monitor goroutine: done must be closed once that Wait has returned.
// It never calls c.Wait itself.
func killProcGroupOwned(c *exec.Cmd, done <-chan struct{}, timeout time.Duration) {
	if c == nil || c.Process == nil {
		return
	}
	select {
	case <-done:
		// Already exited and reaped by the owning monitor: the pid may have
		// been recycled to an unrelated process — do not signal it.
		return
	default:
	}
	pid := strconv.Itoa(c.Process.Pid)
	_ = exec.Command("taskkill", "/T", "/PID", pid).Run()
	t := time.NewTimer(timeout)
	defer t.Stop()
	select {
	case <-done:
	case <-t.C:
		_ = exec.Command("taskkill", "/T", "/F", "/PID", pid).Run()
		<-done
	}
}
