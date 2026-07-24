package gen

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/gsxhq/gsx/internal/attrclass"
	"github.com/gsxhq/gsx/internal/codegen"
	"github.com/gsxhq/gsx/internal/sourceview"
)

type realisticCacheFixture struct {
	root           string
	pagesDir       string
	uiDir          string
	iconsDir       string
	replacementDir string
}

func newRealisticCacheFixture(tb testing.TB) realisticCacheFixture {
	tb.Helper()
	repoRoot, err := filepath.Abs("..")
	if err != nil {
		tb.Fatal(err)
	}
	parent := tb.TempDir()
	root := filepath.Join(parent, "app")
	replacementDir := filepath.Join(parent, "local")
	write := func(base, rel, source string) {
		tb.Helper()
		path := filepath.Join(base, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			tb.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(source), 0o644); err != nil {
			tb.Fatal(err)
		}
	}

	write(root, "go.mod", fmt.Sprintf(`module example.com/app

go 1.26.1

require (
	example.com/local v0.0.0
	github.com/gsxhq/gsx v0.0.0
)

replace example.com/local => ../local

replace github.com/gsxhq/gsx => %s
`, filepath.ToSlash(repoRoot)))
	write(root, "go.sum", "")
	write(replacementDir, "go.mod", "module example.com/local\n\ngo 1.26.1\n")
	write(replacementDir, "go.sum", "")
	write(replacementDir, "model/model.go", "package model\n\nconst Label = \"replacement-v1\"\n")

	write(root, "icons/icon.gsx", "package icons\n\ncomponent Icon() { <i>icon-v1</i> }\n")
	write(root, "ui/card.gsx", `package ui

import (
	"example.com/app/icons"
	"example.com/local/model"
)

component Card() {
	<section><icons.Icon/><span>{ model.Label }</span><span>{ activePlatform }</span></section>
}
`)
	write(root, "pages/home.gsx", `package pages

import "example.com/app/ui"

component Home() { <main><ui.Card/></main> }
`)

	excludedGOOS := realisticCacheExcludedGOOS()
	write(root, "ui/active_"+runtime.GOOS+".go", fmt.Sprintf("//go:build %s\n\npackage ui\n\nconst activePlatform = %q\n", runtime.GOOS, runtime.GOOS+"-v1"))
	write(root, "ui/inactive_"+excludedGOOS+".go", fmt.Sprintf("//go:build %s\n\npackage ui\n\nconst inactivePlatform = %q\n", excludedGOOS, excludedGOOS+"-v1"))

	write(root, "unrelatedcgo/cgo.go", "package unrelatedcgo\n\n/* static int unrelated(void) { return 1; } */\nimport \"C\"\n")
	write(root, "unrelatedembed/embed.go", "package unrelatedembed\n\nimport _ \"embed\"\n\n//go:embed missing.txt\nvar missing string\n")
	write(root, "admin/broken.go", "package admin\n\nvar broken int = \"unrelated type error\"\n")
	write(root, "nested/go.mod", "module example.com/nested\n\ngo 1.26.1\n")
	write(root, "nested/view/view.gsx", "package view\n\ncomponent Nested() { <p>nested</p> }\n")

	return realisticCacheFixture{
		root:           root,
		pagesDir:       filepath.Join(root, "pages"),
		uiDir:          filepath.Join(root, "ui"),
		iconsDir:       filepath.Join(root, "icons"),
		replacementDir: replacementDir,
	}
}

