package gen

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/gsxhq/gsx/internal/codegen"
)

// TestModuleRootReportsErrNoEnclosingModule pins moduleRoot's sentinel error
// for a directory with no go.mod in any ancestor. regenPending's cross-module
// routing (T3) distinguishes this case — "not part of any watched module,
// safe to skip" — from a genuine go.mod read/parse failure, which must not be
// silently skipped.
func TestModuleRootReportsErrNoEnclosingModule(t *testing.T) {
	dir := t.TempDir()
	_, _, err := moduleRoot(dir)
	if !errors.Is(err, errNoEnclosingModule) {
		t.Fatalf("moduleRoot(%s) error = %v, want errors.Is(err, errNoEnclosingModule)", dir, err)
	}
}

// TestWorkspaceUsesModuleHonorsAuthoritativeGOWORK pins that the cross-module
// workspace link is decided by the GOWORK value frozen into the consumer's Go
// command universe, not by a filesystem walk: GOWORK=off severs an ancestor
// go.work, and an explicit GOWORK names a workspace a walk would never find.
//
// Ported from PR #185 (gen/watch_goedit_crossmodule_test.go) unmodified. On
// darwin, t.TempDir() sits under a symlinked /var -> /private/var, and `go
// env GOWORK`'s auto-discovered answer comes back symlink-resolved while
// alphaRoot/betaRoot stay lexical — workspaceUsesModule must normalize both
// sides before comparing or the third assertion (discovered ancestor
// workspace) fails deterministically on darwin.
func TestWorkspaceUsesModuleHonorsAuthoritativeGOWORK(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping go-env resolution test in -short mode")
	}
	parent := t.TempDir()
	alphaRoot := filepath.Join(parent, "alpha")
	betaRoot := filepath.Join(parent, "beta")
	writeFileT(t, filepath.Join(alphaRoot, "go.mod"), "module alphamod\n\ngo 1.26.1\n")
	writeFileT(t, filepath.Join(betaRoot, "go.mod"), "module betamod\n\ngo 1.26.1\n")
	workPath := filepath.Join(parent, "go.work")
	writeFileT(t, workPath, "go 1.26.1\n\nuse (\n\t./alpha\n\t./beta\n)\n")
	elsewhere := filepath.Join(t.TempDir(), "linked.work")
	writeFileT(t, elsewhere, "go 1.26.1\n\nuse (\n\t"+filepath.ToSlash(alphaRoot)+"\n\t"+filepath.ToSlash(betaRoot)+"\n)\n")
	sess := &watchSession{modules: map[string]*codegen.Module{}}

	t.Setenv("GOWORK", "off")
	if sess.workspaceUsesModule(betaRoot, alphaRoot) {
		t.Fatal("GOWORK=off must sever the ancestor workspace link")
	}
	t.Setenv("GOWORK", elsewhere)
	if !sess.workspaceUsesModule(betaRoot, alphaRoot) {
		t.Fatal("an explicit GOWORK workspace link was missed")
	}
	t.Setenv("GOWORK", "")
	if !sess.workspaceUsesModule(betaRoot, alphaRoot) {
		t.Fatal("the discovered ancestor workspace link was missed")
	}
}

