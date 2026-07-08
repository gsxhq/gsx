package corpus

import (
	"bytes"
	"flag"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
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

	// Classify cases and collect codegen candidates for ONE batchCodegen call.
	// A candidate is any case that is NOT a parse-error case and NOT a parser-layer case.
	type classification struct {
		single      bool
		parserDiag  []byte
		astDump     []byte
		isCandidate bool
	}
	classif := make(map[string]*classification, len(cases))
	var candidates []*caseDoc

	for _, c := range cases {
		astDump, parserDiag, single := c.astAndParserDiag()
		cl := &classification{
			single:     single,
			parserDiag: parserDiag,
			astDump:    astDump,
		}
		if single && len(parserDiag) > 0 {
			// parse-error case — no codegen
			cl.isCandidate = false
		} else if single && hasAstGolden(c) {
			// parser-layer snapshot — no codegen
			cl.isCandidate = false
		} else {
			cl.isCandidate = true
			candidates = append(candidates, c)
		}
		classif[c.name] = cl
	}

	// Single batchCodegen call for candidate cases that can match this test run.
	// This keeps focused runs (e.g. -run TestCorpus/attrs/spread_byo) from
	// paying the full-corpus codegen cost before subtest filtering applies.
	casesForBatch := candidates
	if selected := selectedCaseNamesForRun("TestCorpus", cases); selected != nil {
		casesForBatch = nil
		for _, c := range candidates {
			if selected[c.name] {
				casesForBatch = append(casesForBatch, c)
			}
		}
	}

	cg, err := batchCodegen(repoRoot, casesForBatch)
	if err != nil {
		t.Fatalf("batchCodegen: %v", err)
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cl := classif[c.name]
			astDump := cl.astDump
			parserDiag := cl.parserDiag
			single := cl.single

			// Resolve diagnostics + generated facets.
			var diagGot, genGot []byte
			switch {
			case single && len(parserDiag) > 0:
				diagGot = parserDiag // parser-error case; no codegen
			case single && hasAstGolden(c):
				// parser-layer snapshot (pins ast.golden): parse + AST + parser
				// diagnostics only; codegen stays in the parser layer per spec §2.
				diagGot = parserDiag
			default:
				if r := cg[c.name]; r != nil {
					diagGot, genGot = r.diag, r.gen
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
				if r := cg[c.name]; r != nil {
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
	want := c.goldens[sec]
	if !bytes.Equal(got, want) {
		t.Errorf("%s: %s mismatch\n--- got ---\n%s\n--- want ---\n%s", c.name, sec, got, want)
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

// selectedCaseNamesForRun returns the subset of case names that can match the
// active -test.run pattern for a given top-level test (e.g. "TestCorpus").
// It returns nil when there is no subtest filter, meaning "run all".
func selectedCaseNamesForRun(testName string, cases []*caseDoc) map[string]bool {
	f := flag.Lookup("test.run")
	if f == nil {
		return nil
	}
	pattern := strings.TrimSpace(f.Value.String())
	if pattern == "" {
		return nil
	}

	parts := splitRunPattern(pattern)
	if len(parts) < 2 {
		return nil // no subtest component in -run
	}
	topRE, err := regexp.Compile(parts[0])
	if err != nil || !topRE.MatchString(testName) {
		return nil
	}

	subRE := make([]*regexp.Regexp, 0, len(parts)-1)
	for _, p := range parts[1:] {
		re, err := regexp.Compile(p)
		if err != nil {
			return nil
		}
		subRE = append(subRE, re)
	}

	selected := map[string]bool{}
	for _, c := range cases {
		segments := strings.Split(c.name, "/")
		if len(subRE) > len(segments) {
			continue
		}
		ok := true
		for i, re := range subRE {
			if !re.MatchString(segments[i]) {
				ok = false
				break
			}
		}
		if ok {
			selected[c.name] = true
		}
	}
	return selected
}

// splitRunPattern splits a go test -run pattern into slash-separated regex
// components while respecting escaped slashes and character classes.
func splitRunPattern(pattern string) []string {
	var (
		parts   []string
		buf     strings.Builder
		escaped bool
		inClass bool
	)
	for _, r := range pattern {
		switch {
		case escaped:
			buf.WriteRune(r)
			escaped = false
		case r == '\\':
			escaped = true
			buf.WriteRune(r)
		case r == '[':
			inClass = true
			buf.WriteRune(r)
		case r == ']' && inClass:
			inClass = false
			buf.WriteRune(r)
		case r == '/' && !inClass:
			parts = append(parts, buf.String())
			buf.Reset()
		default:
			buf.WriteRune(r)
		}
	}
	parts = append(parts, buf.String())
	return parts
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
