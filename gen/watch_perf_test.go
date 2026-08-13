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
//     lazy externalImporter reload), which costs a small constant number of
//     packages.Load calls — see goEditLoadBudget below for what that constant
//     comprises. The constant pins that load cost is dir-count-independent
//     and does not become per-dir or per-cycle; it does NOT by itself
//     distinguish scoped-closure regen from a full reopen() — on this fixture
//     a reopen also costs exactly one packages.Load (openModule is lazy, and
//     the first Generate's "./..." load caches into m.ext for every later
//     dir). Routing scope — that a .go edit regenerates only the dependent
//     closure, never the whole module — is separately pinned by
//     TestWatchSession_GoEditRegeneratesOnlyDependents (asserts results are
//     scoped to page, never other/).
//   - a second .go edit cycle must cost the SAME constant: the reload cost
//     does not grow across repeated cycles.
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
	// sharedWorldComposition composes to exactly {runtime, std} (ok=true) and
	// no fallback full load triggers. It is
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

		// (c) a second .go edit cycle — must cost the same constant as the
		// first, not grow across repeated cycles.
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
		t.Errorf("second .go body edit issued %d packages.Load calls, want the same %d as the first — reload cost must not grow across repeated cycles", goDelta2, goEditLoadBudget)
	}
}

// writeConfiguredModuleWorldBudgetFixture stages the CONFIGURED-module fixture
// the design's acceptance gate #2 names (docs/superpowers/specs/2026-08-13-
// project-shared-world-design.md), in the shape gsxui actually has:
//
//   - an in-module class merger (mrg/, gsxui's merge/): main-module code, so it
//     composes into NO world and its types come from retained source;
//   - an out-of-module filter package (filters/, its own module, the
//     structpages shape): this is what the config tier is made of;
//   - an ordinary project dependency (dep/) that imports a package no
//     configuration names (gsx/parser — the runtime is stdlib-only, so the
//     config tier cannot carry it): this is what the EXTENSION tier is made of,
//     gsxui's document.gsx → github.com/gsxhq/vite.
//
// views/ exercises all three: the merger via a composite class attribute, the
// filter via a `|> shout` pipeline, and dep via an ordinary .gsx import.
func writeConfiguredModuleWorldBudgetFixture(t *testing.T, root, modName string) (modPath, filterPath, mrgDir, depDir, viewsDir string) {
	t.Helper()
	modPath = "example.com/" + modName
	// The filter package lives in its OWN module, so it actually composes into
	// the config tier. A main-module filter package would not: main-module code
	// never enters a world, which would collapse tier one to the base pair and
	// leave this gate measuring nothing about configuration at all.
	filterPath = "example.com/" + modName + "filters"
	writeFileT(t, filepath.Join(root, "go.mod"),
		"module "+modPath+"\n\ngo 1.26.1\n\nrequire (\n\tgithub.com/gsxhq/gsx v0.0.0\n\t"+filterPath+" v0.0.0\n)\n\nreplace github.com/gsxhq/gsx => "+gsxModuleDir(t)+"\n\nreplace "+filterPath+" => ./filters\n")
	mrgDir = filepath.Join(root, "mrg")
	filterDir := filepath.Join(root, "filters")
	depDir = filepath.Join(root, "dep")
	viewsDir = filepath.Join(root, "views")
	writeFileT(t, filepath.Join(filterDir, "go.mod"), "module "+filterPath+"\n\ngo 1.26.1\n")
	writeFileT(t, filepath.Join(mrgDir, "mrg.go"),
		"package mrg\n\nimport \"strings\"\n\nfunc Merge(classes []string) string { return strings.Join(classes, \" \") }\n")
	writeFileT(t, filepath.Join(filterDir, "filters.go"),
		"package filters\n\nimport \"strings\"\n\nfunc Shout(s string) string { return strings.ToUpper(s) + \"!\" }\n")
	// dep imports a package NO configuration names and the base closure does not
	// carry (the gsx runtime is stdlib-only; parser is a tool package). That is
	// gsxui's shape — site/pages/document.gsx importing github.com/gsxhq/vite —
	// and it is what makes this fixture exercise the world's EXTENSION tier
	// rather than the config tier alone. Before the extension existed such a
	// module could not be served at all: it paid the world load, the project-half
	// load AND the full load, on every cycle.
	writeFileT(t, filepath.Join(depDir, "dep.go"),
		"package dep\n\nimport \"github.com/gsxhq/gsx/parser\"\n\nvar _ parser.Mode\n\nfunc Value() string { return \"extra\" }\n")
	writeFileT(t, filepath.Join(viewsDir, "card.gsx"),
		"package views\n\nimport \""+modPath+"/dep\"\n\ncomponent Card() {\n\t<div class={ \"card\", dep.Value() }>{dep.Value() |> shout}</div>\n}\n")
	return modPath, filterPath, mrgDir, depDir, viewsDir
}

