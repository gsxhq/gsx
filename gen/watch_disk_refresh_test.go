package gen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWatchSessionRefreshesSavedGsxImportSurface(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping module-resolution test in -short mode")
	}
	root := t.TempDir()
	repoRoot, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	writeFileT(t, filepath.Join(root, "go.mod"), "module example.com/app\n\ngo 1.26.1\n\nrequire (\n\tgithub.com/gsxhq/gsx v0.0.0\n\texample.com/dep v0.0.0\n)\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\nreplace example.com/dep => ./dep\n")
	writeFileT(t, filepath.Join(root, "dep", "go.mod"), "module example.com/dep\n\ngo 1.26.1\n")
	writeFileT(t, filepath.Join(root, "dep", "dep.go"), "package dep\n\ntype Value string\n")
	pageDir := filepath.Join(root, "page")
	pagePath := filepath.Join(pageDir, "page.gsx")
	writeFileT(t, pagePath, "package page\n\ncomponent Page(value string) { <p>{value}</p> }\n")

	session, startup, err := startWatchSessionForTest(watchConfig{paths: []string{pageDir}})
	if err != nil {
		t.Fatalf("startWatchSessionForTest: %v", err)
	}
	for _, result := range startup {
		if !result.OK {
			t.Fatalf("startup result for %s: error=%v diagnostics=%v", result.Dir, result.Err, result.Diags)
		}
	}

	writeFileT(t, pagePath, `package page

import "example.com/dep"

component Page(value dep.Value) { <p>{value}</p> }
`)
	results, err := session.regenPending(map[string]bool{pageDir: true}, false)
	if err != nil {
		t.Fatalf("regenPending: %v", err)
	}
	var pageResult *cycleResult
	for index := range results {
		if results[index].Dir == pageDir {
			pageResult = &results[index]
			break
		}
	}
	if pageResult == nil || !pageResult.OK {
		t.Fatalf("page refresh result=%+v all=%+v", pageResult, results)
	}
	generated, err := os.ReadFile(filepath.Join(pageDir, "page.x.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(generated), "dep.Value") {
		t.Fatalf("watch output did not use refreshed import:\n%s", generated)
	}
}
