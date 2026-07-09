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
// Regenerate with: go test ./internal/gsxfmt -run TestFmtCorpus -update
// Then re-run without -update to verify.
package gsxfmt

import (
	"bytes"
	"flag"
	"go/token"
	"os"
	"path/filepath"
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

			got, err := Format("input.gsx", input, fmtWidth)
			if err != nil {
				t.Fatalf("Format: %v", err)
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

			// Idempotence: the golden is a fixed point of the formatter.
			again, err := Format("input.gsx", want, fmtWidth)
			if err != nil {
				t.Fatalf("re-Format of golden: %v", err)
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
