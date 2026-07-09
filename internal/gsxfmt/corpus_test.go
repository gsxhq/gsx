// The fmt corpus is the authoritative reference for gsx fmt's LAYOUT.
//
// It is deliberately separate from internal/corpus, which pins parse → generate
// → render. Those cases answer "what does this gsx MEAN"; a formatter case
// answers "where do the bytes go", and the two rot in opposite directions: a
// codegen golden must not churn when the formatter's line-breaking changes, and
// a layout golden must not churn when generated code changes. Keeping them apart
// also keeps this suite fast — no type-checking, no compilation, no rendering.
//
// The invariant suite in internal/printer (faithfulness, idempotence, re-parse
// safety, no-verbatim-fallback) runs over the SEMANTIC corpus and asserts
// properties that hold for every input. It cannot see a layout regression: a
// formatter that reflows the author's source is still faithful, still
// idempotent, still re-parses. Only a pinned golden catches that — which is what
// this corpus is for.
//
// Each case is a txtar with:
//
//	-- input.gsx --    the source to format
//	-- fmt.golden --   the expected output
//
// Two more sections are optional:
//
//	-- imports --   "gofmt" (default when absent) or "goimports", parsed with
//	                this package's own ParseImportsMode. Selects the import
//	                handling mode the case formats under (FormatOptions.Reorder).
//	-- unused --    newline-separated import refs to delete before formatting,
//	                one per line as `path` or `alias path`; blank lines and lines
//	                starting with # are ignored. Absent means Unused is nil.
//	                This section exists so unused-import removal can be pinned
//	                deterministically and fast: the real `gsx fmt` CLI derives
//	                its Unused list from full module analysis (type-checking,
//	                `go list`), which this suite deliberately avoids to stay
//	                quick — the corpus case supplies the same list by hand.
//	-- tab_width -- a positive integer, the column width of one tab when
//	                measuring line overflow (FormatOptions.TabWidth). Absent
//	                means 0, i.e. pretty.DefaultTabWidth. Indentation is always
//	                emitted as tabs regardless of this value — it only changes
//	                where a line is judged to overflow the width budget.
//
// Regenerate with: go test ./internal/gsxfmt -run TestFmtCorpus -update
// Then re-run without -update to verify.
package gsxfmt

import (
	"bytes"
	"flag"
	"fmt"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/gsxhq/gsx/internal/txtar"
	"github.com/gsxhq/gsx/parser"
)

var update = flag.Bool("update", false, "rewrite fmt.golden files from actual output")

// fmtWidth is the column budget every corpus case is formatted at. Pinned here
// (not per-case) so a golden's line breaks mean one thing across the suite.
const fmtWidth = 80

func TestFmtCorpus(t *testing.T) {
	cases, err := filepath.Glob("testdata/cases/*.txtar")
	if err != nil {
		t.Fatal(err)
	}
	if len(cases) == 0 {
		t.Fatal("no fmt corpus cases found — this suite is asserting nothing")
	}
	for _, path := range cases {
		t.Run(strings.TrimSuffix(filepath.Base(path), ".txtar"), func(t *testing.T) {
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			ar := txtar.Parse(data)
			input, ok := archiveFile(ar, "input.gsx")
			if !ok {
				t.Fatal("case has no input.gsx")
			}

			modeStr := "gofmt"
			if raw, ok := archiveFile(ar, "imports"); ok {
				modeStr = strings.TrimSpace(string(raw))
			}
			mode, err := ParseImportsMode(modeStr)
			if err != nil {
				t.Fatalf("case %s: %v", path, err)
			}

			var unused []ImportRef
			if raw, ok := archiveFile(ar, "unused"); ok {
				refs, err := parseUnusedRefs(string(raw))
				if err != nil {
					t.Fatalf("case %s: %v", path, err)
				}
				unused = refs
			}

			tabWidth := 0
			if raw, ok := archiveFile(ar, "tab_width"); ok {
				n, err := strconv.Atoi(strings.TrimSpace(string(raw)))
				if err != nil || n <= 0 {
					t.Fatalf("case %s: bad tab_width %q", path, raw)
				}
				tabWidth = n
			}

			opts := FormatOptions{Unused: unused, Width: fmtWidth, TabWidth: tabWidth, Reorder: mode.Reorder()}

			got, err := FormatWith("input.gsx", input, opts)
			if err != nil {
				t.Fatalf("FormatWith: %v", err)
			}

			if *update {
				setArchiveFile(ar, "fmt.golden", got)
				if err := os.WriteFile(path, txtar.Format(ar), 0o644); err != nil {
					t.Fatal(err)
				}
			}

			want, ok := archiveFile(ar, "fmt.golden")
			if !ok {
				t.Fatal("case has no fmt.golden — run with -update")
			}
			if !bytes.Equal(got, want) {
				t.Errorf("fmt output differs from fmt.golden\n--- got ---\n%s\n--- want ---\n%s", got, want)
			}

			// Idempotence: the golden is a fixed point of the formatter under the
			// case's own mode (an "unused" list already gone from the golden must
			// still be a no-op re-applied to it).
			again, err := FormatWith("input.gsx", want, opts)
			if err != nil {
				t.Fatalf("re-FormatWith of golden: %v", err)
			}
			if !bytes.Equal(again, want) {
				t.Errorf("fmt is not idempotent on its own golden\n--- once ---\n%s\n--- twice ---\n%s", want, again)
			}

			// Re-parse safety: fmt never emits gsx it cannot read back.
			if _, err := parser.ParseFile(token.NewFileSet(), "fmt.golden", want, 0); err != nil {
				t.Errorf("formatted output does not re-parse: %v", err)
			}
		})
	}
}

// parseUnusedRefs parses a "-- unused --" section body into ImportRefs, one per
// non-blank, non-comment line, each spelled `path` (default import) or
// `alias path` (aliased import).
func parseUnusedRefs(raw string) ([]ImportRef, error) {
	var refs []ImportRef
	for line := range strings.SplitSeq(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		switch len(fields) {
		case 1:
			refs = append(refs, ImportRef{Path: fields[0]})
		case 2:
			refs = append(refs, ImportRef{Name: fields[0], Path: fields[1]})
		default:
			return nil, fmt.Errorf("malformed unused-import line %q (want `path` or `alias path`)", line)
		}
	}
	return refs, nil
}

func archiveFile(ar *txtar.Archive, name string) ([]byte, bool) {
	for _, f := range ar.Files {
		if f.Name == name {
			return f.Data, true
		}
	}
	return nil, false
}

// setArchiveFile overwrites name's data, appending the file if absent, so
// -update can seed a brand-new case that ships only an input.gsx.
func setArchiveFile(ar *txtar.Archive, name string, data []byte) {
	for i := range ar.Files {
		if ar.Files[i].Name == name {
			ar.Files[i].Data = data
			return
		}
	}
	ar.Files = append(ar.Files, txtar.File{Name: name, Data: data})
}
