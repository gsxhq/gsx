package gen

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/gsxhq/gsx/internal/attrclass"
)

// writeCacheBoundaryGoCommand installs a fake `go` on PATH that answers the
// environment probes from a fixed script and forwards everything else to the
// real toolchain.
//
// GSX_COMMAND_LOG records one line per forwarded (non-`go env`) command. It is
// append-only on purpose: packages.Load runs several `go list` invocations
// CONCURRENTLY — golist.go fills in Sizes from a goroutine, so the
// `{{context.GOARCH}} {{context.Compiler}}` probe overlaps the driver's
// `{{context.ReleaseTags}}` probe to the microsecond. A read-modify-write
// counter loses updates under that overlap (and reads an empty file inside the
// other writer's truncate window, resetting the count to 1), which is exactly
// how this harness flaked in CI. Appends of one short line are atomic, and the
// log doubles as the diagnostic when a command-count assertion fails.
//
// GSX_CREATE_VENDOR_AFTER_FIRST_COMMAND creates GSX_CREATE_VENDOR_DIR from the
// second forwarded command onwards — i.e. once the metadata graph list has been
// taken, so vendor/ appears while packages.Load is running. Each command counts
// the log after appending its own line, so the ordering is decided by durable
// state rather than by a racing counter.
func writeCacheBoundaryGoCommand(t *testing.T, compiler string) string {
	t.Helper()
	realGo, err := exec.LookPath("go")
	if err != nil {
		t.Fatal(err)
	}
	goRoot := t.TempDir()
	bin := filepath.Join(goRoot, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	command := filepath.Join(bin, "go")
	script := `#!/bin/sh
if [ "$1" = "env" ] && [ "$2" = "-json" ] && [ "$3" = "GOWORK" ]; then
	printf '{"GOWORK":"off","GOTOOLDIR":"%s","GOHOSTOS":"%s","GOROOT":"%s","GOVERSION":"go1.26.1","GOTOOLCHAIN":"go1.26.1+auto"}' "$GSX_FAKE_TOOL_DIR" "$GSX_FAKE_HOST_OS" "$GSX_FAKE_GOROOT"
	exit 0
fi
if [ "$1" = "env" ] && [ "$2" = "-json" ] && [ "$3" = "GOTOOLDIR" ]; then
	printf '{"GOTOOLDIR":"%s","GOHOSTOS":"%s","GOROOT":"%s","GOVERSION":"go1.26.1"}' "$GSX_FAKE_TOOL_DIR" "$GSX_FAKE_HOST_OS" "$GSX_FAKE_GOROOT"
	exit 0
fi
if [ "$1" = "env" ] && [ "$2" = "-json" ] && [ -z "$3" ] && [ -n "$GSX_CREATE_VENDOR_DURING_FINGERPRINT_MARKER" ]; then
	/bin/mkdir -p "$GSX_CREATE_VENDOR_DIR"
	: > "$GSX_CREATE_VENDOR_DURING_FINGERPRINT_MARKER"
fi
if [ "$1" = "env" ] && [ "$2" = "-json" ] && [ -z "$3" ]; then
	printf '{"GOFLAGS":"%s","GOWORK":"off","GOTOOLDIR":"%s","GOHOSTOS":"%s","GOROOT":"%s","GOVERSION":"go1.26.1","GOTOOLCHAIN":"go1.26.1+auto","GOENV":"/persisted/go/env","GOGCCFLAGS":"transient"}' "$GOFLAGS" "$GSX_FAKE_TOOL_DIR" "$GSX_FAKE_HOST_OS" "$GSX_FAKE_GOROOT"
	exit 0
fi
if [ "$1" = "env" ]; then
	exec "$REAL_GO" "$@"
fi
if [ "$1" = "list" ] && [ "$2" = "-deps" ] && [ -n "$GSX_FAIL_GRAPH_MARKER" ]; then
	: > "$GSX_FAIL_GRAPH_MARKER"
	exit 1
fi
if [ -n "$GSX_COMMAND_LOG" ]; then
	printf '%s\n' "$*" >> "$GSX_COMMAND_LOG"
	commands=0
	while read -r _; do
		commands=$((commands + 1))
	done < "$GSX_COMMAND_LOG"
	if [ -n "$GSX_CREATE_VENDOR_AFTER_FIRST_COMMAND" ] && [ "$commands" -ge 2 ]; then
		/bin/mkdir -p "$GSX_CREATE_VENDOR_DIR"
	fi
fi
if [ -n "$GSX_CREATE_VENDOR_MARKER" ] && [ ! -e "$GSX_CREATE_VENDOR_MARKER" ]; then
	/bin/mkdir -p "$GSX_CREATE_VENDOR_DIR"
	: > "$GSX_CREATE_VENDOR_MARKER"
fi
if [ -n "$GSX_MUTATE_COMPILER_MARKER" ] && [ ! -e "$GSX_MUTATE_COMPILER_MARKER" ]; then
	printf 'compiler version two' > "$GSX_FAKE_COMPILER"
	: > "$GSX_MUTATE_COMPILER_MARKER"
fi
exec "$REAL_GO" "$@"
`
	if err := os.WriteFile(command, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	t.Setenv("REAL_GO", realGo)
	t.Setenv("GSX_FAKE_COMPILER", compiler)
	t.Setenv("GSX_FAKE_TOOL_DIR", filepath.Dir(compiler))
	t.Setenv("GSX_FAKE_HOST_OS", runtime.GOOS)
	t.Setenv("GSX_FAKE_GOROOT", goRoot)
	t.Setenv("GOWORK", "off")
	t.Setenv("GOENV", "off")
	t.Setenv("GOFLAGS", "")
	t.Setenv("GOPACKAGESDRIVER", "off")
	return command
}

// enableGoCommandLog points the fake `go` launcher at a fresh command log and
// returns the reader for it. The reader reports the forwarded commands in the
// order they started, so assertions can name what ran instead of comparing a
// bare integer.
func enableGoCommandLog(t *testing.T) func() []string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "go-commands")
	t.Setenv("GSX_COMMAND_LOG", path)
	return func() []string {
		t.Helper()
		data, err := os.ReadFile(path)
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			t.Fatalf("read go command log: %v", err)
		}
		var commands []string
		for line := range strings.SplitSeq(string(data), "\n") {
			if strings.TrimSpace(line) != "" {
				commands = append(commands, line)
			}
		}
		return commands
	}
}

