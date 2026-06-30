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
	pid := strconv.Itoa(c.Process.Pid)
	_ = exec.Command("taskkill", "/T", "/PID", pid).Run()
	done := make(chan struct{})
	go func() { _ = c.Wait(); close(done) }()
	t := time.NewTimer(timeout)
	defer t.Stop()
	select {
	case <-done:
	case <-t.C:
		_ = exec.Command("taskkill", "/T", "/F", "/PID", pid).Run()
		<-done
	}
}
