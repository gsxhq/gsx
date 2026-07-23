package gen

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"testing"

	"github.com/gsxhq/gsx/internal/attrclass"
)

// runCapture drives run with captured stdout/stderr and returns code+output.
func runCapture(t *testing.T, args []string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	code := run(args, &out, &errb)
	return code, out.String(), errb.String()
}

// TestRunGenerate proves `generate <pkgDir>` writes the .x.go, returns 0, and
// the default summary mentions wrote/1.
func TestRunGenerate(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping module-resolution test in -short mode")
	}
	mod := newModule(t, "gsxrungen")
	pkgDir := filepath.Join(mod, "views")
	writeFile(t, pkgDir, "hi.gsx", hiComponent)

	code, out, errb := runCapture(t, []string{"generate", pkgDir})
	if code != 0 {
		t.Fatalf("expected exit 0, got %d; stderr=%q", code, errb)
	}
	if !strings.Contains(out, "wrote") || !strings.Contains(out, "1") {
		t.Fatalf("expected stdout to mention wrote/1, got %q", out)
	}
	target := filepath.Join(pkgDir, "hi.x.go")
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("expected %s on disk: %v", target, err)
	}
}

// TestRunGenerateVerbose proves -v lists the written file.
func TestRunGenerateVerbose(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping module-resolution test in -short mode")
	}
	mod := newModule(t, "gsxrungenv")
	pkgDir := filepath.Join(mod, "views")
	writeFile(t, pkgDir, "hi.gsx", hiComponent)

	code, out, errb := runCapture(t, []string{"-v", "generate", pkgDir})
	if code != 0 {
		t.Fatalf("expected exit 0, got %d; stderr=%q", code, errb)
	}
	target := filepath.Join(pkgDir, "hi.x.go")
	if !strings.Contains(out, target) {
		t.Fatalf("expected verbose stdout to list %q, got %q", target, out)
	}
}

// TestRunGenerateQuiet proves -q prints nothing on success.
func TestRunGenerateQuiet(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping module-resolution test in -short mode")
	}
	mod := newModule(t, "gsxrungenq")
	pkgDir := filepath.Join(mod, "views")
	writeFile(t, pkgDir, "hi.gsx", hiComponent)

	code, out, errb := runCapture(t, []string{"-q", "generate", pkgDir})
	if code != 0 {
		t.Fatalf("expected exit 0, got %d; stderr=%q", code, errb)
	}
	if out != "" {
		t.Fatalf("expected empty stdout with -q, got %q", out)
	}
}

func TestRunGenerateCacheReport(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping module-resolution test in -short mode")
	}
	mod := newModule(t, "gsxruncachereport")
	pkgDir := filepath.Join(mod, "views")
	writeFile(t, pkgDir, "hi.gsx", hiComponent)
	t.Setenv("GSXCACHE", t.TempDir())

	code, out, errb := runCapture(t, []string{"generate", pkgDir})
	if code != 0 {
		t.Fatalf("normal generate exit = %d, stderr=%q", code, errb)
	}
	if strings.Contains(out, "gsx cache:") {
		t.Fatalf("normal generate included cache report: %q", out)
	}

	code, out, errb = runCapture(t, []string{"generate", "-v", pkgDir})
	if code != 0 {
		t.Fatalf("verbose generate exit = %d, stderr=%q", code, errb)
	}
	if !strings.Contains(out, "gsx cache:") {
		t.Fatalf("verbose generate omitted cache report: %q", out)
	}

	code, out, errb = runCapture(t, []string{"generate", "-q", "-v", pkgDir})
	if code != 0 {
		t.Fatalf("quiet verbose generate exit = %d, stderr=%q", code, errb)
	}
	if out != "" {
		t.Fatalf("quiet verbose generate output = %q, want empty", out)
	}
}

