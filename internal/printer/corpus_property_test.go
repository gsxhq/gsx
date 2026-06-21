package printer

import (
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/gsxhq/gsx/internal/txtar"
	"github.com/gsxhq/gsx/parser"
)

// corpusInput is one parseable `input.gsx` extracted from a corpus txtar case.
type corpusInput struct {
	name string // case path (for failure messages)
	src  string // the input.gsx contents
}

// loadParseableCorpusInputs globs every corpus case archive, extracts its
// `input.gsx` section, and keeps only those that PARSE on their own. Cases
// without an input.gsx, or whose input is an intentional parse-error fixture,
// are skipped (the latter are diagnostics cases, out of scope for formatting).
func loadParseableCorpusInputs(t *testing.T) []corpusInput {
	t.Helper()
	cases, err := filepath.Glob("../../internal/corpus/testdata/cases/*/*.txtar")
	if err != nil {
		t.Fatalf("glob corpus cases: %v", err)
	}
	if len(cases) == 0 {
		t.Fatal("no corpus cases found — glob path wrong?")
	}
	var inputs []corpusInput
	for _, c := range cases {
		data, err := os.ReadFile(c)
		if err != nil {
			t.Fatalf("read %s: %v", c, err)
		}
		ar := txtar.Parse(data)
		var src string
		var found bool
		for _, f := range ar.Files {
			if f.Name == "input.gsx" {
				src = string(f.Data)
				found = true
				break
			}
		}
		if !found {
			continue
		}
		fset := token.NewFileSet()
		if _, perr := parser.ParseFile(fset, c, src, 0); perr != nil {
			// Intentional parse-error fixture (e.g. diagnostics) — not formattable.
			continue
		}
		inputs = append(inputs, corpusInput{name: c, src: src})
	}
	return inputs
}

// TestCorpusInputsProperty asserts the three printer contracts over every
// parseable corpus input.gsx:
//
//   - Faithfulness: Normalize(parse(fmt(S))) deep-equals Normalize(parse(S)),
//     comparing the markup structure and text exactly while canonicalizing Go
//     fragments (the formatter may gofmt Go) and zeroing spans on both sides.
//   - Idempotence: fmt(fmt(S)) is byte-identical to fmt(S).
//   - Re-parse safety: fmt(S) re-parses without error.
//
// Any failure here is a real printer bug, not a fixture quirk: the corpus inputs
// that don't parse originally are already filtered out.
func TestCorpusInputsProperty(t *testing.T) {
	inputs := loadParseableCorpusInputs(t)
	if len(inputs) == 0 {
		t.Fatal("no parseable corpus inputs — something is wrong")
	}
	t.Logf("checking %d parseable corpus inputs", len(inputs))

	for _, in := range inputs {
		name := filepath.Base(filepath.Dir(in.name)) + "/" + filepath.Base(in.name)

		formatted, err := normPrint(t, in.src)
		if err != nil {
			t.Errorf("%s: fmt failed: %v", name, err)
			continue
		}

		// Re-parse safety: the formatted output must parse.
		fset := token.NewFileSet()
		if _, rerr := parser.ParseFile(fset, name, formatted, 0); rerr != nil {
			t.Errorf("%s: formatted output does not re-parse: %v\n%s", name, rerr, formatted)
			continue
		}

		// Faithfulness: normalized ASTs (spans zeroed, Go fragments canonicalized)
		// must be deep-equal.
		want := normalizedAST(t, in.src)
		got := normalizedAST(t, formatted)
		if !reflect.DeepEqual(want, got) {
			t.Errorf("%s: fmt changed the normalized AST (not render-faithful)", name)
		}

		// Idempotence: formatting the formatted output is a no-op.
		formatted2, err := normPrint(t, formatted)
		if err != nil {
			t.Errorf("%s: re-fmt failed: %v", name, err)
			continue
		}
		if formatted != formatted2 {
			t.Errorf("%s: fmt is not idempotent\n--- first ---\n%s\n--- second ---\n%s", name, formatted, formatted2)
		}
	}
}