// TestCacheBoundaryGoCommandLogRecordsConcurrentCommands pins the harness
// property every command-count assertion below rests on. packages.Load runs
// `go list` invocations concurrently, so the launcher's log must record all of
// them; the read-modify-write counter this replaced dropped commands under that
// overlap and flaked in CI. No packages.Load here — `go version` forwards in
// milliseconds.
func TestCacheBoundaryGoCommandLogRecordsConcurrentCommands(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell Go launcher probe is Unix-only")
	}
	compiler := filepath.Join(t.TempDir(), "compile")
	if err := os.WriteFile(compiler, []byte("compiler version one"), 0o755); err != nil {
		t.Fatal(err)
	}
	command := writeCacheBoundaryGoCommand(t, compiler)
	goCommands := enableGoCommandLog(t)

	const concurrent = 8
	errs := make([]error, concurrent)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range concurrent {
		wg.Go(func() {
			<-start
			errs[i] = exec.Command(command, "version").Run()
		})
	}
	close(start)
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("concurrent forwarded command %d: %v", i, err)
		}
	}
	if commands := goCommands(); len(commands) != concurrent {
		t.Fatalf("logged commands = %d, want %d: %q", len(commands), concurrent, commands)
	}
}

// isMetadataGraphList reports whether cmd is the cache's own
// `go list -deps` metadata graph query (gen/cachekey.go), the command that must
// precede any packages.Load on a cache path.
func isMetadataGraphList(cmd string) bool {
	return strings.HasPrefix(cmd, "list -deps ")
}