// TestWatchSession_ConfiguredModuleWorldBudget pins Task 4's acceptance gate
// (docs/superpowers/specs/2026-08-13-project-shared-world-design.md,
// "Acceptance gates" #2): a CONFIGURED module — an out-of-module filter package
// composed into the config tier, a class merger the main module owns, and a
// dependency reached only through the extension tier — must hold the same
// world discipline TestWatchSharedWorld_UnrelatedEditsLeaveWorldCold pins for
// the unconfigured fixture, PLUS the payoff that motivates this whole phase: a
// second Module opened in this process over the SAME root and configuration
// must reuse the cached world instead of reloading it.
//
//   - cold start (the session's first Generate) performs exactly TWO world
//     LOOKUPS, one per tier. Lookups, not loads: every loadSharedWorld call
//     increments exactly one of SharedWorldLoads or SharedWorldHits, so their
//     sum counts calls regardless of what an earlier test in the process left
//     in the cache. Counting loads alone is worthless here — neither tier's key
//     mentions this fixture's temp root, so a sibling test of the same shape
//     pre-warms both entries and every load-delta assertion passes vacuously
//     (the retry-and-take-the-minimum below makes that certain, since the
//     second window is all hits). Three lookups would mean a third,
//     differently-keyed world, which is the regression this bound exists for:
//     getting the config tier to one lookup required routing
//     prepareWatchSession's class-merger validation through the session's own
//     already-open Module (codegen.Module.ValidateConfiguredMergers) instead of
//     a throwaway probe Module scoped to just the merger, whose narrower
//     composition used to mint a second, differently-keyed world at every
//     configured-module session startup;
//   - a .go edit OUTSIDE the composed closure (dep/, an ordinary project
//     dependency, mirroring TestWatchSharedWorld_UnrelatedEditsLeaveWorldCold's
//     shape) forces the usual one in-place project-half reload
//     (TestWatchSession_EditLoadBudget's goEditLoadBudget) but must NOT
//     reload the world itself — dep.go is not one of the world's stamped
//     files;
//   - opening a SECOND watch session over the identical root and cfg — the
//     same class merger and filter package, so sharedWorldKey computes the
//     same key — must serve the process-wide cached world: zero further
//     world loads, and at least one recorded world HIT (SharedWorldHits),
//     the process-cache payoff this phase exists to prove.
//
// Deliberately NOT t.Parallel(): codegen.SharedWorldLoads, codegen.
// SharedWorldHits, and codegen.ProjectLoadCalls are all process-wide
// counters (same discipline as TestWatchSession_EditLoadBudget and every
// other counter-reading test in this package — see gen/watch_sharedworld_
// test.go's comment block, memorialized after a t.Parallel() regression on a
// ">= 1" world-load bound). A non-parallel test has the package's only
// running slot for the assertions below, which a concurrent sibling test's
// own packages.Load or cache-hit traffic could otherwise falsely trip in
// either direction. A budget breach is retried once in a fresh module and
// the two measurements' minimum is taken for every delta, including the
// "at least 1" hit bound: a real regression (broken caching) reports 0 hits
// in BOTH windows, so min(0, 0) still fails correctly; foreign traffic can
// only inflate a measurement, so taking the min never manufactures a false
// pass — same reasoning as TestWatchSession_EditLoadBudget's retry.
func TestWatchSession_ConfiguredModuleWorldBudget(t *testing.T) {
	// goEditLoadBudget mirrors TestWatchSession_EditLoadBudget's constant of
	// the same name: loadExternalGraph's project-half load is the only
	// packages.Load call an in-place world reload performs, whether or not
	// the module carries a class merger and filter package — sharedWorldComposition
	// widens the composed PATH SET, not the number of load calls.
	const goEditLoadBudget = 1

	// worldLookups counts loadSharedWorld CALLS: each one increments exactly one
	// of the two counters, so the sum is order-independent where either counter
	// alone is not.
	worldLookups := func() uint64 {
		return uint64(codegen.SharedWorldLoads() + codegen.SharedWorldHits())
	}

	measure := func() (coldLookupDelta, goEditWorldDelta, goEditProjDelta, secondWorldDelta, secondHitDelta uint64) {
		root := t.TempDir()
		modPath, filterPath, _, depDir, viewsDir := writeConfiguredModuleWorldBudgetFixture(t, root, "wwbudget")
		cfg := watchConfig{
			paths:       []string{viewsDir},
			filterPkgs:  []string{filterPath},
			classMerger: &codegen.ClassMergerRef{PkgPath: modPath + "/mrg", FuncName: "Merge"},
		}

		beforeCold := worldLookups()
		sess, startup, err := startWatchSessionForTest(cfg)
		if err != nil {
			t.Fatalf("startWatchSessionForTest: %v", err)
		}
		for _, r := range startup {
			if !r.OK {
				t.Fatalf("startup regen not OK: dir=%s err=%v diags=%v", r.Dir, r.Err, r.Diags)
			}
		}
		coldLookupDelta = worldLookups() - beforeCold

		// .go edit OUTSIDE the world's composed closure.
		writeFileT(t, filepath.Join(depDir, "dep.go"), "package dep\n\nimport \"github.com/gsxhq/gsx/parser\"\n\nvar _ parser.Mode\n\nfunc Value() string { return \"extra2\" }\n")
		dirty := newWatchDirtySet()
		dirty.dirs[depDir] = true
		dirty.goDirty = true
		beforeWorld := codegen.SharedWorldLoads()
		beforeProj := codegen.ProjectLoadCalls()
		results, rebuild, err := dirty.regenerate(sess.regenPending)
		if err != nil {
			t.Fatalf("regenerate after dep edit: %v", err)
		}
		if !rebuild {
			t.Fatal("regenerate()'s rebuild return must be true for the dep-edit goDirty cycle")
		}
		for _, r := range results {
			if !r.OK {
				t.Fatalf("dep-edit regen not OK: dir=%s err=%v diags=%v", r.Dir, r.Err, r.Diags)
			}
		}
		goEditWorldDelta = uint64(codegen.SharedWorldLoads() - beforeWorld)
		goEditProjDelta = codegen.ProjectLoadCalls() - beforeProj

		// A second Module — a fresh watch session — over the SAME root and cfg.
		beforeWorld2 := codegen.SharedWorldLoads()
		beforeHits := codegen.SharedWorldHits()
		_, startup2, err := startWatchSessionForTest(cfg)
		if err != nil {
			t.Fatalf("second startWatchSessionForTest: %v", err)
		}
		for _, r := range startup2 {
			if !r.OK {
				t.Fatalf("second session startup regen not OK: dir=%s err=%v diags=%v", r.Dir, r.Err, r.Diags)
			}
		}
		secondWorldDelta = uint64(codegen.SharedWorldLoads() - beforeWorld2)
		secondHitDelta = uint64(codegen.SharedWorldHits() - beforeHits)
		return coldLookupDelta, goEditWorldDelta, goEditProjDelta, secondWorldDelta, secondHitDelta
	}

	coldLookupDelta, goEditWorldDelta, goEditProjDelta, secondWorldDelta, secondHitDelta := measure()
	if coldLookupDelta != 2 || goEditWorldDelta != 0 || goEditProjDelta != goEditLoadBudget || secondWorldDelta != 0 || secondHitDelta < 1 {
		c2, w2, p2, sw2, sh2 := measure()
		coldLookupDelta = min(coldLookupDelta, c2)
		goEditWorldDelta = min(goEditWorldDelta, w2)
		goEditProjDelta = min(goEditProjDelta, p2)
		secondWorldDelta = min(secondWorldDelta, sw2)
		secondHitDelta = min(secondHitDelta, sh2)
	}
	if coldLookupDelta != 2 {
		t.Errorf("configured-module cold start performed %d shared-world lookups, want exactly 2 (the config tier, then the extension that covers dep's out-of-config import)", coldLookupDelta)
	}
	if goEditWorldDelta != 0 {
		t.Errorf("dep.go edit (outside the composed closure) reloaded the shared world %d times, want 0", goEditWorldDelta)
	}
	if goEditProjDelta != goEditLoadBudget {
		t.Errorf("dep.go edit issued %d packages.Load calls, want exactly %d (one in-place world reload's project-half load)", goEditProjDelta, goEditLoadBudget)
	}
	if secondWorldDelta != 0 {
		t.Errorf("second Module open over the same root/config issued %d shared-world loads, want 0 — the process cache should have served it", secondWorldDelta)
	}
	if secondHitDelta < 1 {
		t.Errorf("second Module open over the same root/config recorded %d shared-world hits, want at least 1 — the process-cache payoff this phase exists for", secondHitDelta)
	}
}
