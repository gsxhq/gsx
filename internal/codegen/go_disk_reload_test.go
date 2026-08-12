package codegen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gsxhq/gsx/internal/diag"
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
	write("go.mod", "module example.com/m\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot(t)+"\n")
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
	if _, err := m.RefreshDiskSourcesAndInvalidate(filepath.Join(root, "dep")); err != nil {
		t.Fatal(err)
	}
	_, diags, _ := m.Generate(filepath.Join(root, "page"))
	if !diagMentions(diags, "Value") {
		t.Fatalf("stale-blind: removed dep.Value produced no diagnostic; diags=%v", diags)
	}

	// Case 2: restoring the symbol heals on the same warm module.
	write("dep/dep.go", "package dep\n\nfunc Value() string { return \"v3\" }\n")
	if _, err := m.RefreshDiskSourcesAndInvalidate(filepath.Join(root, "dep")); err != nil {
		t.Fatal(err)
	}
	if _, diags, _ := m.Generate(filepath.Join(root, "page")); diagMentions(diags, "Value") {
		t.Fatalf("reload did not pick the restored symbol back up: %v", diags)
	}

	// Case 3: byte-identical rewrite must NOT mark a reload (no epoch churn).
	epoch := m.testSourceManifestEpoch()
	write("dep/dep.go", "package dep\n\nfunc Value() string { return \"v3\" }\n")
	if _, err := m.RefreshDiskSourcesAndInvalidate(filepath.Join(root, "dep")); err != nil {
		t.Fatal(err)
	}
	if got := m.testSourceManifestEpoch(); got != epoch {
		t.Fatalf("byte-identical .go rewrite bumped the manifest epoch %d -> %d", epoch, got)
	}

	// Case 4: a gsx-owned paired .x.go write (the dev session's own output)
	// must never mark a reload, even though it is a new .go file on disk.
	epoch = m.testSourceManifestEpoch()
	write("page/page.x.go", "package page\n\n// generated\n")
	if _, err := m.RefreshDiskSourcesAndInvalidate(filepath.Join(root, "page")); err != nil {
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
	if _, err := m.RefreshDiskSourcesAndInvalidate(filepath.Join(root, "page")); err != nil {
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
	if _, err := m.RefreshDiskSourcesAndInvalidate(filepath.Join(root, "page")); err != nil {
		t.Fatal(err)
	}
	if out, diags, err := m.Generate(filepath.Join(root, "page")); err != nil || hasDiagErrors(diags) || len(out) == 0 {
		t.Fatalf("post-delete restore generate: out=%d diags=%v err=%v", len(out), diags, err)
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