func TestRunGenerateStoreFailureIsVerboseOnly(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping module-resolution test in -short mode")
	}
	mod := newModule(t, "gsxrunstorefailure")
	pkgDir := filepath.Join(mod, "views")
	writeFile(t, pkgDir, "page.gsx", hiComponent)

	t.Setenv("GSXCACHE", t.TempDir())
	prep, _, err := prepareCache(moduleGroup{
		root:    mod,
		modPath: "gsxrunstorefailure",
		dirs:    []string{pkgDir},
	}, moduleGenerateConfig{classifier: attrclass.Builtin(), useCache: true})
	if err != nil || !prep.cacheReady {
		t.Fatalf("cache preparation = (%+v, %v)", prep, err)
	}
	key, err := computeKey(pkgDir, prep.projection, prep.keyConfig)
	if err != nil {
		t.Fatal(err)
	}
	badCache := t.TempDir()
	if err := os.WriteFile(filepath.Join(badCache, key[:2]), []byte("not a cache shard directory"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GSXCACHE", badCache)

	result, report, err := generateCachedWithReport([]string{pkgDir}, nil, nil, nil, attrclass.Builtin(), true, nil, nil, nil, false, false, false, nil)
	if err != nil || len(result.Errs) != 0 {
		t.Fatalf("store failure generation = (%+v, %v)", result, err)
	}
	if len(report.Modules) != 1 || len(report.Modules[0].StoreFailures) != 1 {
		t.Fatalf("store failure report = %+v", report)
	}

	code, out, errb := runCapture(t, []string{"generate", pkgDir})
	if code != 0 || strings.Contains(out, "cache store write failed") || errb != "" {
		t.Fatalf("normal store failure generate = (%d, %q, %q), want successful and quiet", code, out, errb)
	}
	code, out, errb = runCapture(t, []string{"generate", "-v", pkgDir})
	if code != 0 || errb != "" {
		t.Fatalf("verbose store failure generate = (%d, %q, %q)", code, out, errb)
	}
	if !strings.Contains(out, "cache store write failed") || !strings.Contains(out, pkgDir) {
		t.Fatalf("verbose store failure output = %q", out)
	}
}

func TestRunGenerateBuiltinFullMinifyUsesCache(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping module-resolution test in -short mode")
	}
	previousMinify, hadMinify := os.LookupEnv("GSX_MINIFY")
	if err := os.Unsetenv("GSX_MINIFY"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		var err error
		if hadMinify {
			err = os.Setenv("GSX_MINIFY", previousMinify)
		} else {
			err = os.Unsetenv("GSX_MINIFY")
		}
		if err != nil {
			t.Error(err)
		}
	})
	mod := newModule(t, "gsxrunbuiltinfullminify")
	pkgDir := filepath.Join(mod, "views")
	writeFile(t, pkgDir, "page.gsx", `package views

component Page() {
	<style>
		.card { color: #ffffff; }
	</style>
	<script>
		const answer = 1 + 2;
	</script>
}
`)
	writeFile(t, mod, "gsx.toml", "[minify]\ncss = \"full\"\njs = \"full\"\n")
	t.Setenv("GSXCACHE", t.TempDir())

	args := []string{"-C", mod, "generate", "-v", "./views"}
	code, out, errb := runCapture(t, args)
	if code != 0 {
		t.Fatalf("cold generate exit = %d, stderr=%q", code, errb)
	}
	if !strings.Contains(out, "0 hit, 1 miss, 0 uncacheable") {
		t.Fatalf("cold generate cache report = %q", out)
	}
	cold, err := os.ReadFile(filepath.Join(pkgDir, "page.x.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(cold), "#fff") || strings.Contains(string(cold), "#ffffff") || !strings.Contains(string(cold), "const answer=1+2") {
		t.Fatalf("built-in full minification did not minify generated output:\n%s", cold)
	}

	code, out, errb = runCapture(t, args)
	if code != 0 {
		t.Fatalf("warm generate exit = %d, stderr=%q", code, errb)
	}
	if !strings.Contains(out, "1 hit, 0 miss, 0 uncacheable") || strings.Contains(out, "disabled-by-option") {
		t.Fatalf("warm built-in full cache report = %q", out)
	}
	warm, err := os.ReadFile(filepath.Join(pkgDir, "page.x.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(cold, warm) {
		t.Fatal("warm cache hit changed the fully minified generated output")
	}
}

func TestRunGenerateCustomMinifierBypassesCache(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping module-resolution test in -short mode")
	}
	mod := newModule(t, "gsxruncustomminify")
	pkgDir := filepath.Join(mod, "views")
	writeFile(t, pkgDir, "page.gsx", `package views

component Page() {
	<style>.card { color: red; }</style>
}
`)
	t.Setenv("GSXCACHE", t.TempDir())

	called := 0
	custom := func(string) (string, error) {
		called++
		return ".custom{color:red}", nil
	}
	var cfg config
	WithCSSMinifier(custom)(&cfg)
	WithMinifyLevel(MinifyFull, MinifyFull)(&cfg)
	args := []string{"-C", mod, "generate", "-v", "./views"}
	generate := func() (int, string, string) {
		var out, errb bytes.Buffer
		code := runConfig(args, &out, &errb, cfg)
		return code, out.String(), errb.String()
	}

	for run := 1; run <= 2; run++ {
		code, out, errb := generate()
		if code != 0 {
			t.Fatalf("run %d exit = %d, stderr=%q", run, code, errb)
		}
		if !strings.Contains(out, "0 hit, 0 miss, 1 uncacheable") || !strings.Contains(out, "disabled-by-option") {
			t.Fatalf("run %d custom-minifier cache report = %q", run, out)
		}
	}
	if called != 2 {
		t.Fatalf("custom minifier calls = %d, want 2 semantic generations", called)
	}
	generated, err := os.ReadFile(filepath.Join(pkgDir, "page.x.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(generated), ".custom{color:red}") {
		t.Fatalf("custom minifier output was not generated:\n%s", generated)
	}
}

// TestRunGenerateMissingPath proves a non-existent path is a USAGE error (exit 2).
func TestRunGenerateMissingPath(t *testing.T) {
	t.Parallel()
	code, _, errb := runCapture(t, []string{"generate", "/does/not/exist/anywhere"})
	if code != 2 {
		t.Fatalf("expected exit 2 for missing path, got %d; stderr=%q", code, errb)
	}
}

// TestRunGenerateCodegenError proves a .gsx that fails codegen is a CODEGEN error
// (exit 1) and stderr names the dir.
func TestRunGenerateCodegenError(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping module-resolution test in -short mode")
	}
	mod := newModule(t, "gsxrunbad")
	badDir := filepath.Join(mod, "bad")
	writeFile(t, badDir, "bad.gsx", "package bad\n\ncomponent Bad() {\n\t<p>{undefinedSymbol}</p>\n}\n")

	code, _, errb := runCapture(t, []string{"generate", badDir})
	if code != 1 {
		t.Fatalf("expected exit 1 for codegen error, got %d; stderr=%q", code, errb)
	}
	if !strings.Contains(errb, badDir) {
		t.Fatalf("expected stderr to name the bad dir %q, got %q", badDir, errb)
	}
}

