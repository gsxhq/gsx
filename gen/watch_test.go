package gen

import (
	"bytes"
	"strings"
	"testing"
)

// --watch with --format=ndjson on an empty/non-existent path set must not block:
// runWatch returns promptly with exit 0 when there are no dirs to watch, and
// writes nothing to stdout that isn't valid (empty is fine here).
func TestRunWatch_NoDirsReturnsCleanly(t *testing.T) {
	dir := t.TempDir() // no .gsx files
	var out, errb bytes.Buffer
	code := runWatch(watchConfig{
		paths:  []string{dir},
		format: "ndjson",
		stdout: &out,
		stderr: &errb,
	})
	if code != 0 {
		t.Fatalf("runWatch exit = %d, want 0; stderr=%s", code, errb.String())
	}
	// stdout in ndjson mode must never contain a non-JSON line.
	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		if line != "" && !strings.HasPrefix(line, "{") {
			t.Fatalf("non-JSON stdout line: %q", line)
		}
	}
}
