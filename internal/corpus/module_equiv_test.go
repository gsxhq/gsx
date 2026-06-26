package corpus

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/gsxhq/gsx/internal/codegen"
)

// TestModuleMatchesBatchOverCorpus generates each single-package corpus case two
// ways — the legacy go-list batch and the new in-process Module core — and
// asserts byte-identical .x.go output. This is the Phase 0 correctness gate.
//
// The test is gated by testing.Short because it invokes packages.Load (go list)
// per case, which is slow.
func TestModuleMatchesBatchOverCorpus(t *testing.T) {
	if testing.Short() {
		t.Skip("equivalence gate loads packages; skipped in -short")
	}
	repoRoot, _ := filepath.Abs("../..")

	// Load all corpus cases — same walk as TestCorpus.
	var files []string
	filepath.WalkDir("testdata/cases", func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(p, ".txtar") {
			files = append(files, p)
		}
		return nil
	})
	sort.Strings(files)
	if len(files) == 0 {
		t.Fatal("no testdata/cases/**/*.txtar found")
	}

	var cases []*caseDoc
	for _, p := range files {
		c, err := loadCase(p)
		if err != nil {
			t.Fatalf("%s: %v", p, err)
		}
		cases = append(cases, c)
	}

	// Single shared temp module — each case gets its own subdirectory under it.
	tmp := mustTempModule(repoRoot)
	defer os.RemoveAll(tmp)

	// Derive the module path from the temp go.mod (mustTempModule writes "module corpustest").
	gomodData, err := os.ReadFile(filepath.Join(tmp, "go.mod"))
	if err != nil {
		t.Fatalf("read temp go.mod: %v", err)
	}
	modulePath := parseModulePath(gomodData)

	compared := 0
	for _, c := range cases {
		// Mirror the single-package codegen candidate predicate from TestCorpus
		// (corpus_test.go:54-70):
		//   • single=true  ⟺  !c.multiPkg and has "input.gsx"
		//   • not a parser-error case (parserDiag non-empty)
		//   • not a parser-layer snapshot (has ast.golden)
		_, parserDiag, single := c.astAndParserDiag()
		if !single {
			continue // multi-package or no input.gsx
		}
		if len(parserDiag) > 0 {
			continue // parser-error case — no codegen
		}
		if hasAstGolden(c) {
			continue // parser-layer snapshot — no codegen
		}

		t.Run(c.name, func(t *testing.T) {
			if err := writeCaseSources(tmp, c); err != nil {
				t.Fatalf("writeCaseSources: %v", err)
			}
			pkgDir := caseModuleDir(tmp, c)

			// ── Oracle: existing batch path ──────────────────────────────────────
			batchResults, err := codegen.GeneratePackagesWithFilters(
				tmp, []string{pkgDir},
				[]string{codegen.StdImportPath},
				nil, nil, nil, nil, nil, true, true, nil,
			)
			if err != nil {
				t.Fatalf("batch GeneratePackagesWithFilters: %v", err)
			}
			br := batchResults[pkgDir]
			if br == nil || len(br.Files) == 0 {
				// Diagnostic-only case (parse/type error surfaced as Diags): the batch
				// produces no files. Skip so we don't compare an empty oracle.
				t.Skip("no codegen output from batch (diagnostic-only case)")
			}

			// ── Under test: Module path ─────────────────────────────────────────
			// A fresh Module per case ensures no pkgTypes cache bleed-through and
			// gives an independent externalImporter load.
			m, err := codegen.Open(codegen.Options{
				ModuleRoot: tmp,
				ModulePath: modulePath,
				FilterPkgs: []string{codegen.StdImportPath},
			})
			if err != nil {
				t.Fatalf("codegen.Open: %v", err)
			}
			modOut, _, err := m.Generate(pkgDir)
			if err != nil {
				t.Fatalf("Module.Generate: %v", err)
			}

			// Assert byte-identical output for every .gsx file the batch produced.
			for gsxPath, want := range br.Files {
				got := modOut[gsxPath]
				if string(got) != string(want) {
					t.Errorf("case %s file %s: Module.Generate != batch\n--- batch ---\n%s\n--- module ---\n%s",
						c.name, filepath.Base(gsxPath), want, got)
				}
			}
			compared++
		})
	}

	// Fail loudly if the filter was too aggressive — an over-filtered run that
	// compares nothing would pass vacuously.
	const minCompared = 150
	if compared < minCompared {
		t.Errorf("gate compared only %d case(s) (want ≥ %d); single-package filter may be wrong", compared, minCompared)
	}
}
