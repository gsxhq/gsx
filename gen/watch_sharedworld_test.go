package gen

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gsxhq/gsx/internal/codegen"
)

// writeSharedWorldFixture stages the ONE configured-module watch fixture the
// design's "Main-module config packages (the hard case)" section names: a
// class merger that lives in the MAIN module (mrg/ — gsxui's own merge/
// shape), an unrelated go-only project dependency (dep/), and a views
// package that depends on both — the merger implicitly (recordImports'
// configured-function edge, the same mechanism TestWatchSession_
// RendererEditInvalidatesConsumer exercises for renderers) and dep
// explicitly (an ordinary .gsx import). One fixture serves all three watch-
// cycle shapes below: a merger edit (inside the world's composed closure), a
// dep edit (inside the project but outside the world), and a pure .gsx edit
// (outside both).
func writeSharedWorldFixture(t *testing.T, root, modName string) (modPath, mrgDir, depDir, viewsDir string) {
	t.Helper()
	modPath = "example.com/" + modName
	writeFileT(t, filepath.Join(root, "go.mod"),
		"module "+modPath+"\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+gsxModuleDir(t)+"\n")
	mrgDir = filepath.Join(root, "mrg")
	depDir = filepath.Join(root, "dep")
	viewsDir = filepath.Join(root, "views")
	writeFileT(t, filepath.Join(mrgDir, "mrg.go"),
		"package mrg\n\nimport \"strings\"\n\nfunc Merge(classes []string) string { return strings.Join(classes, \" \") }\n")
	writeFileT(t, filepath.Join(depDir, "dep.go"),
		"package dep\n\nfunc Value() string { return \"extra\" }\n")
	writeFileT(t, filepath.Join(viewsDir, "card.gsx"),
		"package views\n\nimport \""+modPath+"/dep\"\n\ncomponent Card() {\n\t<div class={ \"card\", dep.Value() }>hi</div>\n}\n")
	return modPath, mrgDir, depDir, viewsDir
}

// writeSharedWorldRunner stages a tiny main package at root that renders
// views.Card() to stdout, so a `go run .` after a watch cycle proves what the
// watch cycle actually left on disk — not just that codegen's own diagnostics
// stayed clean.
func writeSharedWorldRunner(t *testing.T, root, modPath string) {
	t.Helper()
	writeFileT(t, filepath.Join(root, "main.go"), `package main

import (
	"context"
	"os"

	v "`+modPath+`/views"
)

func main() {
	_ = v.Card().Render(context.Background(), os.Stdout)
}
`)
}

func runSharedWorldRender(t *testing.T, root string) string {
	t.Helper()
	cmd := exec.Command("go", "run", ".")
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go run failed: %v\n%s", err, out)
	}
	return string(out)
}

