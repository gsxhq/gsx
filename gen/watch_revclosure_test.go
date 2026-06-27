package gen

import (
	"io"
	"os"
	"path/filepath"
	"testing"
)

// TestWatchRevClosure verifies that a change to package widgets regenerates
// its reverse-closure importer (home) in the same fire cycle.
// Pre-Phase-3 only widgets would regenerate; reverse-closure now regenerates home too.
func TestWatchRevClosure(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping module-resolution test in -short mode")
	}
	t.Parallel()

	root := t.TempDir()
	repoRoot, _ := filepath.Abs("..")
	must := func(p, c string) {
		t.Helper()
		full := filepath.Join(root, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	must("go.mod", "module example.com/x\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	must("widgets/badge.gsx", "package widgets\n\ncomponent Badge(label string) {\n\t<span>{label}</span>\n}\n")
	must("home/home.gsx", "package home\n\nimport \"example.com/x/widgets\"\n\ncomponent Home() {\n\t<div><widgets.Badge label=\"hi\"/></div>\n}\n")

	widgetsDir := filepath.Join(root, "widgets")
	homeDir := filepath.Join(root, "home")

	sess, startup, err := newWatchSession(watchConfig{
		paths:  []string{root},
		stdout: io.Discard,
		stderr: io.Discard,
	})
	if err != nil {
		t.Fatalf("newWatchSession: %v", err)
	}
	for _, r := range startup {
		if r.Err != nil {
			t.Fatalf("startup regen %s: %v", r.Dir, r.Err)
		}
	}

	// Verify cold generate wrote both .x.go files.
	if _, err := os.Stat(filepath.Join(widgetsDir, "badge.x.go")); err != nil {
		t.Fatalf("widgets/badge.x.go missing after cold gen: %v", err)
	}
	if _, err := os.Stat(filepath.Join(homeDir, "home.x.go")); err != nil {
		t.Fatalf("home/home.x.go missing after cold gen: %v", err)
	}

	// Modify only widgets/badge.gsx: change <span> to <b>.
	// home/home.gsx is untouched. After the fire cycle, home must also be
	// regenerated via reverse-closure even though its source is unchanged.
	// Pre-Phase-3 only widgets would regenerate; reverse-closure now regenerates home too.
	must("widgets/badge.gsx", "package widgets\n\ncomponent Badge(label string) {\n\t<b>{label}</b>\n}\n")

	// Simulate the fire handler's non-depDirty path: Invalidate + reverse-closure union.
	// pending contains only widgetsDir — it is the only package whose source changed.
	pending := map[string]bool{widgetsDir: true}

	affected := map[string]bool{}
	for dir := range pending {
		if onlyGeneratedRemains(dir) {
			continue
		}
		m, merr := sess.moduleForDir(dir)
		if merr != nil {
			t.Fatalf("moduleForDir(%s): %v", dir, merr)
		}
		m.Invalidate(dir)
		for _, dep := range m.Dependents(dir) {
			affected[dep] = true
		}
	}

	var results []cycleResult
	for dir := range affected {
		results = append(results, sess.regenDir(dir))
	}

	// Assert both dirs appear in the regenerated set.
	dirsSeen := map[string]bool{}
	for _, r := range results {
		dirsSeen[r.Dir] = true
		if r.Err != nil {
			t.Errorf("regen %s: %v", r.Dir, r.Err)
		}
	}

	if !dirsSeen[widgetsDir] {
		t.Errorf("widgets not in regenerated set; got: %v", dirsSeen)
	}
	// Non-negotiable: editing only widgets must cause home to regenerate via
	// reverse-closure. Before this task, only widgets would be in the set.
	if !dirsSeen[homeDir] {
		t.Errorf("home not in regenerated set after editing only widgets (reverse-closure broke); got: %v", dirsSeen)
	}
}