func TestRealisticCacheSelectedGraph(t *testing.T) {
	fixture := newRealisticCacheFixture(t)
	goContext := codegen.CaptureGoCommandContext(fixture.root)
	if output, err := goContext.Run("list", "./..."); err == nil {
		t.Fatalf("broad module graph query unexpectedly succeeded:\n%s", output)
	} else if detail := string(output) + err.Error(); !strings.Contains(detail, "missing.txt") {
		t.Fatalf("broad module graph query failed without exercising missing go:embed trap: %s", detail)
	}

	manifest, err := sourceview.Build(sourceview.BuildOptions{
		ModuleRoot: fixture.root,
		ModulePath: "example.com/app",
	})
	if err != nil {
		t.Fatal(err)
	}
	graph, err := loadGraphWithContext(
		goContext,
		manifest,
		[]string{fixture.pagesDir},
		[]string{"github.com/gsxhq/gsx"},
	)
	if err != nil {
		t.Fatalf("selected graph query: %v", err)
	}
	for importPath, wantDir := range map[string]string{
		"example.com/app/pages":   fixture.pagesDir,
		"example.com/app/ui":      fixture.uiDir,
		"example.com/app/icons":   fixture.iconsDir,
		"example.com/local/model": filepath.Join(fixture.replacementDir, "model"),
	} {
		metadata, ok := graph[importPath]
		if !ok {
			t.Fatalf("selected graph is missing %q", importPath)
		}
		gotDir, err := filepath.EvalSymlinks(metadata.Dir)
		if err != nil {
			t.Fatal(err)
		}
		wantDir, err = filepath.EvalSymlinks(wantDir)
		if err != nil {
			t.Fatal(err)
		}
		if gotDir != wantDir {
			t.Fatalf("selected graph directory for %q = %q, want %q", importPath, metadata.Dir, wantDir)
		}
	}
	for _, importPath := range []string{
		"example.com/app/admin",
		"example.com/app/unrelatedcgo",
		"example.com/app/unrelatedembed",
		"example.com/nested/view",
	} {
		if _, ok := graph[importPath]; ok {
			t.Fatalf("selected graph unexpectedly contains %q", importPath)
		}
	}
	nestedRoot := filepath.Join(fixture.root, "nested")
	for importPath, metadata := range graph {
		if metadata.Dir != "" && sourceview.PathWithin(nestedRoot, metadata.Dir) {
			t.Fatalf("selected graph unexpectedly contains nested-module package %q at %s", importPath, metadata.Dir)
		}
	}
}

