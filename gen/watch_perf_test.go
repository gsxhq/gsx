package gen

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/gsxhq/gsx/internal/sourceview"
)

// TestWatchSession_ColdStartParseWorkIsLinear pins the parse-work complexity of
// watch-session startup and warm regeneration. Cold startup over a module with
// K package dirs and F total .gsx files must Inspect each file O(1) times —
// not once per directory. The historical failure mode (74342b54 + whole-module
// manifest reconstruction) re-Inspected all F files inside every per-dir
// refresh, making startup and dep-surface reopen O(K × F): ~26s on a
// 78-dir/1169-file module that generates in ~5s via the batch path.
//
// Deliberately NOT t.Parallel(): sourceview.InspectCalls is a process-wide
// counter, and a non-parallel test has the package's only running slot, so the
// deltas below are attributable to this session alone.
func TestWatchSession_ColdStartParseWorkIsLinear(t *testing.T) {
	root := t.TempDir()
	write := func(p, s string) {
		full := filepath.Join(root, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module example.com/m\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+gsxModuleDir(t)+"\n")

	const dirs = 12
	const filesPerDir = 3
	const files = dirs * filesPerDir
	for d := range dirs {
		pkg := fmt.Sprintf("p%02d", d)
		for f := range filesPerDir {
			write(
				filepath.Join(pkg, fmt.Sprintf("c%d.gsx", f)),
				fmt.Sprintf("package %s\n\ncomponent C%d() {\n\t<p>x</p>\n}\n", pkg, f),
			)
		}
	}

	before := sourceview.InspectCalls()
	s, startup, err := startWatchSessionForTest(watchConfig{paths: []string{root}})
	if err != nil {
		t.Fatalf("startWatchSessionForTest: %v", err)
	}
	for _, r := range startup {
		if !r.OK {
			t.Fatalf("startup regen not OK for %s: err=%v diags=%v", r.Dir, r.Err, r.Diags)
		}
	}
	coldDelta := sourceview.InspectCalls() - before

	// Each of the F files may legitimately be Inspected a small constant number
	// of times during startup (manifest build plus bounded derivations). The
	// quadratic regression Inspects each file once per package dir, i.e.
	// ≥ dirs×files = 12F, far above this bound.
	const coldBudget = 6 * files
	if coldDelta > coldBudget {
		t.Fatalf("cold startup performed %d Inspect calls over %d files in %d dirs; budget is %d (O(files)) — per-dir whole-module re-parse regression",
			coldDelta, files, dirs, coldBudget)
	}

	// A warm single-dir regeneration must not re-parse the whole module either:
	// its parse work is bounded by the refreshed dir's own files (plus a small
	// constant), independent of module size.
	before = sourceview.InspectCalls()
	r := s.regenDir(filepath.Join(root, "p00"))
	if !r.OK {
		t.Fatalf("warm regenDir not OK: err=%v diags=%v", r.Err, r.Diags)
	}
	warmDelta := sourceview.InspectCalls() - before
	const warmBudget = 6*filesPerDir + 6
	if warmDelta > warmBudget {
		t.Fatalf("warm single-dir regen performed %d Inspect calls; budget is %d (O(dir files)) — whole-module re-parse regression",
			warmDelta, warmBudget)
	}
}
