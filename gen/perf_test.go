package gen

// TestPerfBaseline measures the real Analyze path (lspAnalyzer.Analyze →
// codegen.GeneratePackagesWithFilters) on a large synthetic fixture.
//
// It is INTENTIONALLY excluded from the normal test suite: set GSX_PERF=1 to
// run it.
//
//   GSX_PERF=1 go test ./gen/ -run TestPerfBaseline -v -count=1

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"
	"unsafe"

	"github.com/gsxhq/gsx/internal/lsp"
)

func TestPerfBaseline(t *testing.T) {
	if os.Getenv("GSX_PERF") == "" {
		t.Skip("set GSX_PERF=1 to run")
	}

	const N = 50         // packages
	const gsxPerPkg = 4  // .gsx components per package
	const refsPerPkg = 1 // .go files per package (each references all components)

	dir := t.TempDir()
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}

	// ---------- build synthetic fixture ----------
	mustWrite := func(path, content string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	gomod := fmt.Sprintf(
		"module perf.example.com\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => %s\n",
		root,
	)
	mustWrite(filepath.Join(dir, "go.mod"), gomod)

	// One package per pkg index: dir/pkgN/
	// Component names avoid underscores — the gsx parser requires identifiers
	// that match Go's letter-start rule but the gsx lexer rejects underscores
	// mid-identifier in component names (they stop at the '_' expecting '{').
	// Use a letter-only suffix: CompA, CompB, CompC, CompD, ...
	const alpha = "ABCDEFGHIJKLMNOPQRSTUVWXYZ"
	pkgDirs := make([]string, N)
	for i := range N {
		pkgName := fmt.Sprintf("pkg%d", i)
		pkgDir := filepath.Join(dir, pkgName)
		pkgDirs[i] = pkgDir

		// Write gsxPerPkg .gsx files — names like CompAA, CompAB, ...
		var compNames []string
		for j := range gsxPerPkg {
			// Build a short alphabetic suffix so every name is unique across packages.
			suffix := string(alpha[i%len(alpha)]) + string(alpha[j%len(alpha)])
			compName := "Comp" + suffix
			compNames = append(compNames, compName)
			gsxSrc := fmt.Sprintf(
				"package %s\n\ncomponent %s(title string) {\n\t<div>{ title }</div>\n}\n",
				pkgName, compName,
			)
			mustWrite(filepath.Join(pkgDir, strings.ToLower(compName)+".gsx"), gsxSrc)
		}

		// Write one .go file referencing all components (creates CrossIndex refs)
		var refs []string
		for _, cn := range compNames {
			refs = append(refs, fmt.Sprintf("\t_ = %s", cn))
		}
		goSrc := fmt.Sprintf(
			"package %s\n\nfunc use() {\n%s\n}\n",
			pkgName, strings.Join(refs, "\n"),
		)
		mustWrite(filepath.Join(pkgDir, "main.go"), goSrc)
	}

	totalGSXFiles := N * gsxPerPkg
	totalGoFiles := N * refsPerPkg
	t.Logf("fixture: %d packages, %d .gsx files, %d .go files", N, totalGSXFiles, totalGoFiles)

	a := lspAnalyzer{}

	// ---------- cold pass: measure per-package latency ----------
	latencies := make([]time.Duration, N)
	pkgs := make([]*lsp.Package, N) // hold results to measure memory

	// Force GC before baseline memory reading
	runtime.GC()
	runtime.GC()
	var msBefore runtime.MemStats
	runtime.ReadMemStats(&msBefore)

	for i, pkgDir := range pkgDirs {
		start := time.Now()
		pkg, err := a.Analyze(pkgDir, nil)
		latencies[i] = time.Since(start)
		if err != nil {
			t.Fatalf("Analyze pkg%d: %v", i, err)
		}
		pkgs[i] = pkg
	}

	// ---------- memory after holding all packages ----------
	runtime.GC()
	runtime.GC()
	var msAfter runtime.MemStats
	runtime.ReadMemStats(&msAfter)

	heapDelta := int64(msAfter.HeapInuse) - int64(msBefore.HeapInuse)
	allocDelta := int64(msAfter.TotalAlloc) - int64(msBefore.TotalAlloc)

	// ---------- cross-index size ----------
	totalCrossEntries := 0
	for _, pkg := range pkgs {
		if pkg != nil {
			totalCrossEntries += len(pkg.CrossIndex)
		}
	}

	// Approximate slim index size: each CrossRef holds Name(~10B), Decl(token.Position=48B),
	// Refs ([]token.Position — usually 1-2 .go refs: 48B each).
	// Use unsafe.Sizeof as a rough bound.
	var cr lsp.CrossRef
	crossRefSize := int(unsafe.Sizeof(cr))
	slimIndexBytes := totalCrossEntries * crossRefSize
	_ = slimIndexBytes

	// ---------- warm pass: second Analyze of same packages ----------
	warmLatencies := make([]time.Duration, N)
	for i, pkgDir := range pkgDirs {
		start := time.Now()
		_, err := a.Analyze(pkgDir, nil)
		warmLatencies[i] = time.Since(start)
		if err != nil {
			t.Fatalf("warm Analyze pkg%d: %v", i, err)
		}
	}

	// ---------- definition/references lookup latency (index-only) ----------
	// Time an ACTUAL CrossIndex map lookup by component key (the real query-path
	// operation), averaged over many iterations so we time the lookup, not the
	// clock. The handlers then range the entry's Refs — also O(refs).
	var defLatency, refLatency time.Duration
	for _, pkg := range pkgs {
		if pkg == nil || len(pkg.CrossIndex) == 0 {
			continue
		}
		var key string
		for k := range pkg.CrossIndex {
			key = k
			break
		}
		const iters = 100000
		start := time.Now()
		var sink int
		for i := 0; i < iters; i++ {
			cr := pkg.CrossIndex[key] // real map lookup
			sink += len(cr.Refs)
		}
		defLatency = time.Since(start) / iters
		refLatency = defLatency // same operation (lookup + range over Refs)
		_ = sink
		break
	}

	// ---------- report ----------
	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	sort.Slice(warmLatencies, func(i, j int) bool { return warmLatencies[i] < warmLatencies[j] })
	median := func(s []time.Duration) time.Duration { return s[len(s)/2] }

	t.Logf("=== cold Analyze latency ===")
	t.Logf("  min    = %v", latencies[0])
	t.Logf("  median = %v", median(latencies))
	t.Logf("  max    = %v", latencies[len(latencies)-1])
	t.Logf("  total  = %v  (all %d packages)", sumDurations(latencies), N)

	t.Logf("=== warm Analyze latency (2nd call, same pkgs) ===")
	t.Logf("  min    = %v", warmLatencies[0])
	t.Logf("  median = %v", median(warmLatencies))
	t.Logf("  max    = %v", warmLatencies[len(warmLatencies)-1])

	t.Logf("=== definition / references lookup latency ===")
	t.Logf("  definition (index lookup)  = %v", defLatency)
	t.Logf("  references (index lookup)  = %v", refLatency)

	t.Logf("=== retained memory ===")
	t.Logf("  heap delta (HeapInuse)  = %s", fmtBytes(heapDelta))
	t.Logf("  total alloc delta       = %s", fmtBytes(allocDelta))
	t.Logf("  per package (HeapInuse) = %s", fmtBytes(heapDelta/int64(N)))

	t.Logf("=== cross-index (slim) ===")
	t.Logf("  total entries      = %d  (%d components × %d pkgs)", totalCrossEntries, gsxPerPkg, N)
	t.Logf("  CrossRef struct sz = %d bytes", crossRefSize)
	t.Logf("  slim index (est.)  = %s", fmtBytes(int64(slimIndexBytes)))
	t.Logf("  slim vs heap delta = %.1f%%", 100*float64(slimIndexBytes)/float64(max(heapDelta, 1)))
}

func sumDurations(s []time.Duration) time.Duration {
	var total time.Duration
	for _, d := range s {
		total += d
	}
	return total
}

func fmtBytes(n int64) string {
	switch {
	case n < 0:
		return fmt.Sprintf("-%s", fmtBytes(-n))
	case n < 1024:
		return fmt.Sprintf("%d B", n)
	case n < 1024*1024:
		return fmt.Sprintf("%.1f KiB", float64(n)/1024)
	default:
		return fmt.Sprintf("%.1f MiB", float64(n)/1024/1024)
	}
}
