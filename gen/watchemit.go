package gen

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"

	"github.com/gsxhq/gsx/internal/diag"
)

type emitter struct {
	ndjson bool
	stdout io.Writer
	stderr io.Writer
}

func (e *emitter) start(root string, watching []string) {
	if e.ndjson {
		e.line(map[string]any{"event": "start", "root": root, "watching": watching})
		return
	}
	fmt.Fprintf(e.stderr, "gsx: watching %d dir(s) under %s\n", len(watching), root)
}

func (e *emitter) cycle(r cycleResult) {
	if e.ndjson {
		ev := map[string]any{
			"event":       "generated",
			"ok":          r.OK,
			"durationMs":  r.durationMs(),
			"written":     baseNames(r.Written),
			"diagnostics": rawDiagnostics(r.Diags),
		}
		e.line(ev)
		return
	}
	if r.OK {
		fmt.Fprintf(e.stderr, "regenerated %s — %d file(s), %dms\n", r.Dir, len(r.Written), r.durationMs())
		return
	}
	// RenderRich's SourceProvider is func(name string) ([]byte, bool); the watch
	// daemon doesn't surface source frames, so return "not found".
	src := func(string) ([]byte, bool) { return nil, false }
	diag.RenderRich(e.stderr, r.Diags, src)
}

func (e *emitter) line(ev map[string]any) {
	b, _ := json.Marshal(ev)
	e.stdout.Write(b)
	e.stdout.Write([]byte("\n"))
}

// rawDiagnostics encodes diags through the canonical RenderJSON so the NDJSON
// diagnostics field is byte-identical to `gsx generate --json`.
func rawDiagnostics(d []diag.Diagnostic) json.RawMessage {
	var buf bytes.Buffer
	_ = diag.RenderJSON(&buf, d)
	return json.RawMessage(bytes.TrimSpace(buf.Bytes()))
}

// baseNames returns the base filename for each path, so the NDJSON written
// field contains clean file names rather than absolute paths.
func baseNames(paths []string) []string {
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		out = append(out, filepath.Base(p))
	}
	return out
}
