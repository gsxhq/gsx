package gen

import (
	"strings"
	"testing"
	"time"
)

func TestExcludedDir_OnlyOwnBasename(t *testing.T) {
	t.Parallel()
	cases := map[string]bool{
		"/private/tmp/proj/views":      false, // ancestor "tmp" must NOT exclude
		"/tmp/proj/views":              false,
		"/home/u/dev/app/views":        false,
		"/home/u/dev/app/tmp":          true, // project-local tmp/ IS excluded
		"/home/u/dev/app/dist":         true,
		"/home/u/dev/app/node_modules": true,
		"/home/u/dev/app/.git":         true,
	}
	for p, want := range cases {
		if got := excludedDir(p); got != want {
			t.Errorf("excludedDir(%q) = %v, want %v", p, got, want)
		}
	}
}

// An empty module is a structural watch target: the process stays live until
// stopped so it can observe the first .gsx package created after startup.
func TestRunWatch_EmptyModuleStaysLive(t *testing.T) {
	dir := t.TempDir()
	writeMod(t, dir)
	out, errb := &syncBuf{}, &syncBuf{}
	stop := make(chan struct{})
	done := make(chan int, 1)
	go func() {
		done <- runWatchWithStop(watchConfig{
			paths:  []string{dir},
			format: "ndjson",
			stdout: out,
			stderr: errb,
		}, stop)
	}()
	waitFor(t, 10*time.Second, func() bool { return strings.Contains(out.String(), `"event":"start"`) })
	select {
	case code := <-done:
		t.Fatalf("runWatch exited before stop with code %d; stderr=%s", code, errb.String())
	default:
	}
	close(stop)
	code := <-done
	if code != 0 {
		t.Fatalf("runWatch exit = %d, want 0; stderr=%s", code, errb.String())
	}
	// stdout in ndjson mode must never contain a non-JSON line.
	for line := range strings.SplitSeq(strings.TrimSpace(out.String()), "\n") {
		if line != "" && !strings.HasPrefix(line, "{") {
			t.Fatalf("non-JSON stdout line: %q", line)
		}
	}
}
