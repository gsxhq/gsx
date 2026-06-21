package corpus

import (
	"bytes"
	"flag"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/gsxhq/gsx/internal/txtar"
)

var update = flag.Bool("update", false, "regenerate golden sections in testdata/cases")

func TestCorpus(t *testing.T) {
	repoRoot, _ := filepath.Abs("../..")
	var files []string
	filepath.WalkDir("testdata/cases", func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(p, ".txtar") {
			files = append(files, p)
		}
		return nil
	})
	sort.Strings(files)
	if len(files) == 0 {
		t.Fatal("no testdata/cases/**/*.txtar")
	}

	var cases []*caseDoc
	paths := map[string]string{} // case name -> txtar path
	for _, p := range files {
		c, err := loadCase(p)
		if err != nil {
			t.Fatalf("%s: %v", p, err)
		}
		cases = append(cases, c)
		paths[c.name] = p
	}

	// Single batch render (the only place renderable cases are generated).
	batch, err := renderBatch(repoRoot, cases)
	if err != nil {
		t.Fatalf("renderBatch: %v", err)
	}

	// Pre-pass: run generate concurrently for all default-branch cases (non-renderable,
	// non-(single && hasAstGolden), non-(single && parserDiag>0)) so the per-case
	// compare loop below can read from the map instead of calling generate inline.
	type precomputedResult struct {
		gen  []byte
		diag []byte
	}
	precomputed := map[string]precomputedResult{}
	var precomputedMu sync.Mutex

	sem := make(chan struct{}, runtime.NumCPU())
	var wg sync.WaitGroup
	for _, c := range cases {
		c := c
		_, parserDiag, single := c.astAndParserDiag()
		if c.renderable() {
			continue
		}
		if single && hasAstGolden(c) {
			continue
		}
		if single && len(parserDiag) > 0 {
			continue
		}
		// This case hits the default branch — pre-generate it.
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			tmp := mustTempModule(repoRoot)
			gen, diag := c.generate(caseModuleDir(tmp, c), caseImportRoot(c))
			diag = normalizeDiagPaths(diag, tmp)
			os.RemoveAll(tmp)
			precomputedMu.Lock()
			precomputed[c.name] = precomputedResult{gen: gen, diag: diag}
			precomputedMu.Unlock()
		}()
	}
	wg.Wait()

	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			astDump, parserDiag, single := c.astAndParserDiag()

			// Resolve diagnostics + generated facets without re-generating renderables.
			var diagGot, genGot []byte
			switch {
			case single && len(parserDiag) > 0:
				diagGot = parserDiag // parser-error case; no codegen
			case c.renderable():
				if r := batch[c.name]; r != nil {
					diagGot, genGot = r.diagnostics, r.generated
				}
			case single && hasAstGolden(c):
				// parser-layer snapshot (pins ast.golden): parse + AST + parser
				// diagnostics only; codegen stays in the parser layer per spec §2.
				diagGot = parserDiag
			default:
				if r, ok := precomputed[c.name]; ok {
					genGot, diagGot = r.gen, r.diag
				}
			}

			if single {
				checkOrUpdateFacet(t, c, "ast.golden", astDump, paths[c.name])
			}
			checkOrUpdateFacet(t, c, "diagnostics.golden", diagGot, paths[c.name])
			checkOrUpdateFacet(t, c, "generated.x.go.golden", genGot, paths[c.name])

			if c.renderable() {
				if len(diagGot) == 0 {
					if _, ok := c.goldens["render.golden"]; !ok && !*update {
						t.Fatalf("renderable case has no render.golden (run -update)")
					}
				}
				gotHTML := ""
				if r := batch[c.name]; r != nil {
					gotHTML = r.html
				}
				if *update {
					setSection(c.archive, "render.golden", []byte(gotHTML))
					writeArchive(t, paths[c.name], c.archive)
				} else {
					diff, derr := htmlStructuralDiff(gotHTML, string(c.goldens["render.golden"]))
					if derr != nil {
						t.Fatal(derr)
					}
					if diff != "" {
						t.Errorf("%s: render mismatch (%s)\n--- got ---\n%s\n--- want ---\n%s",
							c.name, diff, gotHTML, c.goldens["render.golden"])
					}
				}
			}
		})
	}

	checkOrUpdateCoverage(t, cases)
}

// checkOrUpdateFacet compares one computed facet to its golden section. The
// ast/generated sections are only enforced when present in the archive;
// diagnostics is always enforced (absent ⇒ expect empty).
func checkOrUpdateFacet(t *testing.T, c *caseDoc, sec string, got []byte, path string) {
	t.Helper()
	_, present := c.goldens[sec]
	if sec != "diagnostics.golden" && !present {
		return // optional facet not pinned
	}
	if *update {
		// Only (re)write the section if it already exists, or for diagnostics
		// when there is something to record, to avoid spurious empty sections.
		if present || sec == "diagnostics.golden" {
			setSection(c.archive, sec, got)
			writeArchive(t, path, c.archive)
		}
		return
	}
	if !bytes.Equal(got, c.goldens[sec]) {
		t.Errorf("%s: %s mismatch\n--- got ---\n%s\n--- want ---\n%s", c.name, sec, got, c.goldens[sec])
	}
}

func checkOrUpdateCoverage(t *testing.T, cases []*caseDoc) {
	t.Helper()
	got := coverageReport(cases)
	const golden = "testdata/coverage.golden"
	if *update {
		if err := os.WriteFile(golden, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("read coverage golden (run -update): %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("coverage changed (run -update):\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

// hasAstGolden reports whether the case has an ast.golden section pinned.
func hasAstGolden(c *caseDoc) bool {
	_, ok := c.goldens["ast.golden"]
	return ok
}

// normalizeDiagPaths replaces occurrences of the temp module dir (and its
// filepath.Separator-trailing form) in diag with an empty prefix so that
// golden files contain stable relative paths independent of the OS temp dir.
func normalizeDiagPaths(diag []byte, tmpDir string) []byte {
	if len(diag) == 0 {
		return diag
	}
	// Replace "tmpDir/" (with separator) so remaining path is relative.
	prefix := tmpDir + string(filepath.Separator)
	return bytes.ReplaceAll(diag, []byte(prefix), nil)
}

// setSection replaces the Data of the named section if it exists, or appends it.
func setSection(arc *txtar.Archive, name string, data []byte) {
	for i, f := range arc.Files {
		if f.Name == name {
			arc.Files[i].Data = data
			return
		}
	}
	arc.Files = append(arc.Files, txtar.File{Name: name, Data: data})
}

// writeArchive writes the archive back to path.
func writeArchive(t *testing.T, path string, arc *txtar.Archive) {
	t.Helper()
	if err := os.WriteFile(path, txtar.Format(arc), 0644); err != nil {
		t.Fatalf("writeArchive %s: %v", path, err)
	}
}
