package gen

// TestWatchEquiv and TestWatchEquiv_MinifyNone prove OUTPUT BYTE-EQUIVALENCE:
// the .x.go bytes written by a cold-start watchSession must be byte-identical to
// those written by a one-shot Generate run on the same sources/config. The tests
// would FAIL if the warm Module.Generate path emits any different bytes.

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gsxhq/gsx/internal/attrclass"
)

// equivModuleDir returns the replacement path for the gsx module (one level
// above gen/).  Mirrors gsxModuleDir but is package-local to this file.
func equivModuleDir(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Dir(wd)
}

// equivWriteFixture writes a two-package module into root:
//
//	comp/card.gsx  – a leaf component (no imports)
//	views/page.gsx – imports comp.Card
//
// Both module roots that receive this fixture share the same module path
// "example.com/equiv", so generated code referencing import paths is
// byte-identical across the two independently-rooted copies.
func equivWriteFixture(t *testing.T, root string) {
	t.Helper()
	repoRoot := equivModuleDir(t)
	modContent := "module example.com/equiv\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => " + repoRoot + "\n"
	writeFile(t, root, "go.mod", modContent)
	writeFile(t, filepath.Join(root, "comp"), "card.gsx",
		"package comp\n\ncomponent Card(title string) {\n\t<style>.card {  color : red ;  }</style>\n\t<div class=\"card\">{title}</div>\n}\n")
	writeFile(t, filepath.Join(root, "views"), "page.gsx",
		"package views\n\nimport \"example.com/equiv/comp\"\n\ncomponent Page() {\n\t<comp.Card title=\"hello\"/>\n}\n")
}

// equivReadXGo reads a generated file <root>/<rel> and fatals when missing.
func equivReadXGo(t *testing.T, root, rel string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, rel))
	if err != nil {
		t.Fatalf("missing %s: %v", filepath.Join(root, rel), err)
	}
	return b
}

// TestWatchEquiv proves byte-equivalence between a cold-start watchSession and a
// one-shot generateCached run, for cssMinify=true/jsMinify=true (the setting
// used by the public Generate function).
//
// After a source edit the test also drives a warm regen (Invalidate + Dependents
// + regenDir, mirroring watch.go's fire handler) and verifies the output still
// matches a fresh one-shot generate of the modified sources.
//
// The test is non-tautological: it asserts len > 0 on the one-shot bytes so a
// silently empty generate would surface immediately.
func TestWatchEquiv(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping module-resolution test in -short mode")
	}

	// dir1 = one-shot reference; dir2 = watch cold-start. Same module path,
	// same .gsx sources, independent filesystem roots.
	dir1 := t.TempDir()
	dir2 := t.TempDir()
	equivWriteFixture(t, dir1)
	equivWriteFixture(t, dir2)

	// One-shot generate on dir1. useCache=false avoids any stale cache entry
	// that could mask a real byte difference. cssMinify=true/jsMinify=true
	// matches what the public Generate function uses.
	osRes, osErr := generateCached([]string{dir1}, nil, nil, attrclass.Builtin(), nil, false, nil, nil, true, true, nil)
	if osErr != nil {
		t.Fatalf("one-shot generateCached: %v; diags=%v", osErr, osRes.Diags)
	}
	if len(osRes.Written) == 0 {
		t.Fatal("one-shot produced no written files — fixture may be broken")
	}

	// Cold-start watchSession on dir2 with matching minify settings.
	// Startup regenDir writes all .x.go files and populates the import graph.
	sess, startup, sessErr := newWatchSession(watchConfig{
		paths:     []string{dir2},
		cls:       attrclass.Builtin(),
		cssMinify: true,
		jsMinify:  true,
	})
	if sessErr != nil {
		t.Fatalf("newWatchSession: %v", sessErr)
	}
	for _, r := range startup {
		if !r.OK {
			t.Fatalf("startup regen %s: err=%v diags=%v", r.Dir, r.Err, r.Diags)
		}
	}

	// --- Cold-start equivalence ---
	relPaths := []string{
		filepath.Join("comp", "card.x.go"),
		filepath.Join("views", "page.x.go"),
	}
	for _, rel := range relPaths {
		osBuf := equivReadXGo(t, dir1, rel)
		wBuf := equivReadXGo(t, dir2, rel)
		if len(osBuf) == 0 {
			t.Fatalf("one-shot produced empty %s — non-tautology guard failed", rel)
		}
		if !bytes.Equal(osBuf, wBuf) {
			t.Errorf("cold-start byte mismatch for %s\none-shot (%d B):\n%s\nwatch (%d B):\n%s",
				rel, len(osBuf), osBuf, len(wBuf), wBuf)
		}
	}

	// Non-vacuity: assert CSS was actually minified. If watch ignored cssMinify
	// and emitted raw CSS, this would fail even if bytes matched the wrong thing.
	cardBuf := equivReadXGo(t, dir1, filepath.Join("comp", "card.x.go"))
	if !strings.Contains(string(cardBuf), ".card{color : red}") {
		t.Errorf("TestWatchEquiv: expected minified CSS in card.x.go; got:\n%s", cardBuf)
	}

	// --- Warm regen after source edit ---
	// Modify comp/card.gsx in the watch tree.
	updatedCard := "package comp\n\ncomponent Card(title string) {\n\t<span class=\"card\">{title}</span>\n}\n"
	writeFileT(t, filepath.Join(dir2, "comp", "card.gsx"), updatedCard)

	// Replicate the fire handler: Invalidate the changed dir, collect its
	// reverse-closure via Dependents (comp + views), regen each.
	compDir2 := filepath.Join(dir2, "comp")
	m, mErr := sess.moduleForDir(compDir2)
	if mErr != nil {
		t.Fatalf("moduleForDir(comp): %v", mErr)
	}
	m.Invalidate(compDir2)
	affected := map[string]bool{}
	for _, dep := range m.Dependents(compDir2) {
		affected[dep] = true
	}
	for dir := range affected {
		if r := sess.regenDir(dir); !r.OK {
			t.Fatalf("post-edit regenDir(%s): err=%v diags=%v", dir, r.Err, r.Diags)
		}
	}

	// Fresh one-shot on dir3 with the updated comp source.
	dir3 := t.TempDir()
	equivWriteFixture(t, dir3)
	writeFileT(t, filepath.Join(dir3, "comp", "card.gsx"), updatedCard)
	os3Res, os3Err := generateCached([]string{dir3}, nil, nil, attrclass.Builtin(), nil, false, nil, nil, true, true, nil)
	if os3Err != nil {
		t.Fatalf("post-edit one-shot: %v; diags=%v", os3Err, os3Res.Diags)
	}
	if len(os3Res.Written) == 0 {
		t.Fatal("post-edit one-shot produced no written files")
	}

	for _, rel := range relPaths {
		osBuf := equivReadXGo(t, dir3, rel)
		wBuf := equivReadXGo(t, dir2, rel)
		if len(osBuf) == 0 {
			t.Fatalf("post-edit one-shot produced empty %s — non-tautology guard failed", rel)
		}
		if !bytes.Equal(osBuf, wBuf) {
			t.Errorf("post-edit byte mismatch for %s\none-shot (%d B):\n%s\nwatch (%d B):\n%s",
				rel, len(osBuf), osBuf, len(wBuf), wBuf)
		}
	}
}

