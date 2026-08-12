package gen

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/gsxhq/gsx/internal/codegen"
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
// deltas below are attributable to this session — except for goroutines leaked
// by EARLIER tests in this binary (gen's LSP e2e harness runs in-process, and
// its background analyzer calls Inspect). A leak is a one-shot burst, not a
// steady stream, so a budget breach is retried once in a fresh module: a real
// complexity regression breaches every window, foreign traffic at most one.
func TestWatchSession_ColdStartParseWorkIsLinear(t *testing.T) {
	const dirs = 12
	const filesPerDir = 3
	const files = dirs * filesPerDir
	// Each of the F files may legitimately be Inspected a small constant number
	// of times during startup (manifest build plus bounded derivations). The
	// quadratic regression Inspects each file once per package dir, i.e.
	// ≥ dirs×files = 12F, far above this bound. Warm single-dir regen parse
	// work is bounded by the dir's own files, independent of module size.
	const coldBudget = 6 * files
	const warmBudget = 6*filesPerDir + 6

	measure := func() (coldDelta, warmDelta uint64) {
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
		coldDelta = sourceview.InspectCalls() - before

		before = sourceview.InspectCalls()
		r := s.regenDir(filepath.Join(root, "p00"))
		if !r.OK {
			t.Fatalf("warm regenDir not OK: err=%v diags=%v", r.Err, r.Diags)
		}
		warmDelta = sourceview.InspectCalls() - before
		return coldDelta, warmDelta
	}

	cold, warm := measure()
	if cold > coldBudget || warm > warmBudget {
		cold2, warm2 := measure()
		cold, warm = min(cold, cold2), min(warm, warm2)
	}
	if cold > coldBudget {
		t.Fatalf("cold startup performed %d Inspect calls over %d files in %d dirs; budget is %d (O(files)) — per-dir whole-module re-parse regression",
			cold, files, dirs, coldBudget)
	}
	if warm > warmBudget {
		t.Fatalf("warm single-dir regen performed %d Inspect calls; budget is %d (O(dir files)) — whole-module re-parse regression",
			warm, warmBudget)
	}
}