// TestRunVersion proves version prints something non-empty and returns 0.
func TestRunVersion(t *testing.T) {
	t.Parallel()
	code, out, errb := runCapture(t, []string{"version"})
	if code != 0 {
		t.Fatalf("expected exit 0, got %d; stderr=%q", code, errb)
	}
	if strings.TrimSpace(out) == "" {
		t.Fatalf("expected non-empty version stdout, got %q", out)
	}
}

// TestRunGenerateVerboseAfterCommand proves -v works AFTER the command and its
// path argument (the flag-position fix): `gsx generate <dir> -v` lists the file
// rather than erroring with exit 2.
func TestRunGenerateVerboseAfterCommand(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping module-resolution test in -short mode")
	}
	mod := newModule(t, "gsxrungenvafter")
	pkgDir := filepath.Join(mod, "views")
	writeFile(t, pkgDir, "hi.gsx", hiComponent)

	code, out, errb := runCapture(t, []string{"generate", pkgDir, "-v"})
	if code != 0 {
		t.Fatalf("expected exit 0 for `generate <dir> -v`, got %d; stderr=%q", code, errb)
	}
	target := filepath.Join(pkgDir, "hi.x.go")
	if !strings.Contains(out, target) {
		t.Fatalf("expected verbose stdout to list %q, got %q", target, out)
	}
}

