//go:build !windows

package gen

import (
	"os/exec"
	"syscall"
	"time"
)

// setProcGroup makes the child its own process-group leader so we can signal the
// whole tree with kill(-pgid).
func setProcGroup(c *exec.Cmd) {
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killProcGroup sends SIGTERM to the child's process group, waits up to timeout,
// then SIGKILLs the group. It always waits for the child to be reaped.
func killProcGroup(c *exec.Cmd, timeout time.Duration) {
	if c == nil || c.Process == nil {
		return
	}
	pgid := c.Process.Pid // Setpgid made the child the group leader (pgid == pid)
	_ = syscall.Kill(-pgid, syscall.SIGTERM)
	done := make(chan struct{})
	go func() { _ = c.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(timeout):
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
		<-done
	}
}
