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
	// reloadNoted records that this cycle already surfaced its world-reload
	// note. Reset by cycleBatch, which is the only place a cycle boundary
	// exists on this side of the loop. See cycleBatch.
	reloadNoted bool
}

func (e *emitter) start(root string, watching []string) {
	if e.ndjson {
		e.line(map[string]any{"event": "start", "root": root, "watching": watching})
		return
	}
	fmt.Fprintf(e.stderr, "gsx: watching %d dir(s) under %s\n", len(watching), root)
}

// cycleBatch emits every result of ONE regeneration cycle, surfacing at most
// one world-reload note for the whole cycle.
//
// A reload is module-wide, but the stamp lands per cycleResult from two
// independent branches — regenPending's orphan-only branch and regenDirs'
// first-generated-dir convention — each attributing the reload from its own
// refresh call. One cycle can therefore carry several Reload strings with
// DIFFERENT causes for the same single reload, and a multi-leaf batch (a
// branch switch touching several go-only zero-dependent packages) carries one
// per leaf. Deduping here rather than at the stamp keeps cycleResult.Reload
// exactly as every other consumer already reads it: aggregateEvent, cycleStat
// and firstReload independently fold a batch with "first non-empty wins", and
// this is the same fold applied to the console and NDJSON surfaces, which
// consume results one at a time and so had no cycle scope of their own.
func (e *emitter) cycleBatch(results []cycleResult) {
	e.reloadNoted = false
	for _, r := range results {
		e.cycle(r)
	}
}

func (e *emitter) cycle(r cycleResult) {
	// Operational errors (I/O failure, resolver error with no diagnostics) must
	// never be silent: emit an explicit error event/message so the caller can act.
	// This is distinct from compile-diagnostic failures where Diags carries the detail.
	if !r.OK && len(r.Diags) == 0 && r.Err != nil {
		e.emitError(r.Err)
	}
	reload := r.Reload
	if reload != "" {
		if e.reloadNoted {
			reload = "" // this cycle already reported the reload — see cycleBatch
		} else {
			e.reloadNoted = true
		}
	}
	if e.ndjson {
		ev := map[string]any{
			"event":       "generated",
			"ok":          r.OK,
			"durationMs":  r.durationMs(),
			"written":     baseNames(r.Written),
			"removed":     baseNames(r.Removed),
			"diagnostics": rawDiagnostics(r.Diags),
		}
		// r.Reload can itself be "" for a pending-but-unattributed reload
		// (RefreshVerdict.Describe()'s default branch) — omit the key
		// entirely rather than emit a rendered empty reason.
		if reload != "" {
			ev["reload"] = reload
		}
		e.line(ev)
		return
	}
	if reload != "" {
		fmt.Fprintf(e.stderr, "full reload: %s\n", reload)
	}
	if r.OK {
		if len(r.Removed) > 0 {
			fmt.Fprintf(e.stderr, "regenerated %s — %d file(s), %d removed, %dms\n", r.Dir, len(r.Written), len(r.Removed), r.durationMs())
			return
		}
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

func (e *emitter) emitError(err error) {
	if e.ndjson {
		e.line(map[string]any{"event": "error", "message": err.Error()})
		return
	}
	fmt.Fprintf(e.stderr, "gsx: %v\n", err)
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
