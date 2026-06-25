package gen

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// drive runWatch in a goroutine over a temp module; touch a .gsx; assert a
// `generated ok:true` NDJSON line and the updated .x.go. A goroutine-safe buffer
// avoids racing the writer.
type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) { s.mu.Lock(); defer s.mu.Unlock(); return s.b.Write(p) }
func (s *syncBuf) String() string              { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

func TestRunWatch_RegeneratesOnGsxChange(t *testing.T) {
	root := t.TempDir()
	writeMod(t, root)
	gsxPath := filepath.Join(root, "views", "page.gsx")
	writeFileT(t, gsxPath, "package views\n\ncomponent Page() {\n\t<h1>one</h1>\n}\n")

	out := &syncBuf{}
	errb := &syncBuf{}
	done := make(chan int, 1)
	stop := make(chan struct{})
	go func() {
		done <- runWatchWithStop(watchConfig{
			paths: []string{filepath.Join(root, "views")}, format: "ndjson",
			stdout: out, stderr: errb,
		}, stop)
	}()

	// Wait for the watcher to announce it is ready (start event appears first,
	// then the cold-generate startup cycle is emitted).
	waitFor(t, 5*time.Second, func() bool { return strings.Contains(out.String(), `"event":"start"`) })
	// Capture the generated count after startup so we can detect the regen.
	priorCount := countGenerated(out.String(), true)

	// Edit and expect a new generated event with the updated content.
	writeFileT(t, gsxPath, "package views\n\ncomponent Page() {\n\t<h1>two</h1>\n}\n")
	waitFor(t, 5*time.Second, func() bool { return countGenerated(out.String(), true) > priorCount })

	xgo, _ := os.ReadFile(filepath.Join(root, "views", "page.x.go"))
	if !strings.Contains(string(xgo), `"two"`) {
		t.Fatalf("page.x.go not updated:\n%s", xgo)
	}
	close(stop)
	<-done
}

func waitFor(t *testing.T, d time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("condition not met within %s", d)
}

func countGenerated(s string, ok bool) int {
	n := 0
	for _, line := range strings.Split(s, "\n") {
		var ev map[string]any
		if json.Unmarshal([]byte(line), &ev) != nil {
			continue
		}
		if ev["event"] == "generated" && ev["ok"] == ok {
			n++
		}
	}
	return n
}

// A burst of writes within the debounce window coalesces into a single cycle.
func TestRunWatch_DebounceCoalesces(t *testing.T) {
	root := t.TempDir()
	writeMod(t, root)
	gsxPath := filepath.Join(root, "views", "page.gsx")
	writeFileT(t, gsxPath, "package views\n\ncomponent Page() {\n\t<h1>0</h1>\n}\n")

	out := &syncBuf{}
	stop := make(chan struct{})
	done := make(chan int, 1)
	go func() {
		done <- runWatchWithStop(watchConfig{paths: []string{filepath.Join(root, "views")}, format: "ndjson", stdout: out, stderr: &syncBuf{}}, stop)
	}()
	waitFor(t, 5*time.Second, func() bool { return strings.Contains(out.String(), `"event":"start"`) })
	// Wait for startup cycle to settle before measuring.
	waitFor(t, 5*time.Second, func() bool { return countGenerated(out.String(), true) >= 1 })
	priorCount := countGenerated(out.String(), true)

	for i := 1; i <= 5; i++ { // 5 rapid writes
		writeFileT(t, gsxPath, "package views\n\ncomponent Page() {\n\t<h1>"+string(rune('0'+i))+"</h1>\n}\n")
		time.Sleep(10 * time.Millisecond) // all within the 100ms window
	}
	waitFor(t, 5*time.Second, func() bool { return countGenerated(out.String(), true) > priorCount })
	time.Sleep(300 * time.Millisecond) // let any extra cycles flush
	if n := countGenerated(out.String(), true) - priorCount; n > 2 {
		t.Fatalf("debounce failed: %d generated cycles for a 5-write burst (want 1, ≤2 tolerated)", n)
	}
	close(stop)
	<-done
}
