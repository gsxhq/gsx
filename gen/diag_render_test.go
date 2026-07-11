package gen

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/gsxhq/gsx/internal/attrclass"
)

// gsxWithReservedParam is a .gsx that triggers a reserved-param codegen error
// (component param named "ctx" is reserved).
const gsxWithReservedParam = `package views

component A(ctx string) {
	<div></div>
}
`

// runGenerateArgs drives runGenerate with args strings (including optional --json).
// It returns exit code, stdout, stderr.
func runGenerateArgs(t *testing.T, args []string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	wd, _ := os.Getwd()
	code := runGenerate(args, &out, &errb, false, false, true /*noCache*/, nil, nil, nil, attrclass.Builtin(), nil, nil, nil, true, true, nil, wd)
	return code, out.String(), errb.String()
}

// TestGenerateDiagNonZeroExit proves that a .gsx with a codegen error (reserved-param)
// causes runGenerate to exit with code 1.
func TestGenerateDiagNonZeroExit(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping module-resolution test in -short mode")
	}
	mod := newModule(t, "gsxdiagrender")
	pkgDir := newPkg(t, mod, "views", gsxWithReservedParam)

	code, _, errb := runGenerateArgs(t, []string{pkgDir})
	if code != 1 {
		t.Fatalf("expected exit 1 for codegen diagnostic, got %d; stderr=%q", code, errb)
	}
}

// TestGenerateDiagCompactOutput proves that on a non-TTY stderr (bytes.Buffer),
// runGenerate emits a compact-format diagnostic line containing "error[" and
// the file position.
func TestGenerateDiagCompactOutput(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping module-resolution test in -short mode")
	}
	mod := newModule(t, "gsxdiagcompact")
	pkgDir := newPkg(t, mod, "views", gsxWithReservedParam)

	_, _, errb := runGenerateArgs(t, []string{pkgDir})
	// Compact format: file:line:col: severity[code]: message
	if !strings.Contains(errb, "error[") {
		t.Fatalf("expected compact diagnostic with 'error[' in stderr, got: %q", errb)
	}
	if !strings.Contains(errb, "reserved-param") {
		t.Fatalf("expected 'reserved-param' code in stderr, got: %q", errb)
	}
}

// TestGenerateDiagJSONOutput proves that --json causes runGenerate to emit a
// JSON array on stdout containing the diagnostic with the expected code field,
// and the exit code is still non-zero.
func TestGenerateDiagJSONOutput(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping module-resolution test in -short mode")
	}
	mod := newModule(t, "gsxdiagjson")
	pkgDir := newPkg(t, mod, "views", gsxWithReservedParam)

	code, out, errb := runGenerateArgs(t, []string{"--json", pkgDir})
	if code != 1 {
		t.Fatalf("expected exit 1 for codegen diagnostic with --json, got %d; stderr=%q", code, errb)
	}
	// stdout must be a valid JSON array
	var arr []map[string]any
	if err := json.Unmarshal([]byte(out), &arr); err != nil {
		t.Fatalf("--json: stdout is not a valid JSON array: %v\nstdout=%q\nstderr=%q", err, out, errb)
	}
	if len(arr) == 0 {
		t.Fatalf("--json: expected at least one diagnostic in JSON array, got empty; stdout=%q", out)
	}
	// Must contain the reserved-param diagnostic
	var found bool
	for _, entry := range arr {
		if code, ok := entry["code"].(string); ok && code == "reserved-param" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected 'reserved-param' code in JSON array, got: %v", arr)
	}
}

// TestGenerateDiagJSONNoStderr proves that --json writes nothing to stderr for
// diagnostics (only to stdout), keeping stderr clean for scripted use.
func TestGenerateDiagJSONNoStderr(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping module-resolution test in -short mode")
	}
	mod := newModule(t, "gsxdiagjsonnostderr")
	pkgDir := newPkg(t, mod, "views", gsxWithReservedParam)

	_, _, errb := runGenerateArgs(t, []string{"--json", pkgDir})
	// Diagnostic text must not leak to stderr in JSON mode
	if strings.Contains(errb, "error[") || strings.Contains(errb, "reserved-param") {
		t.Errorf("expected no diagnostic text on stderr with --json, got: %q", errb)
	}
}