// TestModuleConsumesModuleReplaceLink is a direct unit test of
// moduleConsumesModule's replace-directive matching, independent of
// regenPending's routing (not yet wired — see
// TestWatchGoEditPropagatesAcrossReplaceLink below).
func TestModuleConsumesModuleReplaceLink(t *testing.T) {
	parent := t.TempDir()
	editedRoot := filepath.Join(parent, "alpha")
	writeFileT(t, filepath.Join(editedRoot, "go.mod"), "module alphamod\n\ngo 1.26.1\n")

	sess := &watchSession{modules: map[string]*codegen.Module{}}

	t.Run("relative replace target matches", func(t *testing.T) {
		consumerRoot := filepath.Join(parent, "gamma-relative")
		writeFileT(t, filepath.Join(consumerRoot, "go.mod"),
			"module gammamod\n\ngo 1.26.1\n\nrequire alphamod v0.0.0\n\nreplace alphamod => ../alpha\n")
		if !sess.moduleConsumesModule(consumerRoot, editedRoot) {
			t.Fatal("relative replace target was not recognized as consuming")
		}
	})

	t.Run("absolute replace target matches", func(t *testing.T) {
		consumerRoot := filepath.Join(parent, "gamma-absolute")
		writeFileT(t, filepath.Join(consumerRoot, "go.mod"),
			"module gammamod\n\ngo 1.26.1\n\nrequire alphamod v0.0.0\n\nreplace alphamod => "+filepath.ToSlash(editedRoot)+"\n")
		if !sess.moduleConsumesModule(consumerRoot, editedRoot) {
			t.Fatal("absolute replace target was not recognized as consuming")
		}
	})

	t.Run("versioned replace is not a filesystem link", func(t *testing.T) {
		consumerRoot := filepath.Join(parent, "gamma-versioned")
		writeFileT(t, filepath.Join(consumerRoot, "go.mod"),
			"module gammamod\n\ngo 1.26.1\n\nrequire alphamod v0.0.0\n\nreplace alphamod => alphamod v1.2.3\n")
		if sess.moduleConsumesModule(consumerRoot, editedRoot) {
			t.Fatal("a versioned replace (resolves through the proxy/cache) must not be treated as a local consumer link")
		}
	})

	t.Run("no replace and no workspace does not consume", func(t *testing.T) {
		consumerRoot := filepath.Join(parent, "delta")
		writeFileT(t, filepath.Join(consumerRoot, "go.mod"), "module deltamod\n\ngo 1.26.1\n")
		if sess.moduleConsumesModule(consumerRoot, editedRoot) {
			t.Fatal("an unrelated module with no replace/workspace link was reported as consuming")
		}
	})

	t.Run("unreadable go.mod fails toward consuming", func(t *testing.T) {
		consumerRoot := filepath.Join(parent, "epsilon-missing")
		if !sess.moduleConsumesModule(consumerRoot, editedRoot) {
			t.Fatal("an unreadable consumer go.mod must fail toward \"consuming\" (reopen), not toward silence")
		}
	})

	t.Run("unparseable go.mod fails toward consuming", func(t *testing.T) {
		consumerRoot := filepath.Join(parent, "zeta-corrupt")
		writeFileT(t, filepath.Join(consumerRoot, "go.mod"), "not a valid go.mod {{{\n")
		if !sess.moduleConsumesModule(consumerRoot, editedRoot) {
			t.Fatal("an unparseable consumer go.mod must fail toward \"consuming\" (reopen), not toward silence")
		}
	})
}

