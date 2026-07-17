package main

import (
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"
)

func TestProducerContextQueryMatchesPackageLoadConfiguration(t *testing.T) {
	t.Setenv("GOPACKAGESDRIVER", "off")
	t.Setenv("GOFLAGS", "-compiler=gccgo -tags=ambient_tag")
	t.Setenv("GOEXPERIMENT", "invalid_ambient_experiment")
	t.Setenv("GOAMD64", "v999")
	t.Setenv("GOARM64", "v999")
	t.Setenv("GO386", "invalid")

	request := targetRequest{
		compiler: runtime.Compiler, goos: runtime.GOOS, goarch: runtime.GOARCH,
		languageVersion: "go1.23", cgoEnabled: false, tags: "exact_bundle_tag",
	}
	config, err := newProducerConfig(request)
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(config.launcher.Path()) {
		t.Fatalf("go command %q is not absolute", config.launcher.Path())
	}
	moduleDir := t.TempDir()
	probeDir := filepath.Join(moduleDir, "probe")
	if err := os.MkdirAll(probeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(name, source string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(probeDir, name), []byte(source), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("selected.go", "//go:build exact_bundle_tag\n\npackage probe\nconst SelectedByExplicitTag = true\n")
	write("ambient.go", "//go:build ambient_tag || late_mutation\n\npackage probe\nconst SelectedByAmbientTag = true\n")
	write("compiler.go", "//go:build "+runtime.Compiler+"\n\npackage probe\nconst SelectedByCompiler = true\n")
	write("cgo.go", "//go:build cgo\n\npackage probe\nconst SelectedByCGO = true\n")
	write("nocgo.go", "//go:build !cgo\n\npackage probe\nconst SelectedWithoutCGO = true\n")
	if err := os.WriteFile(filepath.Join(moduleDir, "go.mod"), []byte("module example.com/producer-probe\n\ngo 1.26.1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	config.dir = moduleDir
	// Mutation after capture cannot alter either the query or the load.
	t.Setenv("GOFLAGS", "-compiler=unknown -tags=late_mutation")

	target, err := config.queryTarget(request.languageVersion)
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyObservedTarget(target, request); err != nil {
		t.Fatal(err)
	}
	if target.LanguageVersion != "go1.23" || target.ToolchainVersion == "" {
		t.Fatalf("language/toolchain versions = %q/%q, want separate recorded values", target.LanguageVersion, target.ToolchainVersion)
	}
	if !slices.Contains(target.BuildTags, "exact_bundle_tag") || slices.Contains(target.BuildTags, "ambient_tag") || slices.Contains(target.BuildTags, "late_mutation") {
		t.Fatalf("observed BuildTags = %v, want only explicit producer tags", target.BuildTags)
	}
	if len(target.ToolTags) == 0 || len(target.ReleaseTags) == 0 {
		t.Fatalf("observed ToolTags=%v ReleaseTags=%v, want authoritative context", target.ToolTags, target.ReleaseTags)
	}
	if target.CGOEnabled {
		t.Fatal("observed cgo enabled, want explicit CGO_ENABLED=0")
	}

	fset := token.NewFileSet()
	loaded, err := packages.Load(&packages.Config{
		Mode:       packages.NeedName | packages.NeedCompiledGoFiles | packages.NeedTypes | packages.NeedTypesSizes,
		Fset:       fset,
		Dir:        config.dir,
		Env:        append([]string(nil), config.env...),
		BuildFlags: append([]string(nil), config.buildFlags...),
	}, "./probe")
	if err != nil {
		t.Fatal(err)
	}
	if err := config.verifyGoCommand(); err != nil {
		t.Fatal(err)
	}
	if len(loaded) != 1 || loaded[0].TypesSizes == nil {
		t.Fatalf("packages.Load result = %#v, want one package with target sizes", loaded)
	}
	scope := loaded[0].Types.Scope()
	for _, name := range []string{"SelectedByExplicitTag", "SelectedByCompiler", "SelectedWithoutCGO"} {
		if scope.Lookup(name) == nil {
			t.Fatalf("shared producer config did not select %s; files=%v", name, loaded[0].CompiledGoFiles)
		}
	}
	for _, name := range []string{"SelectedByAmbientTag", "SelectedByCGO"} {
		if scope.Lookup(name) != nil {
			t.Fatalf("shared producer config unexpectedly selected %s; files=%v", name, loaded[0].CompiledGoFiles)
		}
	}
}

func TestProducerRejectsExternalPackagesDriver(t *testing.T) {
	t.Setenv("GOPACKAGESDRIVER", "/tmp/external-driver")
	_, err := newProducerConfig(targetRequest{compiler: "gc", goos: runtime.GOOS, goarch: runtime.GOARCH})
	if err == nil || !strings.Contains(err.Error(), "external GOPACKAGESDRIVER") {
		t.Fatalf("newProducerConfig error = %v, want external-driver rejection", err)
	}
}

func TestProducerRejectsUnsupportedCompilerBeforeTargetDiscovery(t *testing.T) {
	t.Setenv("GOPACKAGESDRIVER", "off")
	_, err := newProducerConfig(targetRequest{compiler: "gccgo", goos: runtime.GOOS, goarch: runtime.GOARCH})
	if err == nil || !strings.Contains(err.Error(), "only gc") {
		t.Fatalf("newProducerConfig error = %v, want gc-only rejection", err)
	}
}

func TestProducerRejectsPathDiscoveredPackagesDriver(t *testing.T) {
	dir := t.TempDir()
	driver := filepath.Join(dir, "gopackagesdriver")
	if err := os.WriteFile(driver, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GOPACKAGESDRIVER", "")
	t.Setenv("PATH", dir)
	_, err := newProducerConfig(targetRequest{compiler: "gc", goos: runtime.GOOS, goarch: runtime.GOARCH})
	if err == nil || !strings.Contains(err.Error(), "PATH-discovered gopackagesdriver") {
		t.Fatalf("newProducerConfig error = %v, want PATH-discovered driver rejection", err)
	}
}

func TestProducerRejectsInPlaceGoLauncherMutationDuringTargetQuery(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell launcher probe is Unix-only")
	}
	realGo, err := exec.LookPath("go")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	launcher := filepath.Join(dir, "go")
	const source = `#!/bin/sh
if [ "$1" = "env" ] && [ "$2" = "GOVERSION" ]; then
	printf '#!/bin/sh\nexec "$REAL_GO" "$@"\n' > "$0"
	printf 'go1.25\n'
	exit 0
fi
exec "$REAL_GO" "$@"
`
	if err := os.WriteFile(launcher, []byte(source), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("REAL_GO", realGo)
	t.Setenv("PATH", dir)
	t.Setenv("GOPACKAGESDRIVER", "off")
	request := targetRequest{
		compiler: runtime.Compiler, goos: runtime.GOOS, goarch: runtime.GOARCH,
		languageVersion: "go1.25", cgoEnabled: false,
	}
	config, err := newProducerConfig(request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := config.queryTarget(request.languageVersion); err == nil || !strings.Contains(err.Error(), "changed while running") {
		t.Fatalf("queryTarget error = %v, want in-place launcher mutation rejection", err)
	}
}

func TestParseContextTagsRejectsNonContextOutput(t *testing.T) {
	if _, err := parseContextTags("not-a-list"); err == nil {
		t.Fatal("parseContextTags accepted malformed context output")
	}
}