// TestGenerateDiagJSONShape pins the JSON diagnostic shape for a reserved-param
// error: the fields file, range.start/end, severity, code, and source must be
// present and have the expected types and values. This is the JSON-shape golden.
func TestGenerateDiagJSONShape(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping module-resolution test in -short mode")
	}
	mod := newModule(t, "gsxdiagjsonshape")
	pkgDir := newPkg(t, mod, "views", gsxWithReservedParam)

	_, out, _ := runGenerateArgs(t, []string{"--json", pkgDir})

	var arr []map[string]any
	if err := json.Unmarshal([]byte(out), &arr); err != nil {
		t.Fatalf("JSON parse error: %v\nout=%q", err, out)
	}
	if len(arr) == 0 {
		t.Fatal("expected at least one diagnostic in JSON array")
	}

	// Find the reserved-param diagnostic.
	var d map[string]any
	for _, entry := range arr {
		if c, ok := entry["code"].(string); ok && c == "reserved-param" {
			d = entry
			break
		}
	}
	if d == nil {
		t.Fatalf("no reserved-param diagnostic in JSON output: %v", arr)
	}

	// Pin required fields.
	if d["severity"] != "error" {
		t.Errorf("severity: want %q, got %v", "error", d["severity"])
	}
	if d["code"] != "reserved-param" {
		t.Errorf("code: want %q, got %v", "reserved-param", d["code"])
	}
	if d["source"] != "codegen" {
		t.Errorf("source: want %q, got %v", "codegen", d["source"])
	}
	// file must be a non-empty string ending in .gsx.
	file, ok := d["file"].(string)
	if !ok || !strings.HasSuffix(file, ".gsx") {
		t.Errorf("file: want a .gsx path string, got %v", d["file"])
	}
	// range must have start and end with line and col fields.
	rng, ok := d["range"].(map[string]any)
	if !ok {
		t.Fatalf("range: expected object, got %T", d["range"])
	}
	for _, key := range []string{"start", "end"} {
		pos, ok := rng[key].(map[string]any)
		if !ok {
			t.Errorf("range.%s: expected object, got %T", key, rng[key])
			continue
		}
		if _, ok := pos["line"].(float64); !ok {
			t.Errorf("range.%s.line: expected number, got %T", key, pos["line"])
		}
		if _, ok := pos["col"].(float64); !ok {
			t.Errorf("range.%s.col: expected number, got %T", key, pos["col"])
		}
	}
	// Start position: gsxWithReservedParam has component A(ctx string) on line 3.
	start := rng["start"].(map[string]any)
	if line := start["line"].(float64); line != 3 {
		t.Errorf("range.start.line: want 3 (component decl line), got %v", line)
	}
}

// TestGenerateDiagJSONFlagAfterPath proves that --json works when placed AFTER
// the path argument (e.g. generate <dir> --json), not just before it.
// Previously Go's flag.FlagSet stopped at the first non-flag argument, so
// "--json" after a path was treated as another path and failed with "no such
// file or directory". The fix pre-partitions args into flags and positionals
// before calling gfs.Parse.
func TestGenerateDiagJSONFlagAfterPath(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping module-resolution test in -short mode")
	}
	mod := newModule(t, "gsxdiagjsonflagafter")
	pkgDir := newPkg(t, mod, "views", gsxWithReservedParam)

	// Flag comes AFTER the path — this is the previously broken form.
	code, out, errb := runGenerateArgs(t, []string{pkgDir, "--json"})
	if code != 1 {
		t.Fatalf("expected exit 1 for codegen diagnostic with --json after path, got %d; stderr=%q", code, errb)
	}
	// stdout must be a valid JSON array
	var arr []map[string]any
	if err := json.Unmarshal([]byte(out), &arr); err != nil {
		t.Fatalf("--json after path: stdout is not a valid JSON array: %v\nstdout=%q\nstderr=%q", err, out, errb)
	}
	if len(arr) == 0 {
		t.Fatalf("--json after path: expected at least one diagnostic in JSON array, got empty; stdout=%q", out)
	}
	// Must contain the reserved-param diagnostic
	var found bool
	for _, entry := range arr {
		if c, ok := entry["code"].(string); ok && c == "reserved-param" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected 'reserved-param' code in JSON array (flag after path), got: %v", arr)
	}
}

// newPkg creates pkgDir under mod, writes src as the sole .gsx file,
// and returns the package directory path.
func newPkg(t *testing.T, mod, pkg, src string) string {
	t.Helper()
	writeFile(t, mod+"/"+pkg, pkg+".gsx", src)
	return mod + "/" + pkg
}
