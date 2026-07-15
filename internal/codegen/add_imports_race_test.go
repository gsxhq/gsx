package codegen

import (
	"sync"
	"testing"
)

// TestResolveImportCandidatesConcurrent guards the authoritative snapshot and
// importer caches under `go test -race`. ResolveImportCandidates now shares
// analysisMu with Package/Generate for its complete enumeration and optional
// source recheck, so neither the Module FileSet nor package identities can be
// rebuilt between candidate naming and symbol filtering.
//
// Each of {"rand", "Read"}, {"template", "HTML"}, {"json", "Marshal"},
// {"scanner", "Scanner"} is genuinely ambiguous (see stdlibindex_gen.go), so
// resolving any of them forces ResolveImportCandidates into packageExports'
// "not already in the dep graph" branch, which is what exercises the
// export-data importer path where both races live.
//
// This test only fails under -race; that is its entire purpose as a
// regression guard for this class of bug, not a functional assertion.
func TestResolveImportCandidatesConcurrent(t *testing.T) {
	m, dir := newMissingModule(t, "package u\n\nvar xx = <p>hi</p>\n")

	type query struct{ name, symbol string }
	queries := []query{
		{"rand", "Read"},
		{"template", "HTML"},
		{"json", "Marshal"},
		{"scanner", "Scanner"},
	}

	const n = 40
	var wg sync.WaitGroup
	wg.Add(n)
	for i := range n {
		q := queries[i%len(queries)]
		go func() {
			defer wg.Done()
			m.ResolveImportCandidates(dir, q.name, q.symbol)
		}()
	}
	wg.Wait()
}

func TestResolveImportCandidatesConcurrentWithPackageAnalysis(t *testing.T) {
	m, dir := newMissingModuleFiles(t, "views", "package views\n\nvar xx = <p>hi</p>\n", map[string]string{
		"a/card.gsx": `package db

func Current() string { return "current" }

component Card() { <p/> }
`,
		"b/db.go": "package db\n\nfunc Other() string { return \"other\" }\n",
	})

	const n = 24
	var wg sync.WaitGroup
	wg.Add(n)
	for i := range n {
		go func() {
			defer wg.Done()
			if i%2 == 0 {
				got := m.ResolveImportCandidates(dir, "db", "Current")
				if len(got) != 1 || got[0] != "example.com/u/a" {
					t.Errorf("resolve(db, Current) = %v, want authoritative local package", got)
				}
				return
			}
			if _, err := m.Package(dir); err != nil {
				t.Errorf("Package: %v", err)
			}
		}()
	}
	wg.Wait()
}