// TestWatchSharedWorld_MergerEditProducesFreshBehavior pins behavior (a) of
// Task 3: editing the configured class merger's .go file — main-module code
// living INSIDE the shared world's composed closure — regenerates cleanly on
// the very next watch cycle and the rebuilt program's rendered output
// reflects the merger's new join separator.
//
// The generated card.x.go itself cannot show this: codegen never inlines a
// merger's behavior, it only ever emits a runtime reference
// (_gsxgw.Class(_gsxcm.Merge, ...); see
// internal/corpus/testdata/cases/class/merger_static.txtar), so the merger's
// body is invisible to a bytes-of-card.x.go comparison regardless of
// staleness. Two separate assertions are needed to actually pin the design's
// freshness claim:
//
//   - SharedWorldLoads increases, proving loadSharedWorld's own freshness
//     check (sharedWorld.fresh, keyed on the merger's file stamp) noticed the
//     edit and re-loaded rather than silently reusing the cold closure — this
//     is the part that is actually gsx's, not go build's;
//   - the rendered class attribute changes, proving the watch cycle's regen
//     did not corrupt or poison card.x.go in the process, and that the
//     round-trip a developer actually observes under `gsx dev` behaves as the
//     spec promises end to end.
//
// Deliberately NOT t.Parallel(), even though the SharedWorldLoads assertion
// is a ">= 1" bound rather than an exact delta: codegen.SharedWorldLoads is a
// process-wide counter, so under -parallel a sibling test that mints and
// cold-loads its OWN distinct world key pads this same counter. That would
// make the ">= 1" bound pass even if THIS test's own reload were completely
// broken (a false pass, not a false fail) — and the render check does not
// backstop it, because `go run .` always recompiles mrg.go fresh from disk
// regardless of gsx's internal freshness state (see the doc above). A
// process-wide counter that gates a load-bearing assertion needs the same
// non-parallel discipline as every other counter-reading test in this
// package (TestWatchSession_EditLoadBudget,
// TestWatchSharedWorld_UnrelatedEditsLeaveWorldCold), regardless of which
// direction the bound points.
func TestWatchSharedWorld_MergerEditProducesFreshBehavior(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping go-build/run test in -short mode")
	}
	root := t.TempDir()
	modPath, mrgDir, _, viewsDir := writeSharedWorldFixture(t, root, "wwmerge")
	writeSharedWorldRunner(t, root, modPath)

	sess, startup, err := startWatchSessionForTest(watchConfig{
		paths:       []string{viewsDir},
		classMerger: &codegen.ClassMergerRef{PkgPath: modPath + "/mrg", FuncName: "Merge"},
	})
	if err != nil {
		t.Fatalf("startWatchSessionForTest: %v", err)
	}
	for _, r := range startup {
		if !r.OK {
			t.Fatalf("startup regen not OK: dir=%s err=%v diags=%v", r.Dir, r.Err, r.Diags)
		}
	}

	if out := runSharedWorldRender(t, root); !strings.Contains(out, `class="card extra"`) {
		t.Fatalf("baseline render = %q, want class=\"card extra\" (space-joined)", out)
	}

	// Switch the merger's join separator. The signature (func([]string)
	// string) is unchanged, so this is a pure behavior edit — the strongest
	// available proof, because a signature-changing edit would ALSO be caught
	// by ordinary type-checking even with a stale world serving old-but-
	// still-valid-enough types for validation to pass by coincidence.
	writeFileT(t, filepath.Join(mrgDir, "mrg.go"),
		"package mrg\n\nimport \"strings\"\n\nfunc Merge(classes []string) string { return strings.Join(classes, \",\") }\n")

	dirty := newWatchDirtySet()
	dirty.dirs[mrgDir] = true
	dirty.goDirty = true
	beforeWorld := codegen.SharedWorldLoads()
	results, rebuild, err := dirty.regenerate(sess.regenPending)
	if err != nil {
		t.Fatalf("regenerate after merger edit: %v", err)
	}
	if !rebuild {
		t.Fatal("regenerate()'s rebuild return must be true for a merger-edit goDirty cycle")
	}
	if len(results) != 1 || results[0].Dir != viewsDir {
		t.Fatalf("results = %+v, want exactly one cycleResult for views (the merger's implicit consumer); mrg itself is go-only and contributes none", results)
	}
	if !results[0].OK {
		t.Fatalf("views regen after merger edit not OK: err=%v diags=%v", results[0].Err, results[0].Diags)
	}
	if got := codegen.SharedWorldLoads() - beforeWorld; got < 1 {
		t.Fatalf("shared-world load count did not increase after editing a file the world stamped (delta %d): loadSharedWorld's freshness check did not re-load the merger's package", got)
	}

	out := runSharedWorldRender(t, root)
	if !strings.Contains(out, `class="card,extra"`) {
		t.Fatalf("render after merger edit = %q, want class=\"card,extra\" (comma-joined) — fresh merger behavior did not reach the rebuilt program", out)
	}
}