// TestRunGenerateQuietAfterCommand proves -q works AFTER the command.
func TestRunGenerateQuietAfterCommand(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping module-resolution test in -short mode")
	}
	mod := newModule(t, "gsxrungenqafter")
	pkgDir := filepath.Join(mod, "views")
	writeFile(t, pkgDir, "hi.gsx", hiComponent)

	code, out, errb := runCapture(t, []string{"generate", pkgDir, "-q"})
	if code != 0 {
		t.Fatalf("expected exit 0 for `generate <dir> -q`, got %d; stderr=%q", code, errb)
	}
	if out != "" {
		t.Fatalf("expected empty stdout with trailing -q, got %q", out)
	}
}

// TestFormatBuildVersion proves the version banner includes the module version,
// a shortened VCS revision with time + dirty marker, and the Go toolchain
// version, and that it omits the commit line when no VCS info is present.
func TestFormatBuildVersion(t *testing.T) {
	t.Parallel()
	full := &debug.BuildInfo{
		GoVersion: "go1.24.0",
		Settings: []debug.BuildSetting{
			{Key: "vcs.revision", Value: "0123456789abcdef0123456789abcdef01234567"},
			{Key: "vcs.time", Value: "2026-06-23T21:14:05Z"},
			{Key: "vcs.modified", Value: "true"},
		},
	}
	full.Main.Version = "(devel)"
	got := formatBuildVersion(full)
	for _, want := range []string{"gsx (devel)", "commit: 0123456789ab", "2026-06-23T21:14:05Z", "dirty", "go:     go1.24.0"} {
		if !strings.Contains(got, want) {
			t.Errorf("version banner missing %q; got:\n%s", want, got)
		}
	}
	if strings.Contains(got, "0123456789abcdef") {
		t.Errorf("revision should be shortened to 12 chars, got:\n%s", got)
	}

	bare := &debug.BuildInfo{GoVersion: "go1.24.0"}
	bare.Main.Version = "v1.2.3"
	got = formatBuildVersion(bare)
	if !strings.Contains(got, "gsx v1.2.3") {
		t.Errorf("expected tagged version, got:\n%s", got)
	}
	if strings.Contains(got, "commit:") {
		t.Errorf("expected no commit line without VCS info, got:\n%s", got)
	}
}

// TestRunHelp proves help/no-args list the generate command and return 0.
func TestRunHelp(t *testing.T) {
	t.Parallel()
	for _, args := range [][]string{{"help"}, nil, {"-h"}} {
		code, out, errb := runCapture(t, args)
		if code != 0 {
			t.Fatalf("args=%v: expected exit 0, got %d; stderr=%q", args, code, errb)
		}
		if !strings.Contains(out, "generate") {
			t.Fatalf("args=%v: expected usage to list generate, got %q", args, out)
		}
	}
}

// TestRunUnknownCommand proves an unknown command is a usage error (exit 2) and
// stderr mentions unknown.
func TestRunUnknownCommand(t *testing.T) {
	t.Parallel()
	code, _, errb := runCapture(t, []string{"bogus"})
	if code != 2 {
		t.Fatalf("expected exit 2 for unknown command, got %d; stderr=%q", code, errb)
	}
	if !strings.Contains(errb, "unknown") {
		t.Fatalf("expected stderr to mention unknown, got %q", errb)
	}
}

// TestRunFmtDispatch proves the `fmt` command is wired into run: dispatching
// `fmt` over an empty directory (via -C) is a recognized command that succeeds
// (exit 0) rather than the unknown-command exit 2.
func TestRunFmtDispatch(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	code, _, errb := runCapture(t, []string{"-C", dir, "fmt"})
	if code != 0 {
		t.Fatalf("expected exit 0 for fmt over empty dir, got %d; stderr=%q", code, errb)
	}
}

// TestCleanCache proves `clean --cache` removes the cache dir when GSXCACHE is
// a real directory that has the CACHEDIR.TAG sentinel, and that `clean` without
// --cache does nothing destructive.
func TestCleanCache(t *testing.T) {
	cacheRoot := t.TempDir()
	t.Setenv("GSXCACHE", cacheRoot)

	// Write the sentinel so the guard passes.
	writeSentinel(cacheRoot)

	code, out, errb := runCapture(t, []string{"clean", "--cache"})
	if code != 0 {
		t.Fatalf("expected exit 0, got %d; stderr=%q", code, errb)
	}
	if !strings.Contains(out, "removed gsx cache") {
		t.Fatalf("expected stdout to mention removed gsx cache, got %q", out)
	}
	// The cache dir itself must be gone (RemoveAll removes the root too).
	if _, err := os.Stat(cacheRoot); !os.IsNotExist(err) {
		t.Fatalf("expected cache dir to be removed, but stat returned: %v", err)
	}
}