// TestModuleConsumesModuleNormalizesSymlinkedReplaceTarget covers
// moduleConsumesModule's own symlink-vs-lexical skew: a replace directive
// recorded through a symlink-resolved view of a directory (as a discovery
// path or an authoritative Go-command answer might produce) must still match
// editedRoot's lexical form. On darwin this reproduces naturally because
// t.TempDir() sits under symlinked /var -> /private/var.
func TestModuleConsumesModuleNormalizesSymlinkedReplaceTarget(t *testing.T) {
	parent := t.TempDir()
	editedRoot := filepath.Join(parent, "alpha")
	if err := os.MkdirAll(editedRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	resolvedEdited, err := filepath.EvalSymlinks(editedRoot)
	if err != nil {
		t.Fatal(err)
	}

	consumerRoot := filepath.Join(parent, "gamma")
	// Simulates a replace directive recorded/resolved through a different
	// (symlink-resolved) view of the same directory than editedRoot's own
	// lexical form — the same skew workspaceUsesModule sees between the
	// session's lexical module root and go env's resolved GOWORK.
	writeFileT(t, filepath.Join(consumerRoot, "go.mod"),
		"module gammamod\n\ngo 1.26.1\n\nrequire alphamod v0.0.0\n\nreplace alphamod => "+filepath.ToSlash(resolvedEdited)+"\n")

	sess := &watchSession{modules: map[string]*codegen.Module{}}
	if !sess.moduleConsumesModule(consumerRoot, editedRoot) {
		t.Fatal("replace target normalization missed a symlink-resolved vs lexical path match")
	}
}

// TestReopenConsumerModulesRegeneratesLinkedConsumer directly drives
// reopenConsumerModules (bypassing regenPending's not-yet-wired routing, see
// TestWatchGoEditPropagatesAcrossReplaceLink): a session module consuming
// another session module through a go.mod directory replace must be reopened
// (discarding its retained analysis) with its dirs returned as affected; an
// unlinked sibling module must be left untouched.
func TestReopenConsumerModulesRegeneratesLinkedConsumer(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping module-resolution test in -short mode")
	}
	parent := t.TempDir()

	// alpha: Go-only module exporting the alias gamma consumes. No .gsx at all.
	alphaRoot := filepath.Join(parent, "alpha")
	writeFileT(t, filepath.Join(alphaRoot, "go.mod"), "module alphamod\n\ngo 1.26.1\n")
	libDir := filepath.Join(alphaRoot, "lib")
	libPath := filepath.Join(libDir, "lib.go")
	writeFileT(t, libPath, "package lib\n\ntype Label = string\n")

	// beta: independent module with no link to alpha — must never be reopened
	// by an alpha edit.
	betaRoot := filepath.Join(parent, "beta")
	writeModule(t, betaRoot, "betamod")
	writeFileT(t, filepath.Join(betaRoot, "hi.gsx"), "package beta\n\ncomponent Hi() { <p>beta</p> }\n")

	// gamma: replace-links alphamod and consumes lib.Label in a component.
	gammaRoot := filepath.Join(parent, "gamma")
	writeFileT(t, filepath.Join(gammaRoot, "go.mod"),
		"module gammamod\n\ngo 1.26.1\n\nrequire (\n\talphamod v0.0.0\n\tgithub.com/gsxhq/gsx v0.0.0\n)\n\nreplace alphamod => ../alpha\nreplace github.com/gsxhq/gsx => "+repoRoot(t)+"\n")
	viewsDir := filepath.Join(gammaRoot, "views")
	writeFileT(t, filepath.Join(viewsDir, "page.gsx"),
		"package views\n\nimport \"alphamod/lib\"\n\ncomponent Page(label lib.Label) { <p>{label}</p> }\n")

	sess, startup, err := startWatchSessionForTest(watchConfig{paths: []string{parent}})
	if err != nil {
		t.Fatalf("startWatchSessionForTest: %v", err)
	}
	for _, result := range startup {
		if !result.OK {
			t.Fatalf("startup regen %s not OK: err=%v diags=%v", result.Dir, result.Err, result.Diags)
		}
	}

	origGamma := sess.modules[gammaRoot]
	origBeta := sess.modules[betaRoot]
	if origGamma == nil || origBeta == nil {
		t.Fatalf("expected warm modules for gamma and beta after startup, got gamma=%v beta=%v", origGamma, origBeta)
	}

	// Break the alias. Only gamma's replace-linked external importer can
	// observe this; alpha's own warm module has no dependents to reach it.
	writeFileT(t, libPath, "package lib\n\ntype Renamed = string\n")

	affected, results := sess.reopenConsumerModules(map[string][]string{alphaRoot: {libDir}})
	if len(results) != 0 {
		t.Fatalf("unexpected error results: %+v", results)
	}
	if sess.modules[gammaRoot] == origGamma {
		t.Fatal("linked consumer gamma was not reopened")
	}
	if sess.modules[betaRoot] != origBeta {
		t.Fatal("unlinked module beta was reopened by an alpha edit")
	}
	found := false
	for _, d := range affected {
		if d == viewsDir {
			found = true
		}
	}
	if !found {
		t.Fatalf("gamma/views was not returned as affected: %v", affected)
	}

	// The reopened module must now observe the removed alias.
	result := sess.generateDir(sess.modules[gammaRoot], viewsDir)
	if result.OK || len(result.Diags) == 0 {
		t.Fatalf("gamma/views regen against the reopened module should observe the removed alias, got OK=%v diags=%v", result.OK, result.Diags)
	}
}