// TestWatchSharedWorld_UnrelatedEditsLeaveWorldCold pins behaviors (b) and
// (c): a .go edit OUTSIDE the world's composed closure still forces the
// ordinary project-half reload (any authored .go change nils the Module's
// cached external importer), but must NOT re-load the shared world itself —
// the world's own stamps never covered dep.go, so loadSharedWorld's freshness
// check finds it unchanged and serves the cached closure — and a pure .gsx
// body edit must not touch either loader at all, staying fully warm.
//
// Deliberately NOT t.Parallel(): both codegen.SharedWorldLoads and
// codegen.ProjectLoadCalls are process-wide counters (same discipline as
// TestWatchSession_EditLoadBudget), so a non-parallel test has the package's
// only running slot for the zero-delta assertions below, which — unlike a
// ">= 1" bound — a concurrent test's unrelated packages.Load traffic could
// otherwise falsely trip. A budget breach is retried once in a fresh module
// and the two measurements' minimum is taken: a real regression breaches
// every window, foreign packages.Load traffic from another test breaches at
// most one. See TestWatchSession_EditLoadBudget's comment for the same
// reasoning in full.
func TestWatchSharedWorld_UnrelatedEditsLeaveWorldCold(t *testing.T) {
	measure := func() (depWorldDelta, gsxWorldDelta, gsxProjDelta uint64) {
		root := t.TempDir()
		modPath, _, depDir, viewsDir := writeSharedWorldFixture(t, root, "wwcold")

		sess, startup, err := startWatchSessionForTest(watchConfig{
			paths:       []string{viewsDir},
			classMerger: &codegen.ClassMergerRef{PkgPath: modPath + "/mrg", FuncName: "Merge"},
		})
		if err != nil {
			t.Fatalf("startWatchSessionForTest: %v", err)
		}
		for _, r := range startup {
			if !r.OK {
				t.Fatalf("startup regen not OK: dir=%s err=%v diags=%v", r.Dir, r.Err, r.Diags)
			}
		}

		// (b) an unrelated .go edit: dep is an ordinary project dependency,
		// never part of sharedWorldComposition's {runtime, std, merger} path
		// set.
		writeFileT(t, filepath.Join(depDir, "dep.go"), "package dep\n\nfunc Value() string { return \"extra2\" }\n")
		dirty := newWatchDirtySet()
		dirty.dirs[depDir] = true
		dirty.goDirty = true
		beforeWorld := codegen.SharedWorldLoads()
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
		depWorldDelta = uint64(codegen.SharedWorldLoads() - beforeWorld)

		// (c) a pure .gsx body edit touches no Go source at all, so the
		// externalImporter cache is never invalidated: neither the project
		// half nor the shared world sees any packages.Load traffic.
		writeFileT(t, filepath.Join(viewsDir, "card.gsx"),
			"package views\n\nimport \""+modPath+"/dep\"\n\ncomponent Card() {\n\t<div class={ \"card\", dep.Value() }>hi again</div>\n}\n")
		dirty = newWatchDirtySet()
		dirty.dirs[viewsDir] = true
		beforeWorld = codegen.SharedWorldLoads()
		beforeProj := codegen.ProjectLoadCalls()
		results, _, err = dirty.regenerate(sess.regenPending)
		if err != nil {
			t.Fatalf("regenerate after gsx edit: %v", err)
		}
		for _, r := range results {
			if !r.OK {
				t.Fatalf("gsx-edit regen not OK: dir=%s err=%v diags=%v", r.Dir, r.Err, r.Diags)
			}
		}
		gsxWorldDelta = uint64(codegen.SharedWorldLoads() - beforeWorld)
		gsxProjDelta = codegen.ProjectLoadCalls() - beforeProj
		return depWorldDelta, gsxWorldDelta, gsxProjDelta
	}

	depWorldDelta, gsxWorldDelta, gsxProjDelta := measure()
	if depWorldDelta != 0 || gsxWorldDelta != 0 || gsxProjDelta != 0 {
		d2, g2, p2 := measure()
		depWorldDelta = min(depWorldDelta, d2)
		gsxWorldDelta = min(gsxWorldDelta, g2)
		gsxProjDelta = min(gsxProjDelta, p2)
	}
	if depWorldDelta != 0 {
		t.Errorf("unrelated dep.go edit reloaded the shared world %d times, want 0 — an edit outside the world's composed closure must not force a world rebuild", depWorldDelta)
	}
	if gsxWorldDelta != 0 {
		t.Errorf("pure .gsx edit reloaded the shared world %d times, want 0", gsxWorldDelta)
	}
	if gsxProjDelta != 0 {
		t.Errorf("pure .gsx edit issued %d packages.Load calls, want 0 — a body-only edit must stay on the cached external importer", gsxProjDelta)
	}
}