// TestWatchSession_EditLoadBudget pins the go-list call budget (not parse
// work — see TestWatchSession_ColdStartParseWorkIsLinear above) of warm edit
// cycles after a settled cold start. Module layout: dep/ (go-only, no .gsx),
// page/ (imports dep), and 10 filler .gsx dirs unrelated to either.
//
//   - a .gsx body edit must issue ZERO packages.Load calls: the cold external
//     importer stays cached, and Generate re-type-checks the edited dir from
//     retained source only.
//   - a .go body edit forces exactly ONE in-place world reload (Task 1/2's
//     lazy externalImporter reload, not a full session reopen), which costs a
//     small constant number of packages.Load calls — see goEditLoadBudget
//     below for what that constant comprises.
//   - a second .go edit cycle must cost the SAME constant: no drift toward
//     the old reopen-every-time behavior.
//
// Deliberately NOT t.Parallel(): codegen.ProjectLoadCalls is a process-wide
// counter (same discipline as sourceview.InspectCalls above), so a
// non-parallel test has the package's only running slot. A budget breach is
// retried once in a fresh module, taking the min of two — a real complexity
// regression breaches every window, foreign packages.Load traffic from a
// leaked background goroutine at most one.
func TestWatchSession_EditLoadBudget(t *testing.T) {
	const fillerDirs = 10

	// goEditLoadBudget is the exact number of packages.Load calls ONE in-place
	// world reload performs on this fixture: loadExternalGraph's project-half
	// load (sharedworld.go, one call covering "./..." — the WHOLE module in a
	// single call, not one per dir) plus zero calls for the shared gsx-runtime
	// closure, which loadSharedWorld already cached fresh from cold start. The
	// project-half load is the only per-reload cost because this fixture's
	// Options carry no filters/renderers/aliases/class-merger, so
	// sharedWorldEligible stays true and no fallback full load triggers. It is
	// independent of fillerDirs: "./..." is one packages.Load call regardless
	// of how many directories it walks, which is the invariant this test
	// exists to pin — a regression toward reopen-per-dir would scale with
	// fillerDirs instead of staying flat.
	const goEditLoadBudget = 1

	measure := func() (gsxDelta, goDelta1, goDelta2 uint64) {
		root := t.TempDir()
		writeMod(t, root)
		writeFileT(t, filepath.Join(root, "dep", "dep.go"),
			"package dep\n\nfunc Value() string { return \"v1\" }\n")
		writeFileT(t, filepath.Join(root, "page", "page.gsx"),
			"package page\n\nimport \"example.com/m/dep\"\n\ncomponent Page() {\n\t<p>{dep.Value()}</p>\n}\n")
		for i := range fillerDirs {
			pkg := fmt.Sprintf("filler%02d", i)
			writeFileT(t, filepath.Join(root, pkg, "c.gsx"),
				fmt.Sprintf("package %s\n\ncomponent C() {\n\t<p>x</p>\n}\n", pkg))
		}

		depDir := filepath.Join(root, "dep")
		pageDir := filepath.Join(root, "page")

		sess, startup, err := startWatchSessionForTest(watchConfig{paths: []string{root}})
		if err != nil {
			t.Fatalf("startWatchSessionForTest: %v", err)
		}
		for _, r := range startup {
			if !r.OK {
				t.Fatalf("startup regen not OK: dir=%s err=%v diags=%v", r.Dir, r.Err, r.Diags)
			}
		}

		// (a) .gsx body edit — no import/membership change.
		writeFileT(t, filepath.Join(pageDir, "page.gsx"),
			"package page\n\nimport \"example.com/m/dep\"\n\ncomponent Page() {\n\t<p>v2 {dep.Value()}</p>\n}\n")
		dirty := newWatchDirtySet()
		dirty.dirs[pageDir] = true
		before := codegen.ProjectLoadCalls()
		results, _, err := dirty.regenerate(sess.regenPending)
		if err != nil {
			t.Fatalf("regenerate after gsx edit: %v", err)
		}
		for _, r := range results {
			if !r.OK {
				t.Fatalf("gsx-edit regen not OK: dir=%s err=%v diags=%v", r.Dir, r.Err, r.Diags)
			}
		}
		gsxDelta = codegen.ProjectLoadCalls() - before

		// (b) .go body edit — same signature, changed literal.
		writeFileT(t, filepath.Join(depDir, "dep.go"),
			"package dep\n\nfunc Value() string { return \"v2\" }\n")
		dirty = newWatchDirtySet()
		dirty.dirs[depDir] = true
		dirty.goDirty = true
		before = codegen.ProjectLoadCalls()
		results, rebuild, err := dirty.regenerate(sess.regenPending)
		if err != nil {
			t.Fatalf("regenerate after go edit: %v", err)
		}
		if !rebuild {
			t.Fatal("regenerate()'s rebuild return must be true for a goDirty cycle")
		}
		for _, r := range results {
			if !r.OK {
				t.Fatalf("go-edit regen not OK: dir=%s err=%v diags=%v", r.Dir, r.Err, r.Diags)
			}
		}
		goDelta1 = codegen.ProjectLoadCalls() - before

		// (c) a second .go edit cycle — must cost the same constant, not drift
		// toward reopen-like behavior.
		writeFileT(t, filepath.Join(depDir, "dep.go"),
			"package dep\n\nfunc Value() string { return \"v3\" }\n")
		dirty = newWatchDirtySet()
		dirty.dirs[depDir] = true
		dirty.goDirty = true
		before = codegen.ProjectLoadCalls()
		results, rebuild, err = dirty.regenerate(sess.regenPending)
		if err != nil {
			t.Fatalf("regenerate after second go edit: %v", err)
		}
		if !rebuild {
			t.Fatal("regenerate()'s rebuild return must be true for the second goDirty cycle too")
		}
		for _, r := range results {
			if !r.OK {
				t.Fatalf("second go-edit regen not OK: dir=%s err=%v diags=%v", r.Dir, r.Err, r.Diags)
			}
		}
		goDelta2 = codegen.ProjectLoadCalls() - before
		return gsxDelta, goDelta1, goDelta2
	}

	gsxDelta, goDelta1, goDelta2 := measure()
	if gsxDelta != 0 || goDelta1 != goEditLoadBudget || goDelta2 != goEditLoadBudget {
		gsxDelta2, goDelta1b, goDelta2b := measure()
		gsxDelta = min(gsxDelta, gsxDelta2)
		goDelta1 = min(goDelta1, goDelta1b)
		goDelta2 = min(goDelta2, goDelta2b)
	}
	if gsxDelta != 0 {
		t.Errorf(".gsx body edit issued %d packages.Load calls, want 0 — a body-only edit must stay on the cached external importer", gsxDelta)
	}
	if goDelta1 != goEditLoadBudget {
		t.Errorf(".go body edit issued %d packages.Load calls, want exactly %d (one in-place world reload's project-half load)", goDelta1, goEditLoadBudget)
	}
	if goDelta2 != goEditLoadBudget {
		t.Errorf("second .go body edit issued %d packages.Load calls, want the same %d as the first — no drift toward reopen-per-cycle behavior", goDelta2, goEditLoadBudget)
	}
}