// TestWatchEquiv_MinifyNone proves byte-equivalence when cssMinify=false and
// jsMinify=false (MinifyNone level). Both one-shot and watch must agree on the
// verbatim (non-minified) output.  The minify-threading unit tests (Task 1
// TestMinifyThreading) cover the full-level path in isolation; this test proves
// that the zero-value (MinifyNone) setting is also threaded consistently through
// the watch path.
func TestWatchEquiv_MinifyNone(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping module-resolution test in -short mode")
	}

	dir1 := t.TempDir()
	dir2 := t.TempDir()
	equivWriteFixture(t, dir1)
	equivWriteFixture(t, dir2)

	// One-shot with minify disabled (cssMinify=false, jsMinify=false).
	osRes, osErr := generateCached([]string{dir1}, nil, nil, attrclass.Builtin(), nil, false, nil, nil, false, false, nil)
	if osErr != nil {
		t.Fatalf("one-shot (minify-none): %v; diags=%v", osErr, osRes.Diags)
	}
	if len(osRes.Written) == 0 {
		t.Fatal("one-shot (minify-none) produced no written files")
	}

	// Watch with cssMinify=false (zero value — MinifyNone).
	_, startup, sessErr := newWatchSession(watchConfig{
		paths:     []string{dir2},
		cls:       attrclass.Builtin(),
		cssMinify: false,
		jsMinify:  false,
	})
	if sessErr != nil {
		t.Fatalf("newWatchSession (minify-none): %v", sessErr)
	}
	for _, r := range startup {
		if !r.OK {
			t.Fatalf("startup regen (minify-none) %s: err=%v diags=%v", r.Dir, r.Err, r.Diags)
		}
	}

	relPaths := []string{
		filepath.Join("comp", "card.x.go"),
		filepath.Join("views", "page.x.go"),
	}
	for _, rel := range relPaths {
		osBuf := equivReadXGo(t, dir1, rel)
		wBuf := equivReadXGo(t, dir2, rel)
		if len(osBuf) == 0 {
			t.Fatalf("minify-none one-shot produced empty %s", rel)
		}
		if !bytes.Equal(osBuf, wBuf) {
			t.Errorf("minify-none byte mismatch for %s\none-shot (%d B):\n%s\nwatch (%d B):\n%s",
				rel, len(osBuf), osBuf, len(wBuf), wBuf)
		}
	}

	// Non-vacuity: assert CSS was NOT minified. If watch always minified (ignoring
	// cssMinify=false), watch's bytes would contain ".card{color : red}" while
	// one-shot emits the verbatim form — the byte-equality check above would already
	// catch that mismatch, but this explicit Contains makes the minify STATE visible.
	cardBuf := equivReadXGo(t, dir1, filepath.Join("comp", "card.x.go"))
	if !strings.Contains(string(cardBuf), ".card {  color : red ;  }") {
		t.Errorf("TestWatchEquiv_MinifyNone: expected un-minified CSS in card.x.go; got:\n%s", cardBuf)
	}
}
