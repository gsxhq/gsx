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

// NOTE: the TestRunWatch_* integration tests are intentionally NOT t.Parallel():
// they drive a real fsnotify watcher + debounce timer and assert within wall-clock
// deadlines, which would starve competing with the parallel suite for CPU. Keep
// them serial. The deadlines are generous (60s) because the watcher's startup
// cycle runs a cold packages.Load + compile of the temp module — that can take
// tens of seconds on a cold 2-core CI runner (locally it's sub-second). waitFor
// polls and returns as soon as the condition holds, so a large deadline only
// guards against slow CI; it never slows the happy path.
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
	waitFor(t, 60*time.Second, func() bool { return strings.Contains(out.String(), `"event":"start"`) })
	// Do not let the startup cycle satisfy the post-edit wait below. Under load,
	// the test goroutine can observe start before runWatch emits generated.
	waitFor(t, 60*time.Second, func() bool { return countGenerated(out.String(), true) >= 1 })
	// Capture the generated count after startup so we can detect the regen.
	priorCount := countGenerated(out.String(), true)

	// Edit and expect a new generated event with the updated content.
	writeFileT(t, gsxPath, "package views\n\ncomponent Page() {\n\t<h1>two</h1>\n}\n")
	waitFor(t, 60*time.Second, func() bool { return countGenerated(out.String(), true) > priorCount })

	xgo, _ := os.ReadFile(filepath.Join(root, "views", "page.x.go"))
	// Coalesced static writes emit `S("<h1>two</h1>")`, so assert on the content,
	// not a standalone `"two"` token.
	if !strings.Contains(string(xgo), `two</h1>`) {
		t.Fatalf("page.x.go not updated:\n%s", xgo)
	}
	close(stop)
	<-done
}

func TestRunWatch_GeneratesFirstGsxCreatedAfterStartup(t *testing.T) {
	root := t.TempDir()
	writeMod(t, root)
	stagedTree := filepath.Join(t.TempDir(), "new")
	gsxPath := filepath.Join(stagedTree, "nested", "page.gsx")
	writeFileT(t, gsxPath, "package nested\n\ncomponent Page() {\n\t<h1>first</h1>\n}\n")

	out := &syncBuf{}
	errOut := &syncBuf{}
	stop := make(chan struct{})
	done := make(chan int, 1)
	go func() {
		done <- runWatchWithStop(watchConfig{
			paths: []string{root}, format: "ndjson", stdout: out, stderr: errOut,
		}, stop)
	}()
	waitFor(t, 60*time.Second, func() bool { return strings.Contains(out.String(), `"event":"start"`) })

	// Move a fully populated tree into the watched module in one operation.
	// fsnotify reports the new top-level directory; it does not owe us separate
	// create events for children that already existed. The watcher must add and
	// inventory the subtree from that structural event.
	if err := os.Rename(stagedTree, filepath.Join(root, "new")); err != nil {
		t.Fatalf("move populated package tree into watched module: %v", err)
	}
	waitFor(t, 60*time.Second, func() bool { return countGenerated(out.String(), true) >= 1 })
	xgo, err := os.ReadFile(filepath.Join(root, "new", "nested", "page.x.go"))
	if err != nil {
		t.Fatalf("first post-startup .gsx was not generated: %v; stderr=%s; events=%s", err, errOut.String(), out.String())
	}
	if !strings.Contains(string(xgo), `first</h1>`) {
		t.Fatalf("generated output does not contain first component body:\n%s", xgo)
	}
	close(stop)
	if code := <-done; code != 0 {
		t.Fatalf("runWatch exit = %d, want 0; stderr=%s", code, errOut.String())
	}
}