func TestCacheColdWarmEdit(t *testing.T) {
	repoRoot, _ := filepath.Abs("..")
	tmp := t.TempDir()
	os.WriteFile(filepath.Join(tmp, "go.mod"), []byte("module ex/c\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n"), 0o644)
	mkgsx := func(p, body string) {
		os.MkdirAll(filepath.Join(tmp, p), 0o755)
		os.WriteFile(filepath.Join(tmp, p, p+".gsx"), []byte(body), 0o644)
	}
	mkgsx("v", "package v\n\ncomponent A(name string) { <p>{name}</p> }\n")
	mkgsx("w", "package w\n\ncomponent B() { <div>hi</div> }\n")
	t.Setenv("GSXCACHE", t.TempDir())

	// cold: both miss and generate
	res, report, err := generateCachedWithReport([]string{tmp}, nil, nil, nil, attrclass.Builtin(), true, nil, nil, nil, true, true, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Written) != 2 {
		t.Fatalf("cold: want 2 written, got %v", res.Written)
	}
	hits, misses, uncacheable := report.counts()
	if hits != 0 || misses != 2 || uncacheable != 0 || !report.semanticGeneration() {
		t.Fatalf("cold cache report = %+v", report)
	}

	// warm no-op: both hit; restores are skipped when on-disk matches.
	res, report, err = generateCachedWithReport([]string{tmp}, nil, nil, nil, attrclass.Builtin(), true, nil, nil, nil, true, true, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Written) != 0 {
		t.Fatalf("warm no-op: want 0 written, got %v", res.Written)
	}
	hits, misses, uncacheable = report.counts()
	if hits != 2 || misses != 0 || uncacheable != 0 || report.semanticGeneration() {
		t.Fatalf("warm cache report = %+v", report)
	}

	// edit only v -> only v regenerates
	mkgsx("v", "package v\n\ncomponent A(name string) { <p>Hi {name}</p> }\n")
	res, err = generateCached([]string{tmp}, nil, nil, nil, attrclass.Builtin(), true, nil, nil, nil, true, true, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Written) != 1 || filepath.Base(filepath.Dir(res.Written[0])) != "v" {
		t.Fatalf("edit v: want only v written, got %v", res.Written)
	}
}

