package codegen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gsxhq/gsx/internal/diag"
	"github.com/gsxhq/gsx/internal/sourceview"
)

// TestRefreshDiskSourcesMarksGoReload pins the disk counterpart of the
// override rule at module.go:443-445: authored .go content or membership
// changes observed by a disk refresh must reload the cold world at the next
// analysis. Before this task the warm world kept serving stale types (a
// removed exported symbol produced no diagnostic in a dependent's regen).
// It also pins the new Go-reload path staying quiet when it must: a
// byte-identical rewrite, the dev session's own paired .x.go output
// appearing on disk, and a .gsx deletion whose paired .x.go is orphaned on
// disk in the same refresh (that case still bumps the epoch once, through
// the pre-existing source-inventory-fact path — just not twice).
//
// It continues past that baseline into the RefreshVerdict truth table (Task
// 2): eight rows pinning WorldReloadPending/Reason/Path, including the
// persistence rule — a verdict can be pending from an EARLIER call whose
// reload has not landed yet, and Describe must still render that call's
// attribution. The rows extend this same Module/fixture (no extra
// packages.Load) rather than opening a second one; the "extra" module below
// exists only so row 6 has a real previously-unseen import to add — mirrors
// TestRefreshDiskSourcesReloadsPreviouslyUnseenImport's example.com/dep.
func TestRefreshDiskSourcesMarksGoReload(t *testing.T) {
	t.Parallel()
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
	write("go.mod", "module example.com/m\n\ngo 1.26.1\n\nrequire (\n\tgithub.com/gsxhq/gsx v0.0.0\n\texample.com/extra v0.0.0\n)\n\nreplace github.com/gsxhq/gsx => "+repoRoot(t)+"\n\nreplace example.com/extra => ./extra\n")
	write("extra/go.mod", "module example.com/extra\n\ngo 1.26.1\n")
	write("extra/extra.go", "package extra\n\nfunc Label() string { return \"extra\" }\n")
	write("dep/dep.go", "package dep\n\nfunc Value() string { return \"v1\" }\n")
	write("page/page.gsx", "package page\n\nimport \"example.com/m/dep\"\n\ncomponent Page() {\n\t<p>{dep.Value()}</p>\n}\n")

	m, err := Open(Options{ModuleRoot: root, ModulePath: "example.com/m"})
	if err != nil {
		t.Fatal(err)
	}

	if out, diags, err := m.Generate(filepath.Join(root, "page")); err != nil || hasDiagErrors(diags) || len(out) == 0 {
		t.Fatalf("baseline generate: out=%d diags=%v err=%v", len(out), diags, err)
	}

	// Case 1: content change removing the symbol → dependent regen must diagnose.
	write("dep/dep.go", "package dep\n\nfunc Hidden() string { return \"v2\" }\n")
	if _, _, err := m.RefreshDiskSourcesAndInvalidate(filepath.Join(root, "dep")); err != nil {
		t.Fatal(err)
	}
	_, diags, _ := m.Generate(filepath.Join(root, "page"))
	if !diagMentions(diags, "Value") {
		t.Fatalf("stale-blind: removed dep.Value produced no diagnostic; diags=%v", diags)
	}

	// Case 2: restoring the symbol heals on the same warm module.
	write("dep/dep.go", "package dep\n\nfunc Value() string { return \"v3\" }\n")
	if _, _, err := m.RefreshDiskSourcesAndInvalidate(filepath.Join(root, "dep")); err != nil {
		t.Fatal(err)
	}
	if _, diags, _ := m.Generate(filepath.Join(root, "page")); diagMentions(diags, "Value") {
		t.Fatalf("reload did not pick the restored symbol back up: %v", diags)
	}

	// Case 3: byte-identical rewrite must NOT mark a reload (no epoch churn).
	epoch := m.testSourceManifestEpoch()
	write("dep/dep.go", "package dep\n\nfunc Value() string { return \"v3\" }\n")
	if _, _, err := m.RefreshDiskSourcesAndInvalidate(filepath.Join(root, "dep")); err != nil {
		t.Fatal(err)
	}
	if got := m.testSourceManifestEpoch(); got != epoch {
		t.Fatalf("byte-identical .go rewrite bumped the manifest epoch %d -> %d", epoch, got)
	}

	// Case 4: a gsx-owned paired .x.go write (the dev session's own output)
	// must never mark a reload, even though it is a new .go file on disk.
	epoch = m.testSourceManifestEpoch()
	write("page/page.x.go", "package page\n\n// generated\n")
	if _, _, err := m.RefreshDiskSourcesAndInvalidate(filepath.Join(root, "page")); err != nil {
		t.Fatal(err)
	}
	if got := m.testSourceManifestEpoch(); got != epoch {
		t.Fatalf("paired .x.go appearance bumped the manifest epoch %d -> %d", epoch, got)
	}

	// Case 5: deleting the .gsx AND rewriting its paired .x.go in the SAME
	// refresh cycle bumps the epoch once, through the pre-existing
	// source-inventory-fact path (the .gsx's absence is itself a tracked fact
	// change) — but must NOT also trip the new Go-reload path. Once the .gsx
	// is gone, the refreshed manifest no longer classifies page.x.go as a
	// paired output at all; only the PRE-refresh (old) manifest still does,
	// because the .gsx was present when this cycle started. If the exclusion
	// consulted only the refreshed manifest, this rewritten, now-orphaned
	// .x.go would misread as a newly-appeared/changed authored .go file.
	epoch = m.testSourceManifestEpoch()
	if err := os.Remove(filepath.Join(root, "page", "page.gsx")); err != nil {
		t.Fatal(err)
	}
	write("page/page.x.go", "package page\n\n// orphaned rewrite\n")
	if _, _, err := m.RefreshDiskSourcesAndInvalidate(filepath.Join(root, "page")); err != nil {
		t.Fatal(err)
	}
	if got := m.testSourceManifestEpoch(); got != epoch+1 {
		t.Fatalf(".gsx deletion epoch = %d -> %d, want exactly +1 (membership fact only, no double count from the orphaned .x.go rewrite)", epoch, got)
	}
	if m.testGoSourceReload() {
		t.Fatal("orphaned .x.go rewrite alongside .gsx deletion marked a pending Go reload")
	}

	// A subsequent healthy refresh cycle still behaves normally: restoring
	// page.gsx heals cleanly with no leftover suppression from the delete.
	write("page/page.gsx", "package page\n\nimport \"example.com/m/dep\"\n\ncomponent Page() {\n\t<p>{dep.Value()}</p>\n}\n")
	if _, _, err := m.RefreshDiskSourcesAndInvalidate(filepath.Join(root, "page")); err != nil {
		t.Fatal(err)
	}
	if out, diags, err := m.Generate(filepath.Join(root, "page")); err != nil || hasDiagErrors(diags) || len(out) == 0 {
		t.Fatalf("post-delete restore generate: out=%d diags=%v err=%v", len(out), diags, err)
	}

	// --- RefreshVerdict truth table (Task 2) -------------------------------
	//
	// The Generate call directly above landed every outstanding reload, so
	// the module enters row 1 with WorldReloadPending already false — the
	// same starting condition a freshly Opened Module would have, without
	// paying for a second packages.Load.
	depDir := filepath.Join(root, "dep")
	pageDir := filepath.Join(root, "page")

	// Row 1: .go body edit inside an already-compiled, already-selected file
	// -> NOT pending. This row DELIBERATELY FLIPPED from
	// {true ReloadGoSource dep/dep.go} when the warm Go-syntax fast path
	// landed (refreshGoSyntaxLocked): the one changed file is re-parsed into
	// the retained FileSet and swapped into the retained package, so cmd/go
	// never has to re-select anything and no world reload is scheduled. The
	// ReloadGoSource attribution is still pinned by rows 3/4/9, which change
	// package MEMBERSHIP — a transition the fast path refuses on principle
	// (cmd/go stays the authority for file selection). Cases 1/2 above pin
	// that the swapped syntax is what later analysis actually type-checks.
	loadsBeforeRow1 := m.externalLoads()
	write("dep/dep.go", "package dep\n\nfunc Value() string { return \"v4\" }\n")
	_, verdict, err := m.RefreshDiskSourcesAndInvalidate(depDir)
	if err != nil {
		t.Fatal(err)
	}
	if verdict.WorldReloadPending {
		t.Fatalf("row 1 verdict = %+v, want not pending (warm Go-syntax fast path)", verdict)
	}
	if got := verdict.Describe(); got != "" {
		t.Fatalf("row 1 Describe() = %q, want empty", got)
	}
	if _, diags, err := m.Generate(pageDir); err != nil || hasDiagErrors(diags) {
		t.Fatalf("row 1 generate: diags=%v err=%v", diags, err)
	}
	if got := m.externalLoads(); got != loadsBeforeRow1 {
		t.Fatalf("row 1 external loads = %d, want the body edit to stay warm at %d", got, loadsBeforeRow1)
	}

	// Row 2: byte-identical .go rewrite with no prior pending reload -> not
	// pending.
	write("dep/dep.go", "package dep\n\nfunc Value() string { return \"v4\" }\n")
	_, verdict, err = m.RefreshDiskSourcesAndInvalidate(depDir)
	if err != nil {
		t.Fatal(err)
	}
	if verdict.WorldReloadPending {
		t.Fatalf("row 2 verdict = %+v, want not pending", verdict)
	}
	if got := verdict.Describe(); got != "" {
		t.Fatalf("row 2 Describe() = %q, want empty", got)
	}

	// Row 3: .go file added -> pending, ReloadGoSource, attributed to the
	// new file.
	write("dep/helper.go", "package dep\n\nfunc Extra() string { return \"extra\" }\n")
	_, verdict, err = m.RefreshDiskSourcesAndInvalidate(depDir)
	if err != nil {
		t.Fatal(err)
	}
	if !verdict.WorldReloadPending || verdict.Reason != sourceview.ReloadGoSource || verdict.Path != "dep/helper.go" {
		t.Fatalf("row 3 verdict = %+v, want {true ReloadGoSource dep/helper.go}", verdict)
	}

	// Row 4: .go file removed -> pending, ReloadGoSource, attributed to the
	// removed file. Left unlanded from row 3 on purpose: a second Go-source
	// diff inside one still-pending window must still attribute correctly.
	if err := os.Remove(filepath.Join(depDir, "helper.go")); err != nil {
		t.Fatal(err)
	}
	_, verdict, err = m.RefreshDiskSourcesAndInvalidate(depDir)
	if err != nil {
		t.Fatal(err)
	}
	if !verdict.WorldReloadPending || verdict.Reason != sourceview.ReloadGoSource || verdict.Path != "dep/helper.go" {
		t.Fatalf("row 4 verdict = %+v, want {true ReloadGoSource dep/helper.go}", verdict)
	}
	// Land it so row 5 starts clean.
	if _, diags, err := m.Generate(pageDir); err != nil || hasDiagErrors(diags) {
		t.Fatalf("row 4 landing generate: diags=%v err=%v", diags, err)
	}

	// Row 5: .gsx body-only edit -> not pending (no package/import/membership
	// fact changed).
	write("page/page.gsx", "package page\n\nimport \"example.com/m/dep\"\n\ncomponent Page() {\n\t<p>changed: {dep.Value()}</p>\n}\n")
	_, verdict, err = m.RefreshDiskSourcesAndInvalidate(pageDir)
	if err != nil {
		t.Fatal(err)
	}
	if verdict.WorldReloadPending {
		t.Fatalf("row 5 verdict = %+v, want not pending", verdict)
	}

	// Row 6: .gsx gains an import outside the loaded world -> pending,
	// ReloadImports, attributed to page.gsx. example.com/extra was declared
	// in go.mod from Open but never imported, so it is genuinely absent from
	// the cold external importer.
	write("page/page.gsx", "package page\n\nimport (\n\t\"example.com/m/dep\"\n\t\"example.com/extra\"\n)\n\ncomponent Page() {\n\t<p>changed: {dep.Value()}</p>\n\t<p>{extra.Label()}</p>\n}\n")
	_, verdict, err = m.RefreshDiskSourcesAndInvalidate(pageDir)
	if err != nil {
		t.Fatal(err)
	}
	if !verdict.WorldReloadPending || verdict.Reason != sourceview.ReloadImports || verdict.Path != "page/page.gsx" {
		t.Fatalf("row 6 verdict = %+v, want {true ReloadImports page/page.gsx}", verdict)
	}
	if got, want := verdict.Describe(), "new import outside the loaded world in page/page.gsx"; got != want {
		t.Fatalf("row 6 Describe() = %q, want %q", got, want)
	}

	// Row 7: persistence — refresh a DIFFERENT dir byte-identically, with NO
	// Generate in between, and the row-6 reload must still read pending. The
	// dep dir's own facts are untouched by this refresh, so the only source
	// of WorldReloadPending here is row 6's still-unlanded page.gsx entry.
	write("dep/dep.go", "package dep\n\nfunc Value() string { return \"v4\" }\n")
	_, verdict, err = m.RefreshDiskSourcesAndInvalidate(depDir)
	if err != nil {
		t.Fatal(err)
	}
	if !verdict.WorldReloadPending {
		t.Fatalf("row 7 verdict = %+v, want still pending (persisted from row 6)", verdict)
	}

	// Row 8: after Generate lands the row-6/7 reload, a fresh byte-identical
	// refresh reports not pending again.
	if _, diags, err := m.Generate(pageDir); err != nil || hasDiagErrors(diags) {
		t.Fatalf("row 7 landing generate: diags=%v err=%v", diags, err)
	}
	write("page/page.gsx", "package page\n\nimport (\n\t\"example.com/m/dep\"\n\t\"example.com/extra\"\n)\n\ncomponent Page() {\n\t<p>changed: {dep.Value()}</p>\n\t<p>{extra.Label()}</p>\n}\n")
	_, verdict, err = m.RefreshDiskSourcesAndInvalidate(pageDir)
	if err != nil {
		t.Fatal(err)
	}
	if verdict.WorldReloadPending {
		t.Fatalf("row 8 verdict = %+v, want not pending", verdict)
	}

	// Row 9 (beyond the brief's 8; closes a coverage gap): persistence for
	// the Go-source branch specifically, not just sourceReloadReasons (row
	// 7). goSourceReload is a single flag with no memory of which file
	// tripped it, so once it is pending, a LATER call that finds nothing new
	// of its own must still attribute to the file that originally set it —
	// exactly what m.reloadAttribution exists for. The seed is a membership
	// change rather than a body edit (as it was before the warm Go-syntax
	// fast path landed) because only a transition the fast path refuses can
	// leave goSourceReload pending across calls at all.
	write("dep/persist.go", "package dep\n\nfunc Persisted() string { return \"persisted\" }\n")
	_, verdict, err = m.RefreshDiskSourcesAndInvalidate(depDir)
	if err != nil {
		t.Fatal(err)
	}
	if !verdict.WorldReloadPending || verdict.Reason != sourceview.ReloadGoSource || verdict.Path != "dep/persist.go" {
		t.Fatalf("row 9a verdict = %+v, want {true ReloadGoSource dep/persist.go}", verdict)
	}
	// Refresh page dir byte-identically, with NO Generate in between: this
	// call finds nothing new (page.gsx unchanged, no .gsx reload reason), so
	// the verdict can only come from the persisted attribution set by the
	// dep.go change above, not from anything this call touched.
	write("page/page.gsx", "package page\n\nimport (\n\t\"example.com/m/dep\"\n\t\"example.com/extra\"\n)\n\ncomponent Page() {\n\t<p>changed: {dep.Value()}</p>\n\t<p>{extra.Label()}</p>\n}\n")
	_, verdict, err = m.RefreshDiskSourcesAndInvalidate(pageDir)
	if err != nil {
		t.Fatal(err)
	}
	if !verdict.WorldReloadPending || verdict.Reason != sourceview.ReloadGoSource || verdict.Path != "dep/persist.go" {
		t.Fatalf("row 9b verdict = %+v, want {true ReloadGoSource dep/persist.go} (persisted from the untouched dep dir)", verdict)
	}
	if got, want := verdict.Describe(), "changed Go source dep/persist.go"; got != want {
		t.Fatalf("row 9b Describe() = %q, want %q", got, want)
	}

	// Row 10: case 5's exclusion discipline, now inside the warm Go-syntax
	// fast path. The fast path compares the SAME authored-Go file set the
	// detector above compared, so its paired-output exclusion must also be the
	// union of both manifests. Here one refresh deletes page.gsx, rewrites its
	// now-orphaned page.x.go, AND body-edits a real authored helper in the same
	// dir: the union hides page.x.go on both sides, leaving helper.go as the
	// only authored change — swappable, so no Go-source reload is scheduled and
	// the epoch moves once, for the .gsx membership fact alone (whose own
	// ReloadMembership reason is what schedules the world reload). Consulting
	// either manifest alone would see page.x.go appear out of nowhere, refuse
	// the swap, and double-count the epoch — case 5's original bug, one layer
	// down.
	if _, diags, err := m.Generate(pageDir); err != nil || hasDiagErrors(diags) {
		t.Fatalf("row 9 landing generate: diags=%v err=%v", diags, err)
	}
	write("page/helper.go", "package page\n\nfunc helperValue() string { return \"h1\" }\n")
	if _, _, err := m.RefreshDiskSourcesAndInvalidate(pageDir); err != nil {
		t.Fatal(err)
	}
	if _, diags, err := m.Generate(pageDir); err != nil || hasDiagErrors(diags) {
		t.Fatalf("row 10 helper landing generate: diags=%v err=%v", diags, err)
	}
	if m.testGoSourceReload() {
		t.Fatal("row 10 setup left a Go reload pending")
	}
	epoch = m.testSourceManifestEpoch()
	if err := os.Remove(filepath.Join(pageDir, "page.gsx")); err != nil {
		t.Fatal(err)
	}
	write("page/page.x.go", "package page\n\n// orphaned rewrite again\n")
	write("page/helper.go", "package page\n\nfunc helperValue() string { return \"h2\" }\n")
	_, verdict, err = m.RefreshDiskSourcesAndInvalidate(pageDir)
	if err != nil {
		t.Fatal(err)
	}
	if got := m.testSourceManifestEpoch(); got != epoch+1 {
		t.Fatalf("row 10 epoch = %d -> %d, want exactly +1 (membership fact only; the warm swap absorbs the helper edit)", epoch, got)
	}
	if m.testGoSourceReload() {
		t.Fatal("row 10 marked a Go reload: the orphaned .x.go must stay excluded on BOTH sides of the fast path's comparison")
	}
	if !verdict.WorldReloadPending || verdict.Reason != sourceview.ReloadMembership {
		t.Fatalf("row 10 verdict = %+v, want the .gsx deletion's own {true ReloadMembership ...}", verdict)
	}
}

// diagMentions reports whether any diagnostic in diags mentions substr.
func diagMentions(diags []diag.Diagnostic, substr string) bool {
	for _, d := range diags {
		if strings.Contains(d.Message, substr) {
			return true
		}
	}
	return false
}