// TestCleanCacheSentinelGuard proves that clean --cache REFUSES (exit 1, dir
// not removed) when the GSXCACHE dir lacks the CACHEDIR.TAG sentinel.
// This guards against GSXCACHE=$HOME accidentally deleting $HOME.
func TestCleanCacheSentinelGuard(t *testing.T) {
	cacheRoot := t.TempDir()
	t.Setenv("GSXCACHE", cacheRoot)

	// Write a file to prove the dir is NOT removed.
	entryFile := filepath.Join(cacheRoot, "dummy-entry")
	if err := os.WriteFile(entryFile, []byte("data"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	// No CACHEDIR.TAG written — sentinel is absent.

	code, _, errb := runCapture(t, []string{"clean", "--cache"})
	if code == 0 {
		t.Fatalf("expected non-zero exit when sentinel absent, got 0")
	}
	if !strings.Contains(errb, "CACHEDIR.TAG") {
		t.Errorf("expected stderr to mention CACHEDIR.TAG, got %q", errb)
	}
	// Dir must still exist.
	if _, err := os.Stat(cacheRoot); err != nil {
		t.Fatalf("dir must NOT be removed when sentinel absent: %v", err)
	}
}

// TestCleanCacheSentinelWrittenByStorePut proves that a normal generate/storePut
// lifecycle writes the CACHEDIR.TAG sentinel so clean --cache works afterward.
func TestCleanCacheSentinelWrittenByStorePut(t *testing.T) {
	t.Parallel()
	cacheRoot := t.TempDir()
	out := pkgOutput{"a.x.go": []byte("package a\n")}
	if err := storePut(cacheRoot, "testkey", out); err != nil {
		t.Fatal(err)
	}
	tag := filepath.Join(cacheRoot, "CACHEDIR.TAG")
	data, err := os.ReadFile(tag)
	if err != nil {
		t.Fatalf("CACHEDIR.TAG must exist after storePut: %v", err)
	}
	if !strings.Contains(string(data), "8a477f597d28d172789f06886806bc55") {
		t.Errorf("CACHEDIR.TAG missing expected signature, got %q", string(data))
	}
}

// TestCleanCacheDisabled proves `clean --cache` when GSXCACHE=off prints a
// clear message and exits 0 without removing anything.
func TestCleanCacheDisabled(t *testing.T) {
	t.Setenv("GSXCACHE", "off")

	code, out, errb := runCapture(t, []string{"clean", "--cache"})
	if code != 0 {
		t.Fatalf("expected exit 0, got %d; stderr=%q", code, errb)
	}
	if !strings.Contains(out, "cache") {
		t.Fatalf("expected stdout to mention cache, got %q", out)
	}
}

// TestCleanNoFlags proves `clean` without --cache prints usage and exits 0.
func TestCleanNoFlags(t *testing.T) {
	t.Parallel()
	code, out, errb := runCapture(t, []string{"clean"})
	if code != 0 {
		t.Fatalf("expected exit 0, got %d; stderr=%q", code, errb)
	}
	if !strings.Contains(out, "cache") {
		t.Fatalf("expected stdout to mention cache, got %q", out)
	}
	_ = errb
}

// TestRunChdir proves -C runs relative to the given directory: a relative path
// "views" resolves under the -C dir.
func TestRunChdir(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping module-resolution test in -short mode")
	}
	mod := newModule(t, "gsxrunchdir")
	pkgDir := filepath.Join(mod, "views")
	writeFile(t, pkgDir, "hi.gsx", hiComponent)

	code, _, errb := runCapture(t, []string{"-C", mod, "generate", "views"})
	if code != 0 {
		t.Fatalf("expected exit 0, got %d; stderr=%q", code, errb)
	}
	if _, err := os.Stat(filepath.Join(pkgDir, "hi.x.go")); err != nil {
		t.Fatalf("expected hi.x.go written under -C dir: %v", err)
	}
}
