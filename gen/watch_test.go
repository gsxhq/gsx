package gen

import (
	"bytes"
	"strings"
	"testing"
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

// --watch with --format=ndjson on an empty/non-existent path set must not block:
// runWatch returns promptly with exit 0 when there are no dirs to watch, and
// writes nothing to stdout that isn't valid (empty is fine here).
func TestRunWatch_NoDirsReturnsCleanly(t *testing.T) {
	t.Parallel()
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
