package gen

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/gsxhq/gsx/internal/diag"
)

func TestEmitter_NDJSON_GeneratedOK(t *testing.T) {
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

func TestEmitter_NDJSON_DiagnosticsShapeMatchesRenderJSON(t *testing.T) {
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