// TestReopenConsumerModulesSurfacesErrorForUnparseableConsumer is the
// defect-2 regression test: moduleConsumesModule documents "unreadable or
// unparseable module metadata is treated as consuming" (fail toward reopen).
// reopenConsumerModules must make that promise real — attempt the reopen and
// surface a per-module error result when it can't — rather than silently
// producing neither an affected dir nor an error for a linked consumer whose
// own go.mod just became unparseable.
func TestReopenConsumerModulesSurfacesErrorForUnparseableConsumer(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping module-resolution test in -short mode")
	}
	parent := t.TempDir()

	alphaRoot := filepath.Join(parent, "alpha")
	writeFileT(t, filepath.Join(alphaRoot, "go.mod"), "module alphamod\n\ngo 1.26.1\n")
	libDir := filepath.Join(alphaRoot, "lib")
	writeFileT(t, filepath.Join(libDir, "lib.go"), "package lib\n\ntype Label = string\n")

	gammaRoot := filepath.Join(parent, "gamma")
	gammaGoMod := filepath.Join(gammaRoot, "go.mod")
	writeFileT(t, gammaGoMod, "module gammamod\n\ngo 1.26.1\n")
	viewsDir := filepath.Join(gammaRoot, "views")
	writeFileT(t, filepath.Join(viewsDir, "page.gsx"), "package views\n\ncomponent Page() { <p>x</p> }\n")

	sess, _, err := startWatchSessionForTest(watchConfig{paths: []string{parent}})
	if err != nil {
		t.Fatalf("startWatchSessionForTest: %v", err)
	}

	// Corrupt gamma's go.mod after the session already opened it warm — the
	// same failure moduleConsumesModule's doc treats as "assume consuming,
	// reopen rather than serve stale types".
	writeFileT(t, gammaGoMod, "not a valid go.mod {{{\n")

	affected, results := sess.reopenConsumerModules(map[string][]string{alphaRoot: {libDir}})
	if len(affected) != 0 {
		t.Fatalf("unparseable consumer must not report affected dirs it could not confirm: %v", affected)
	}
	if len(results) != 1 || results[0].Err == nil {
		t.Fatalf("unparseable consumer go.mod must surface exactly one per-module error result, got %+v", results)
	}
}

// TestWatchGoEditPropagatesAcrossReplaceLink is PR #185's end-to-end proof
// that a session module consuming another through a go.mod directory replace
// regenerates when the consumed module's Go source changes. It drives
// regenPending(nil, goPending, false) — PR #185's 3-argument form that routes
// authored-Go dirs into reopenConsumerModules via a goByRoot classification.
// Main's regenPending (gen/watchsession.go) does not yet carry that
// classification; wiring it in is T3's job (.pr185-overlap-report.md Q3/Q4;
// "do NOT re-shape main's regenPending" is out of scope for this change).
// Until T3 lands that routing, this stays a documented gap covered directly
// by TestModuleConsumesModule*, TestWorkspaceUsesModule*, and
// TestReopenConsumerModules* above, which exercise the same mechanics without
// going through regenPending.
func TestWatchGoEditPropagatesAcrossReplaceLink(t *testing.T) {
	t.Skip("TODO(T3): wire regenPending's authored-Go routing to reopenConsumerModules, then restore this end-to-end scenario (see TestReopenConsumerModulesRegeneratesLinkedConsumer for the equivalent direct-call coverage)")
}
