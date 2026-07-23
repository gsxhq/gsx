//go:build js

package gen

import (
	"os/exec"
	"time"
)

// Processes and process groups are unavailable in js/wasm. These stubs keep
// packages that expose the gsx generator buildable for the browser; gsx dev
// itself cannot run in that environment.
func setProcGroup(_ *exec.Cmd) {}

func killProcGroup(c *exec.Cmd, _ time.Duration) {
	if c == nil || c.Process == nil {
		return
	}
	_ = c.Process.Kill()
	_ = c.Wait()
}

func killProcGroupOwned(c *exec.Cmd, done <-chan struct{}, _ time.Duration) {
	if c == nil || c.Process == nil {
		return
	}
	_ = c.Process.Kill()
	<-done
}
