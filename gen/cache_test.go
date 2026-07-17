package gen

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/gsxhq/gsx/internal/attrclass"
)

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
if [ -n "$GSX_CREATE_VENDOR_ON_SECOND_COMMAND_COUNTER" ]; then
	count=0
	if [ -f "$GSX_CREATE_VENDOR_ON_SECOND_COMMAND_COUNTER" ]; then
		read count < "$GSX_CREATE_VENDOR_ON_SECOND_COMMAND_COUNTER"
	fi
	count=$((count + 1))
	printf '%s' "$count" > "$GSX_CREATE_VENDOR_ON_SECOND_COMMAND_COUNTER"
	if [ "$count" -eq 2 ]; then
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

	// cold: both generate
	res, err := generateCached([]string{tmp}, nil, nil, nil, attrclass.Builtin(), true, nil, nil, nil, true, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Written) != 2 {
		t.Fatalf("cold: want 2 written, got %v", res.Written)
	}

	// warm no-op: nothing regenerated (Written empty — restores are skipped when on-disk matches)
	res, err = generateCached([]string{tmp}, nil, nil, nil, attrclass.Builtin(), true, nil, nil, nil, true, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Written) != 0 {
		t.Fatalf("warm no-op: want 0 written, got %v", res.Written)
	}

	// edit only v -> only v regenerates
	mkgsx("v", "package v\n\ncomponent A(name string) { <p>Hi {name}</p> }\n")
	res, err = generateCached([]string{tmp}, nil, nil, nil, attrclass.Builtin(), true, nil, nil, nil, true, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Written) != 1 || filepath.Base(filepath.Dir(res.Written[0])) != "v" {
		t.Fatalf("edit v: want only v written, got %v", res.Written)
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

	res, err := generateCached([]string{root}, nil, nil, nil, attrclass.Builtin(), true, nil, nil, nil, true, true, nil)
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
	counter := filepath.Join(t.TempDir(), "semantic-command-count")
	t.Setenv("GSX_CREATE_VENDOR_ON_SECOND_COMMAND_COUNTER", counter)
	t.Setenv("GSX_CREATE_VENDOR_DIR", filepath.Join(root, "vendor"))

	res, err := generateCached([]string{root}, nil, nil, nil, attrclass.Builtin(), true, nil, nil, nil, true, true, nil)
	if err == nil || !strings.Contains(err.Error(), "vendor directory state changed") {
		t.Fatalf("generate error = %v, want packages.Load vendor mutation rejection", err)
	}
	count, err := os.ReadFile(counter)
	if err != nil {
		t.Fatalf("semantic command counter: %v", err)
	}
	commandCount, err := strconv.Atoi(string(count))
	if err != nil || commandCount < 2 {
		t.Fatalf("semantic command count = %q, want graph followed by packages.Load", count)
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

	if _, err := generateCached([]string{root}, nil, nil, nil, attrclass.Builtin(), true, nil, nil, nil, true, true, nil); err != nil {
		t.Fatalf("populate cache: %v", err)
	}
	xgo := filepath.Join(dir, "view.x.go")
	if err := os.Remove(xgo); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(t.TempDir(), "mutated")
	t.Setenv("GSX_MUTATE_COMPILER_MARKER", marker)

	res, err := generateCached([]string{root}, nil, nil, nil, attrclass.Builtin(), true, nil, nil, nil, true, true, nil)
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

	if _, err := generateCached([]string{root}, nil, nil, nil, attrclass.Builtin(), true, nil, nil, nil, true, true, nil); err != nil {
		t.Fatalf("populate cache: %v", err)
	}
	xgo := filepath.Join(dir, "view.x.go")
	if err := os.Remove(xgo); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(t.TempDir(), "created-vendor")
	t.Setenv("GSX_CREATE_VENDOR_MARKER", marker)
	t.Setenv("GSX_CREATE_VENDOR_DIR", filepath.Join(root, "vendor"))

	res, err := generateCached([]string{root}, nil, nil, nil, attrclass.Builtin(), true, nil, nil, nil, true, true, nil)
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

	res, err := generateCached([]string{root}, nil, nil, nil, attrclass.Builtin(), true, nil, nil, nil, true, true, nil)
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
	res, err := generateCached([]string{tmp}, nil, nil, nil, attrclass.Builtin(), true, nil, nil, nil, true, true, nil)
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
	res, err = generateCached([]string{tmp}, nil, nil, nil, attrclass.Builtin(), false, nil, nil, nil, true, true, nil)
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
