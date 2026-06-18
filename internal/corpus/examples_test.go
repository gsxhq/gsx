package corpus

import (
	"bytes"
	"fmt"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/gsxhq/gsx/parser"
)

// TestExamplesCoverage parses every examples/*.gsx through the parser and records,
// per file, whether it parses cleanly ("ok") or the first diagnostic it hits. The
// golden report (testdata/examples_coverage.golden) is a LIVING grammar-coverage
// tracker: as the parser grows (Part 2 control flow / {{ }} / comments / raw text,
// then codegen + render), files flip from a diagnostic to "ok", and the diff makes
// that progress visible. Independent of golden comparison, every real example MUST
// parse WITHOUT PANIC (robustness on real-world inputs) — a panic fails the test.
//
// This is how the examples/ folder is exercised today; individual examples get
// promoted into full testdata/pipeline/*.txtar cases (with ast/diagnostics, then
// generated.x.go and render.golden) once the parser handles them cleanly.
func TestExamplesCoverage(t *testing.T) {
	files, err := filepath.Glob("../../examples/*.gsx")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("no examples/*.gsx found")
	}
	sort.Strings(files)

	var report bytes.Buffer
	for _, path := range files {
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		name := filepath.Base(path)
		// ParseFile must not panic on any real example; a panic propagates and
		// fails the test with a stack trace pointing at the offending input.
		_, perr := parser.ParseFile(token.NewFileSet(), name, src, 0)
		if perr != nil {
			fmt.Fprintf(&report, "%s: %s\n", name, perr.Error())
		} else {
			fmt.Fprintf(&report, "%s: ok\n", name)
		}
	}

	const golden = "testdata/examples_coverage.golden"
	if *update {
		if err := os.WriteFile(golden, report.Bytes(), 0644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("read golden (run with -update first): %v", err)
	}
	if !bytes.Equal(report.Bytes(), want) {
		t.Errorf("examples coverage changed (run -update to accept):\n--- got ---\n%s\n--- want ---\n%s",
			report.Bytes(), want)
	}
}