func TestRunWatch_RearmsExplicitExcludedRootAfterRecreation(t *testing.T) {
	root := t.TempDir()
	writeMod(t, root)
	// The selected root sits below an excluded ancestor. Its own watch vanishes
	// on deletion, and the module-root walk deliberately does not watch tmp, so
	// only the explicit-root parent sentinel can observe this recreation.
	explicit := filepath.Join(root, "tmp", "selected")
	gsxPath := filepath.Join(explicit, "page.gsx")
	writeFileT(t, gsxPath, "package selected\ncomponent Page() { <h1>old</h1> }\n")

	out := &syncBuf{}
	errOut := &syncBuf{}
	stop := make(chan struct{})
	done := make(chan int, 1)
	go func() {
		done <- runWatchWithStop(watchConfig{
			paths: []string{explicit}, format: "ndjson", stdout: out, stderr: errOut,
		}, stop)
	}()
	waitFor(t, 60*time.Second, func() bool { return countGenerated(out.String(), true) >= 1 })
	prior := countGenerated(out.String(), true)

	// Delete the excluded ancestor, not only the requested root. The watcher on
	// tmp vanishes too; recreation must be noticed from the surviving module-root
	// watch and must re-arm only the requested branch below tmp.
	if err := os.RemoveAll(filepath.Join(root, "tmp")); err != nil {
		t.Fatal(err)
	}
	writeFileT(t, gsxPath, "package selected\ncomponent Page() { <h1>recreated</h1> }\n")
	waitFor(t, 60*time.Second, func() bool {
		generated, err := os.ReadFile(filepath.Join(explicit, "page.x.go"))
		return err == nil && strings.Contains(string(generated), "recreated</h1>") && countGenerated(out.String(), true) > prior
	})

	close(stop)
	if code := <-done; code != 0 {
		t.Fatalf("runWatch exit = %d, want 0; stderr=%s", code, errOut.String())
	}
}

func TestRunWatch_ReactsToUnpairedXGoDependency(t *testing.T) {
	root := t.TempDir()
	writeMod(t, root)
	helpPath := filepath.Join(root, "helpers", "helper.x.go")
	writeFileT(t, helpPath, "package helpers\n\nfunc Greeting() string { return \"hello\" }\n")
	viewsDir := filepath.Join(root, "views")
	writeFileT(t, filepath.Join(viewsDir, "page.gsx"), "package views\n\nimport \"example.com/m/helpers\"\n\ncomponent Page() {\n\t<h1>{helpers.Greeting()}</h1>\n}\n")

	out := &syncBuf{}
	errOut := &syncBuf{}
	stop := make(chan struct{})
	done := make(chan int, 1)
	go func() {
		done <- runWatchWithStop(watchConfig{
			// Selecting one GSX package must still watch its owning module tree:
			// helper.x.go lives in a sibling authored-Go-only package.
			paths: []string{viewsDir}, format: "ndjson", stdout: out, stderr: errOut,
		}, stop)
	}()
	waitFor(t, 60*time.Second, func() bool { return countGenerated(out.String(), true) >= 1 })
	priorFailures := countGenerated(out.String(), false)

	// Remove the symbol used by page.gsx. This must rebuild the Module and emit
	// a failed generation cycle; treating every .x.go as generated output would
	// silently miss the authored dependency edit.
	writeFileT(t, helpPath, "package helpers\n\nfunc Farewell() string { return \"goodbye\" }\n")
	waitFor(t, 60*time.Second, func() bool { return countGenerated(out.String(), false) > priorFailures })
	close(stop)
	if code := <-done; code != 0 {
		t.Fatalf("runWatch exit = %d, want 0; stderr=%s", code, errOut.String())
	}
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
	for line := range strings.SplitSeq(s, "\n") {
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
	// Serial: see the note on TestRunWatch_RegeneratesOnGsxChange (real-timer deadlines).
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
	waitFor(t, 60*time.Second, func() bool { return strings.Contains(out.String(), `"event":"start"`) })
	// Wait for startup cycle to settle before measuring.
	waitFor(t, 60*time.Second, func() bool { return countGenerated(out.String(), true) >= 1 })
	priorCount := countGenerated(out.String(), true)

	for i := 1; i <= 5; i++ { // 5 rapid writes
		writeFileT(t, gsxPath, "package views\n\ncomponent Page() {\n\t<h1>"+string(rune('0'+i))+"</h1>\n}\n")
		time.Sleep(10 * time.Millisecond) // all within the 100ms window
	}
	waitFor(t, 60*time.Second, func() bool { return countGenerated(out.String(), true) > priorCount })
	time.Sleep(300 * time.Millisecond) // let any extra cycles flush
	if n := countGenerated(out.String(), true) - priorCount; n > 2 {
		t.Fatalf("debounce failed: %d generated cycles for a 5-write burst (want 1, ≤2 tolerated)", n)
	}
	close(stop)
	<-done
}