func TestRealisticCacheColdWarm(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell Go launcher probe is Unix-only")
	}
	fixture := newRealisticCacheFixture(t)
	compiler := filepath.Join(t.TempDir(), "compile")
	if err := os.WriteFile(compiler, []byte("compiler version one"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeCacheBoundaryGoCommand(t, compiler)
	t.Setenv("GOFLAGS", "-mod=mod")
	cacheRoot := t.TempDir()
	t.Setenv("GSXCACHE", cacheRoot)
	generate := func() (Result, cacheReport, error) {
		return generateCachedWithReport(
			[]string{fixture.pagesDir}, nil, nil, nil,
			attrclass.Builtin(), true,
			nil, nil, nil, true, true, false, nil,
		)
	}

	cold, coldReport, err := generate()
	if err != nil {
		t.Fatalf("cold generation: %v", err)
	}
	if len(cold.Written) != 1 || cold.Written[0] != filepath.Join(fixture.pagesDir, "home.x.go") {
		t.Fatalf("cold written = %v, want pages/home.x.go", cold.Written)
	}
	hits, misses, uncacheable := coldReport.counts()
	if hits != 0 || misses != 1 || uncacheable != 0 || !coldReport.semanticGeneration() {
		t.Fatalf("cold cache report = %+v", coldReport)
	}
	var cacheEntries []string
	err = filepath.WalkDir(cacheRoot, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !entry.IsDir() && entry.Name() != "CACHEDIR.TAG" {
			cacheEntries = append(cacheEntries, path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(cacheEntries) == 0 {
		t.Fatal("cold generation created no non-sentinel cache entry")
	}
	generatedPath := filepath.Join(fixture.pagesDir, "home.x.go")
	wantGenerated, err := os.ReadFile(generatedPath)
	if err != nil {
		t.Fatal(err)
	}

	counter := filepath.Join(t.TempDir(), "command-count")
	if err := os.WriteFile(counter, []byte("0"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GSX_COMMAND_COUNTER", counter)
	warm, warmReport, err := generate()
	if err != nil {
		t.Fatalf("warm generation: %v", err)
	}
	hits, misses, uncacheable = warmReport.counts()
	if hits != 1 || misses != 0 || uncacheable != 0 || warmReport.semanticGeneration() {
		t.Fatalf("warm cache report = %+v", warmReport)
	}
	if len(warm.Written) != 0 || warm.UpToDate != 1 {
		t.Fatalf("warm result = %+v, want one up-to-date output", warm)
	}
	gotCount, err := os.ReadFile(counter)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(gotCount)); got != "1" {
		t.Fatalf("warm non-env Go commands = %s, want metadata go list only", got)
	}
	gotGenerated, err := os.ReadFile(generatedPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(gotGenerated) != string(wantGenerated) {
		t.Fatal("warm cache hit changed generated bytes")
	}

	if len(warmReport.Modules) != 1 || len(warmReport.Modules[0].Dirs) != 1 || warmReport.Modules[0].Dirs[0].Dir != fixture.pagesDir {
		t.Fatalf("warm report selected unexpected directories: %+v", warmReport)
	}
	reportText := fmt.Sprintf("%+v", warmReport)
	for _, unrelated := range []string{"admin", "unrelatedcgo", "unrelatedembed", "nested"} {
		if strings.Contains(reportText, unrelated) {
			t.Fatalf("warm report contains unrelated package %q: %s", unrelated, reportText)
		}
	}
}

func TestRealisticCacheInvalidation(t *testing.T) {
	tests := []struct {
		name     string
		mutate   func(testing.TB, realisticCacheFixture)
		wantKind cacheDecisionKind
	}{
		{
			name: "pages GSX body",
			mutate: func(tb testing.TB, fixture realisticCacheFixture) {
				writeRealisticCacheFile(tb, filepath.Join(fixture.pagesDir, "home.gsx"), `package pages

import "example.com/app/ui"

component Home() { <main data-version="two"><ui.Card/></main> }
`)
			},
			wantKind: cacheDecisionMiss,
		},
		{
			name: "direct UI GSX dependency",
			mutate: func(tb testing.TB, fixture realisticCacheFixture) {
				writeRealisticCacheFile(tb, filepath.Join(fixture.uiDir, "card.gsx"), `package ui

import (
	"example.com/app/icons"
	"example.com/local/model"
)

component Card() {
	<section data-version="two"><icons.Icon/><span>{ model.Label }</span><span>{ activePlatform }</span></section>
}
`)
			},
			wantKind: cacheDecisionMiss,
		},
		{
			name: "transitive icons GSX dependency",
			mutate: func(tb testing.TB, fixture realisticCacheFixture) {
				writeRealisticCacheFile(tb, filepath.Join(fixture.iconsDir, "icon.gsx"), "package icons\n\ncomponent Icon() { <b>icon-v2</b> }\n")
			},
			wantKind: cacheDecisionMiss,
		},
		{
			name: "selected Go build-tag file",
			mutate: func(tb testing.TB, fixture realisticCacheFixture) {
				path := filepath.Join(fixture.uiDir, "active_"+runtime.GOOS+".go")
				writeRealisticCacheFile(tb, path, fmt.Sprintf("//go:build %s\n\npackage ui\n\nconst activePlatform = %q\n", runtime.GOOS, runtime.GOOS+"-v2"))
			},
			wantKind: cacheDecisionMiss,
		},
		{
			name: "excluded Go build-tag file",
			mutate: func(tb testing.TB, fixture realisticCacheFixture) {
				excludedGOOS := realisticCacheExcludedGOOS()
				path := filepath.Join(fixture.uiDir, "inactive_"+excludedGOOS+".go")
				writeRealisticCacheFile(tb, path, fmt.Sprintf("//go:build %s\n\npackage ui\n\nconst inactivePlatform = %q\n", excludedGOOS, excludedGOOS+"-v2"))
			},
			// Build tags exclude this file from compilation, but generated helper
			// allocation deliberately sees all same-package Go declarations.
			wantKind: cacheDecisionMiss,
		},
		{
			name: "replacement source",
			mutate: func(tb testing.TB, fixture realisticCacheFixture) {
				writeRealisticCacheFile(tb, filepath.Join(fixture.replacementDir, "model", "model.go"), "package model\n\nconst Label = \"replacement-v2\"\n")
			},
			wantKind: cacheDecisionMiss,
		},
		{
			name: "replacement go.mod",
			mutate: func(tb testing.TB, fixture realisticCacheFixture) {
				writeRealisticCacheFile(tb, filepath.Join(fixture.replacementDir, "go.mod"), "module example.com/local\n\ngo 1.26.1\n\n// cache mutation\n")
			},
			wantKind: cacheDecisionMiss,
		},
		{
			name: "replacement go.sum",
			mutate: func(tb testing.TB, fixture realisticCacheFixture) {
				writeRealisticCacheFile(tb, filepath.Join(fixture.replacementDir, "go.sum"), "example.com/unused v1.0.0 h1:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=\n")
			},
			wantKind: cacheDecisionMiss,
		},
		{
			name: "main go.mod",
			mutate: func(tb testing.TB, fixture realisticCacheFixture) {
				appendRealisticCacheFile(tb, filepath.Join(fixture.root, "go.mod"), "\n// cache mutation\n")
			},
			wantKind: cacheDecisionMiss,
		},
		{
			name: "main go.sum",
			mutate: func(tb testing.TB, fixture realisticCacheFixture) {
				writeRealisticCacheFile(tb, filepath.Join(fixture.root, "go.sum"), "example.com/unused v1.0.0 h1:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=\n")
			},
			wantKind: cacheDecisionMiss,
		},
		{
			name: "unrelated broken source",
			mutate: func(tb testing.TB, fixture realisticCacheFixture) {
				writeRealisticCacheFile(tb, filepath.Join(fixture.root, "admin", "broken.go"), "package admin\n\nvar stillBroken bool = 42\n")
			},
			wantKind: cacheDecisionHit,
		},
		{
			name: "unrelated cgo source",
			mutate: func(tb testing.TB, fixture realisticCacheFixture) {
				writeRealisticCacheFile(tb, filepath.Join(fixture.root, "unrelatedcgo", "cgo.go"), "package unrelatedcgo\n\n/* static int unrelated(void) { return 2; } */\nimport \"C\"\n")
			},
			wantKind: cacheDecisionHit,
		},
		{
			name: "nested module source",
			mutate: func(tb testing.TB, fixture realisticCacheFixture) {
				writeRealisticCacheFile(tb, filepath.Join(fixture.root, "nested", "view", "view.gsx"), "package view\n\ncomponent Nested() { <p>nested-v2</p> }\n")
			},
			wantKind: cacheDecisionHit,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newRealisticCacheFixture(t)
			t.Setenv("GSXCACHE", t.TempDir())
			generate := func() (Result, cacheReport, error) {
				return generateCachedWithReport(
					[]string{fixture.pagesDir}, nil, nil, nil,
					attrclass.Builtin(), true,
					nil, nil, nil, true, true, false, nil,
				)
			}
			if _, report, err := generate(); err != nil {
				t.Fatalf("cold generation: %v", err)
			} else if hits, misses, uncacheable := report.counts(); hits != 0 || misses != 1 || uncacheable != 0 || !report.semanticGeneration() {
				t.Fatalf("cold cache report = %+v", report)
			}
			if _, report, err := generate(); err != nil {
				t.Fatalf("warm generation: %v", err)
			} else if hits, misses, uncacheable := report.counts(); hits != 1 || misses != 0 || uncacheable != 0 || report.semanticGeneration() {
				t.Fatalf("warm cache report = %+v", report)
			}

			test.mutate(t, fixture)
			_, report, err := generate()
			if err != nil {
				t.Fatalf("generation after mutation: %v", err)
			}
			if len(report.Modules) != 1 || len(report.Modules[0].Dirs) != 1 {
				t.Fatalf("mutation report selected unexpected directories: %+v", report)
			}
			decision := report.Modules[0].Dirs[0]
			if decision.Dir != fixture.pagesDir || decision.Decision != test.wantKind {
				t.Fatalf("pages decision = %+v, want kind %v", decision, test.wantKind)
			}
			switch test.wantKind {
			case cacheDecisionMiss:
				if decision.Reason != cacheReasonEntryMissing || !report.semanticGeneration() {
					t.Fatalf("miss report = %+v, want missing entry and semantic generation", report)
				}
			case cacheDecisionHit:
				if decision.Reason != cacheReasonEntryHit || report.semanticGeneration() {
					t.Fatalf("hit report = %+v, want entry hit without semantic generation", report)
				}
			default:
				t.Fatalf("unsupported expected decision kind %v", test.wantKind)
			}
		})
	}
}

func realisticCacheExcludedGOOS() string {
	if runtime.GOOS == "windows" {
		return "linux"
	}
	return "windows"
}

func writeRealisticCacheFile(tb testing.TB, path, source string) {
	tb.Helper()
	if err := os.WriteFile(path, []byte(source), 0o644); err != nil {
		tb.Fatal(err)
	}
}

func appendRealisticCacheFile(tb testing.TB, path, source string) {
	tb.Helper()
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		tb.Fatal(err)
	}
	if _, err := file.WriteString(source); err != nil {
		file.Close()
		tb.Fatal(err)
	}
	if err := file.Close(); err != nil {
		tb.Fatal(err)
	}
}

func BenchmarkGenerateCachedNoop(b *testing.B) {
	fixture := newRealisticCacheFixture(b)
	b.Setenv("GSXCACHE", b.TempDir())
	generate := func() (Result, cacheReport, error) {
		return generateCachedWithReport(
			[]string{fixture.pagesDir}, nil, nil, nil,
			attrclass.Builtin(), true,
			nil, nil, nil, true, true, false, nil,
		)
	}
	if _, _, err := generate(); err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	b.ReportAllocs()
	for b.Loop() {
		_, report, err := generate()
		if err != nil {
			b.Fatal(err)
		}
		if report.semanticGeneration() {
			b.Fatal("warm benchmark entered semantic generation")
		}
	}
}
