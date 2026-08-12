package gen

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/gsxhq/gsx/internal/diag"
)

func TestEmitter_NDJSON_GeneratedOK(t *testing.T) {
	t.Parallel()
	var out, errb bytes.Buffer
	e := &emitter{ndjson: true, stdout: &out, stderr: &errb}
	e.cycle(cycleResult{Dir: "/m/views", Written: []string{"/m/views/page.x.go"}, OK: true})

	var ev map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(out.String())), &ev); err != nil {
		t.Fatalf("stdout is not one JSON object: %q (%v)", out.String(), err)
	}
	if ev["event"] != "generated" || ev["ok"] != true {
		t.Fatalf("unexpected event: %v", ev)
	}
	if _, hasDur := ev["durationMs"]; !hasDur {
		t.Fatalf("missing durationMs: %v", ev)
	}
}

func TestEmitter_NDJSON_OperationalErrorSurfaces(t *testing.T) {
	t.Parallel()
	var out, errb bytes.Buffer
	e := &emitter{ndjson: true, stdout: &out, stderr: &errb}
	e.cycle(cycleResult{Dir: "/m/views", OK: false, Err: errors.New("disk full"), Diags: nil})
	// The operational error must reach stdout as a machine-readable signal,
	// not vanish. Assert an "error" event carrying the message appears.
	if !strings.Contains(out.String(), `"event":"error"`) || !strings.Contains(out.String(), "disk full") {
		t.Fatalf("operational error not surfaced in NDJSON: %q", out.String())
	}
	// Every stdout line must still be valid JSON (stream discipline).
	for line := range strings.SplitSeq(strings.TrimSpace(out.String()), "\n") {
		if line == "" {
			continue
		}
		var v map[string]any
		if json.Unmarshal([]byte(line), &v) != nil {
			t.Fatalf("non-JSON stdout line: %q", line)
		}
	}
}

// TestEmitter_NDJSON_ReloadField proves the "generated" NDJSON event carries
// a "reload" field when cycleResult.Reload is non-empty, and omits the key
// entirely (not an empty-string value) when it is empty — the LSP override
// path can leave a pending reload whose Describe() is "", and no consumer may
// render that as a note.
func TestEmitter_NDJSON_ReloadField(t *testing.T) {
	t.Parallel()
	var out, errb bytes.Buffer
	e := &emitter{ndjson: true, stdout: &out, stderr: &errb}
	e.cycle(cycleResult{Dir: "/m/page", OK: true, Reload: "changed Go source dep/dep.go"})

	var ev map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(out.String())), &ev); err != nil {
		t.Fatalf("stdout is not one JSON object: %q (%v)", out.String(), err)
	}
	if ev["reload"] != "changed Go source dep/dep.go" {
		t.Fatalf("reload field = %v, want %q", ev["reload"], "changed Go source dep/dep.go")
	}

	out.Reset()
	e.cycle(cycleResult{Dir: "/m/other", OK: true})
	var ev2 map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(out.String())), &ev2); err != nil {
		t.Fatalf("stdout is not one JSON object: %q (%v)", out.String(), err)
	}
	if _, has := ev2["reload"]; has {
		t.Fatalf("reload key present for empty Reload: %v", ev2)
	}
}

// TestEmitter_HumanLine_ReloadNote proves the non-NDJSON console output
// prints a "full reload: <reason>" line when cycleResult.Reload is
// non-empty, and prints nothing extra when it is empty.
func TestEmitter_HumanLine_ReloadNote(t *testing.T) {
	t.Parallel()
	var out, errb bytes.Buffer
	e := &emitter{ndjson: false, stdout: &out, stderr: &errb}
	e.cycle(cycleResult{Dir: "/m/page", OK: true, Reload: "changed Go source dep/dep.go"})

	if !strings.Contains(errb.String(), "full reload: changed Go source dep/dep.go") {
		t.Fatalf("stderr missing reload human line: %q", errb.String())
	}

	errb.Reset()
	e.cycle(cycleResult{Dir: "/m/other", OK: true})
	if strings.Contains(errb.String(), "full reload:") {
		t.Fatalf("stderr has a reload line for an empty Reload: %q", errb.String())
	}
}

func TestEmitter_NDJSON_DiagnosticsShapeMatchesRenderJSON(t *testing.T) {
	t.Parallel()
	d := diag.Diagnostic{Severity: diag.Error, Code: "x", Message: "boom"}
	var want bytes.Buffer
	_ = diag.RenderJSON(&want, []diag.Diagnostic{d})

	var out, errb bytes.Buffer
	e := &emitter{ndjson: true, stdout: &out, stderr: &errb}
	e.cycle(cycleResult{Dir: "/m/views", OK: false, Diags: []diag.Diagnostic{d}})

	var ev map[string]json.RawMessage
	_ = json.Unmarshal([]byte(strings.TrimSpace(out.String())), &ev)
	// The diagnostics field must equal RenderJSON's encoding (same shape, no 3rd copy).
	if strings.TrimSpace(string(ev["diagnostics"])) != strings.TrimSpace(want.String()) {
		t.Fatalf("diagnostics shape drift:\n got=%s\nwant=%s", ev["diagnostics"], want.String())
	}
}
