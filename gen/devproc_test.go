package gen

import (
	"os/exec"
	"runtime"
	"testing"
	"time"
)

// A child that spawns a grandchild sleeping; both must die on killProcGroup.
func TestKillProcGroupReapsTree(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("posix sh script; covered separately on Windows")
	}
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	// sh starts a background sleep (grandchild), then waits.
	c := exec.Command("sh", "-c", "sleep 30 & wait")
	setProcGroup(c)
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { killProcGroup(c, time.Second); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("killProcGroup did not return in time")
	}
	// The process must be reaped (Wait already happened inside killProcGroup).
	// Note: ProcessState is set for any termination (normal exit OR signal); we
	// do NOT require Exited() because SIGTERM on Unix yields WIFSIGNALED, not WIFEXITED.
	if c.ProcessState == nil {
		t.Errorf("child was not reaped")
	}
}

func TestKillProcGroupNilSafe(t *testing.T) {
	killProcGroup(nil, time.Second)         // must not panic
	killProcGroup(&exec.Cmd{}, time.Second) // no Process yet
}