func TestCacheIgnoresUnrelatedBrokenPackage(t *testing.T) {
	repoRoot, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module ex/cache-selected\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	viewsDir := filepath.Join(root, "views")
	if err := os.MkdirAll(viewsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(viewsDir, "view.gsx"), []byte("package views\n\ncomponent View() { <p>safe</p> }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	brokenDir := filepath.Join(root, "broken")
	if err := os.MkdirAll(brokenDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(brokenDir, "broken.go"), []byte("package\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GSXCACHE", t.TempDir())

	if _, _, err := generateCachedWithReport([]string{viewsDir}, nil, nil, nil, attrclass.Builtin(), true, nil, nil, nil, true, true, false, nil); err != nil {
		t.Fatalf("seed cache for selected views: %v", err)
	}
	_, report, err := generateCachedWithReport([]string{viewsDir}, nil, nil, nil, attrclass.Builtin(), true, nil, nil, nil, true, true, false, nil)
	if err != nil {
		t.Fatalf("warm cache for selected views: %v", err)
	}
	hits, misses, uncacheable := report.counts()
	if hits != 1 || misses != 0 || uncacheable != 0 || report.semanticGeneration() {
		t.Fatalf("warm selected cache report = %+v", report)
	}
}

func TestCacheWarmHitAvoidsSemanticPackagesLoad(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell Go launcher probe is Unix-only")
	}
	repoRoot, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module ex/cache-warm-hit\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "view")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "view.gsx"), []byte("package view\n\ncomponent View() { <p>safe</p> }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	compiler := filepath.Join(t.TempDir(), "compile")
	if err := os.WriteFile(compiler, []byte("compiler version one"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeCacheBoundaryGoCommand(t, compiler)
	t.Setenv("GOFLAGS", "-mod=mod")
	t.Setenv("GSXCACHE", t.TempDir())

	if _, err := generateCached([]string{root}, nil, nil, nil, attrclass.Builtin(), true, nil, nil, nil, true, true, false, nil); err != nil {
		t.Fatalf("seed cache: %v", err)
	}
	xgo := filepath.Join(dir, "view.x.go")
	wantSource, err := os.ReadFile(xgo)
	if err != nil {
		t.Fatal(err)
	}
	wantInfo, err := os.Stat(xgo)
	if err != nil {
		t.Fatal(err)
	}
	goCommands := enableGoCommandLog(t)

	_, report, err := generateCachedWithReport([]string{root}, nil, nil, nil, attrclass.Builtin(), true, nil, nil, nil, true, true, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	hits, misses, uncacheable := report.counts()
	if hits != 1 || misses != 0 || uncacheable != 0 || report.semanticGeneration() {
		t.Fatalf("warm cache report = %+v", report)
	}
	if commands := goCommands(); len(commands) != 1 || !isMetadataGraphList(commands[0]) {
		t.Fatalf("warm non-env go commands = %q, want the metadata go list only", commands)
	}
	gotSource, err := os.ReadFile(xgo)
	if err != nil {
		t.Fatal(err)
	}
	gotInfo, err := os.Stat(xgo)
	if err != nil {
		t.Fatal(err)
	}
	if string(gotSource) != string(wantSource) || !gotInfo.ModTime().Equal(wantInfo.ModTime()) {
		t.Fatalf("warm all-hit changed generated output: bytes equal=%v modtime before=%s after=%s", string(gotSource) == string(wantSource), wantInfo.ModTime(), gotInfo.ModTime())
	}
}

func TestCacheGraphFailureRegeneratesSelectedDirsWithoutStore(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell Go launcher probe is Unix-only")
	}
	repoRoot, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module ex/cache-graph-failure\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"a", "b"} {
		dir := filepath.Join(root, name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name+".gsx"), []byte("package "+name+"\n\ncomponent View() { <p>"+name+"</p> }\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	compiler := filepath.Join(t.TempDir(), "compile")
	if err := os.WriteFile(compiler, []byte("compiler version one"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeCacheBoundaryGoCommand(t, compiler)
	t.Setenv("GOFLAGS", "-mod=mod")
	cacheRoot := t.TempDir()
	t.Setenv("GSXCACHE", cacheRoot)
	marker := filepath.Join(t.TempDir(), "graph-failed")
	t.Setenv("GSX_FAIL_GRAPH_MARKER", marker)

	res, report, err := generateCachedWithReport([]string{root}, nil, nil, nil, attrclass.Builtin(), true, nil, nil, nil, true, true, false, nil)
	if err != nil {
		t.Fatalf("graph-failure fallback: %v", err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("graph query was not failed by probe: %v", err)
	}
	hits, misses, uncacheable := report.counts()
	if hits != 0 || misses != 0 || uncacheable != 2 || !report.semanticGeneration() {
		t.Fatalf("graph-failure report = %+v", report)
	}
	if len(res.Written) != 2 {
		t.Fatalf("graph-failure written = %v, want both selected dirs", res.Written)
	}
	entries, err := os.ReadDir(cacheRoot)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("graph-failure stored entries under uncertain key: %v", entries)
	}
}

func TestCachePartialMainModuleCgoPreservesSiblingHit(t *testing.T) {
	cgoEnabled, err := exec.Command("go", "env", "CGO_ENABLED").Output()
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(cgoEnabled)) != "1" {
		t.Skip("selected Go environment has cgo disabled")
	}
	repoRoot, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module ex/cache-partial-cgo\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cacheableDir := filepath.Join(root, "cacheable")
	cgoDir := filepath.Join(root, "cgoview")
	for _, dir := range []string{cacheableDir, cgoDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		name := filepath.Base(dir)
		if err := os.WriteFile(filepath.Join(dir, name+".gsx"), []byte("package "+name+"\n\ncomponent View() { <p>"+name+"</p> }\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(cgoDir, "bridge.go"), []byte("package cgoview\n\n// #include <stdlib.h>\nimport \"C\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GSXCACHE", t.TempDir())

	_, coldReport, err := generateCachedWithReport([]string{root}, nil, nil, nil, attrclass.Builtin(), true, nil, nil, nil, true, true, false, nil)
	if err != nil {
		t.Fatalf("cold partial-cgo generate: %v", err)
	}
	hits, misses, uncacheable := coldReport.counts()
	if hits != 0 || misses != 1 || uncacheable != 1 || !coldReport.semanticGeneration() {
		t.Fatalf("cold partial-cgo report = %+v", coldReport)
	}
	for _, path := range []string{filepath.Join(cacheableDir, "cacheable.x.go"), filepath.Join(cgoDir, "cgoview.x.go")} {
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
	}

	res, warmReport, err := generateCachedWithReport([]string{root}, nil, nil, nil, attrclass.Builtin(), true, nil, nil, nil, true, true, false, nil)
	if err != nil {
		t.Fatalf("warm partial-cgo generate: %v", err)
	}
	hits, misses, uncacheable = warmReport.counts()
	if hits != 1 || misses != 0 || uncacheable != 1 || !warmReport.semanticGeneration() {
		t.Fatalf("warm partial-cgo report = %+v", warmReport)
	}
	if len(res.Written) != 2 {
		t.Fatalf("warm partial-cgo written = %v, want restored hit and regenerated cgo output", res.Written)
	}
	decisions := map[string]cacheDecisionKind{}
	for _, module := range warmReport.Modules {
		for _, dirReport := range module.Dirs {
			decisions[dirReport.Dir] = dirReport.Decision
		}
	}
	if decisions[cacheableDir] != cacheDecisionHit || decisions[cgoDir] != cacheDecisionUncacheable {
		t.Fatalf("warm partial-cgo decisions = %v", decisions)
	}
}

func TestCacheWarmHitWithStdlibCgo(t *testing.T) {
	cgoEnabled, err := exec.Command("go", "env", "CGO_ENABLED").Output()
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(cgoEnabled)) != "1" {
		t.Skip("selected Go environment has cgo disabled")
	}

	repoRoot, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module ex/cache-stdlib-cgo\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "view")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "view.gsx"), []byte("package view\n\ncomponent View() { <p>safe</p> }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "view.go"), []byte("package view\n\nimport _ \"plugin\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	graph, err := loadGraph(root)
	if err != nil {
		t.Fatal(err)
	}
	runtimeCgo, ok := graph["runtime/cgo"]
	if !ok || !runtimeCgo.Standard || len(runtimeCgo.CgoFiles) == 0 {
		t.Fatalf("selected graph runtime/cgo = %+v, want standard cgo package", runtimeCgo)
	}

	t.Setenv("GSXCACHE", t.TempDir())
	if _, _, err := generateCachedWithReport([]string{root}, nil, nil, nil, attrclass.Builtin(), true, nil, nil, nil, true, true, false, nil); err != nil {
		t.Fatalf("cold cached generate: %v", err)
	}
	_, report, err := generateCachedWithReport([]string{root}, nil, nil, nil, attrclass.Builtin(), true, nil, nil, nil, true, true, false, nil)
	if err != nil {
		t.Fatalf("warm cached generate: %v", err)
	}
	hits, misses, uncacheable := report.counts()
	if hits != 1 || misses != 0 || uncacheable != 0 || report.semanticGeneration() {
		t.Fatalf("warm cache report = %+v", report)
	}
}

func TestCacheFingerprintProvenanceFailureDoesNotFallBackToGeneration(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell Go launcher probe is Unix-only")
	}
	repoRoot, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module ex/fingerprint-boundary\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "view")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "view.gsx"), []byte("package view\n\ncomponent View() { <p>safe</p> }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	compiler := filepath.Join(t.TempDir(), "compile")
	if err := os.WriteFile(compiler, []byte("compiler version one"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeCacheBoundaryGoCommand(t, compiler)
	t.Setenv("GOFLAGS", "-mod=mod")
	t.Setenv("GSXCACHE", t.TempDir())
	marker := filepath.Join(t.TempDir(), "created-vendor")
	t.Setenv("GSX_CREATE_VENDOR_DURING_FINGERPRINT_MARKER", marker)
	t.Setenv("GSX_CREATE_VENDOR_DIR", filepath.Join(root, "vendor"))

	res, err := generateCached([]string{root}, nil, nil, nil, attrclass.Builtin(), true, nil, nil, nil, true, true, false, nil)
	if err == nil || !strings.Contains(err.Error(), "vendor directory state changed") {
		t.Fatalf("generate error = %v, want fingerprint provenance failure", err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("fingerprint command did not create vendor directory: %v", err)
	}
	if len(res.Written) != 0 {
		t.Fatalf("fingerprint provenance failure wrote files through fallback: %v", res.Written)
	}
	if _, err := os.Stat(filepath.Join(dir, "view.x.go")); !os.IsNotExist(err) {
		t.Fatalf("fingerprint provenance failure generated output through fallback; stat error = %v", err)
	}
}

func TestCacheMissRejectsVendorAppearanceDuringPackagesLoad(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell Go launcher probe is Unix-only")
	}
	repoRoot, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module ex/packages-load-boundary\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "view")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "view.gsx"), []byte("package view\n\ncomponent View() { <p>safe</p> }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	compiler := filepath.Join(t.TempDir(), "compile")
	if err := os.WriteFile(compiler, []byte("compiler version one"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeCacheBoundaryGoCommand(t, compiler)
	t.Setenv("GOFLAGS", "-mod=mod")
	cacheRoot := t.TempDir()
	t.Setenv("GSXCACHE", cacheRoot)
	goCommands := enableGoCommandLog(t)
	t.Setenv("GSX_CREATE_VENDOR_AFTER_FIRST_COMMAND", "1")
	t.Setenv("GSX_CREATE_VENDOR_DIR", filepath.Join(root, "vendor"))

	res, err := generateCached([]string{root}, nil, nil, nil, attrclass.Builtin(), true, nil, nil, nil, true, true, false, nil)
	if err == nil || !strings.Contains(err.Error(), "vendor directory state changed") {
		t.Fatalf("generate error = %v, want packages.Load vendor mutation rejection", err)
	}
	// vendor/ appears from the second command onwards, so the rejection is only
	// meaningful if the metadata graph list ran first and a packages.Load
	// followed it — assert that shape, not just a count.
	commands := goCommands()
	if len(commands) < 2 || !isMetadataGraphList(commands[0]) {
		t.Fatalf("semantic go commands = %q, want the metadata graph list followed by packages.Load", commands)
	}
	for _, cmd := range commands[1:] {
		if isMetadataGraphList(cmd) {
			t.Fatalf("semantic go commands = %q, want packages.Load after the metadata graph list", commands)
		}
	}
	if len(res.Written) != 0 {
		t.Fatalf("packages.Load provenance failure wrote files: %v", res.Written)
	}
	if _, err := os.Stat(filepath.Join(dir, "view.x.go")); !os.IsNotExist(err) {
		t.Fatalf("packages.Load provenance failure generated output; stat error = %v", err)
	}
	entries, err := os.ReadDir(cacheRoot)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("packages.Load provenance failure stored cache entries: %v", entries)
	}
}

func TestCacheHitRejectsCompilerMutationDuringGraphBeforeRestore(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell Go launcher probe is Unix-only")
	}
	repoRoot, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module ex/toolchain-boundary\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "view")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "view.gsx"), []byte("package view\n\ncomponent View() { <p>safe</p> }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	compiler := filepath.Join(t.TempDir(), "compile")
	if err := os.WriteFile(compiler, []byte("compiler version one"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeCacheBoundaryGoCommand(t, compiler)
	t.Setenv("GSXCACHE", t.TempDir())

	if _, err := generateCached([]string{root}, nil, nil, nil, attrclass.Builtin(), true, nil, nil, nil, true, true, false, nil); err != nil {
		t.Fatalf("populate cache: %v", err)
	}
	xgo := filepath.Join(dir, "view.x.go")
	if err := os.Remove(xgo); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(t.TempDir(), "mutated")
	t.Setenv("GSX_MUTATE_COMPILER_MARKER", marker)

	res, err := generateCached([]string{root}, nil, nil, nil, attrclass.Builtin(), true, nil, nil, nil, true, true, false, nil)
	if err == nil || !strings.Contains(err.Error(), "compiler") {
		t.Fatalf("all-HIT generate error = %v, want compiler mutation rejection", err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("graph command did not mutate compiler: %v", err)
	}
	if len(res.Written) != 0 {
		t.Fatalf("stale cache HIT wrote files before validation: %v", res.Written)
	}
	if _, err := os.Stat(xgo); !os.IsNotExist(err) {
		t.Fatalf("stale cache HIT restored %s before validation; stat error = %v", xgo, err)
	}
}

func TestCacheHitRejectsVendorAppearanceDuringGraphBeforeRestore(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell Go launcher probe is Unix-only")
	}
	repoRoot, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module ex/vendor-boundary\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "view")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "view.gsx"), []byte("package view\n\ncomponent View() { <p>safe</p> }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	compiler := filepath.Join(t.TempDir(), "compile")
	if err := os.WriteFile(compiler, []byte("compiler version one"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeCacheBoundaryGoCommand(t, compiler)
	t.Setenv("GOFLAGS", "-mod=mod")
	t.Setenv("GSXCACHE", t.TempDir())

	if _, err := generateCached([]string{root}, nil, nil, nil, attrclass.Builtin(), true, nil, nil, nil, true, true, false, nil); err != nil {
		t.Fatalf("populate cache: %v", err)
	}
	xgo := filepath.Join(dir, "view.x.go")
	if err := os.Remove(xgo); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(t.TempDir(), "created-vendor")
	t.Setenv("GSX_CREATE_VENDOR_MARKER", marker)
	t.Setenv("GSX_CREATE_VENDOR_DIR", filepath.Join(root, "vendor"))

	res, err := generateCached([]string{root}, nil, nil, nil, attrclass.Builtin(), true, nil, nil, nil, true, true, false, nil)
	if err == nil || !strings.Contains(err.Error(), "vendor directory state changed") {
		t.Fatalf("all-HIT generate error = %v, want vendor appearance rejection", err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("graph command did not create vendor directory: %v", err)
	}
	if len(res.Written) != 0 {
		t.Fatalf("stale cache HIT wrote files before vendor validation: %v", res.Written)
	}
	if _, err := os.Stat(xgo); !os.IsNotExist(err) {
		t.Fatalf("stale cache HIT restored %s before vendor validation; stat error = %v", xgo, err)
	}
}

func TestCacheMissRejectsVendorAppearanceDuringGraphBeforeGenerate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell Go launcher probe is Unix-only")
	}
	repoRoot, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module ex/vendor-miss-boundary\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "view")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "view.gsx"), []byte("package view\n\ncomponent View() { <p>safe</p> }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	compiler := filepath.Join(t.TempDir(), "compile")
	if err := os.WriteFile(compiler, []byte("compiler version one"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeCacheBoundaryGoCommand(t, compiler)
	t.Setenv("GOFLAGS", "-mod=mod")
	t.Setenv("GSXCACHE", t.TempDir())
	marker := filepath.Join(t.TempDir(), "created-vendor")
	t.Setenv("GSX_CREATE_VENDOR_MARKER", marker)
	t.Setenv("GSX_CREATE_VENDOR_DIR", filepath.Join(root, "vendor"))

	res, err := generateCached([]string{root}, nil, nil, nil, attrclass.Builtin(), true, nil, nil, nil, true, true, false, nil)
	if err == nil || !strings.Contains(err.Error(), "vendor directory state changed") {
		t.Fatalf("all-MISS generate error = %v, want vendor appearance rejection", err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("graph command did not create vendor directory: %v", err)
	}
	if len(res.Written) != 0 {
		t.Fatalf("stale cache MISS wrote files before vendor validation: %v", res.Written)
	}
	if _, err := os.Stat(filepath.Join(dir, "view.x.go")); !os.IsNotExist(err) {
		t.Fatalf("stale cache MISS generated output before vendor validation; stat error = %v", err)
	}
}

// TestNoCacheBypassesCache proves that useCache=false regenerates even when
// the content-hash cache is warm. We delete the on-disk .x.go between runs
// so the hash-gated write fires, giving a concrete Written count to assert on.
func TestNoCacheBypassesCache(t *testing.T) {
	repoRoot, _ := filepath.Abs("..")
	tmp := t.TempDir()
	os.WriteFile(filepath.Join(tmp, "go.mod"), []byte("module ex/nc\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n"), 0o644)
	mkgsx := func(p, body string) {
		os.MkdirAll(filepath.Join(tmp, p), 0o755)
		os.WriteFile(filepath.Join(tmp, p, p+".gsx"), []byte(body), 0o644)
	}
	mkgsx("v", "package v\n\ncomponent A(name string) { <p>{name}</p> }\n")
	t.Setenv("GSXCACHE", t.TempDir())

	// warm the cache
	res, err := generateCached([]string{tmp}, nil, nil, nil, attrclass.Builtin(), true, nil, nil, nil, true, true, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Written) != 1 {
		t.Fatalf("cold: want 1 written, got %v", res.Written)
	}

	// delete the .x.go so the no-cache path must actually write it again
	xgo := filepath.Join(tmp, "v", "v.x.go")
	if err := os.Remove(xgo); err != nil {
		t.Fatal(err)
	}

	// with --no-cache (useCache=false): regenerates despite warm cache → Written=1
	res, err = generateCached([]string{tmp}, nil, nil, nil, attrclass.Builtin(), false, nil, nil, nil, true, true, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Written) != 1 {
		t.Fatalf("--no-cache: want 1 written (regenerated from scratch), got %v", res.Written)
	}
	if len(res.Errs) != 0 {
		t.Fatalf("--no-cache: unexpected errors: %v", res.Errs)
	}
}

func TestRestore_AtomicNoTempLeftovers(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	po := pkgOutput{"a.x.go": []byte("package a\n"), "b.x.go": []byte("package a\n")}
	written, upToDate, err := restore(dir, po)
	if err != nil {
		t.Fatal(err)
	}
	if len(written) != 2 || upToDate != 0 {
		t.Fatalf("written=%v upToDate=%d", written, upToDate)
	}
	// Second run: byte-identical, no writes.
	written, upToDate, err = restore(dir, po)
	if err != nil {
		t.Fatal(err)
	}
	if len(written) != 0 || upToDate != 2 {
		t.Fatalf("expected 0 writes / 2 up-to-date, got written=%v upToDate=%d", written, upToDate)
	}
	// No temp files left behind.
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ents {
		if !strings.HasSuffix(e.Name(), ".x.go") {
			t.Errorf("leftover non-output file: %s", e.Name())
		}
	}
	// Output files are world-readable (0644-equivalent), not CreateTemp's 0600.
	fi, err := os.Stat(filepath.Join(dir, "a.x.go"))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm()&0o044 == 0 {
		t.Errorf("output not group/world readable: %v", fi.Mode())
	}
}
