package codegen

import (
	"sync"
	"testing"
)

// TestResolveImportCandidatesConcurrent guards against two data races an
// adversarial review found under `go test -race`, both invisible to every
// other (non-concurrent) test in this package:
//
//  1. packageExports called the cached go/importer's .Import() outside any
//     lock. go/importer's gc importer (go/internal/gcimporter) mutates its own
//     internal package cache during Import, so two concurrent resolves
//     corrupted it (see importExportData in add_imports.go, now serialized by
//     Module.gcImporterMu).
//  2. externalImporter's double-checked lock on m.ext read the field back
//     AFTER releasing m.mu — a non-atomic double-checked lock. It was masked
//     because every prior caller held analysisMu, serializing writes and
//     reads; ResolveImportCandidates deliberately does not, exposing it (see
//     the "return ext, nil" fix in module.go's externalImporter).
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
