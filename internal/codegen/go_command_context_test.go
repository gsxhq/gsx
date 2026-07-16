package codegen

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
)

func writeTracingGoCommand(t *testing.T, trace, compiler string) string {
	t.Helper()
	goRoot := t.TempDir()
	bin := filepath.Join(goRoot, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	command := filepath.Join(bin, "go")
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$GSX_GO_COMMAND_TRACE"
if [ "$1" = "env" ] && [ "$2" = "-changed" ]; then
	printf '{"GOFLAGS":"%s"}' "$GOFLAGS"
	exit 0
fi
if [ "$1" = "env" ] && [ "$2" = "-json" ] && [ "$3" = "GOWORK" ]; then
	printf '{"GOWORK":"%s","GOTOOLDIR":"%s","GOHOSTOS":"linux","GOROOT":"%s","GOVERSION":"%s","GOTOOLCHAIN":"go1.26.1+auto"}' "$GOWORK" "$GSX_FAKE_TOOL_DIR" "$GSX_FAKE_SELECTED_GOROOT" "$GSX_FAKE_SELECTED_VERSION"
	exit 0
fi
if [ "$1" = "env" ] && [ "$2" = "-json" ] && [ "$3" = "GOTOOLDIR" ]; then
	printf '{"GOTOOLDIR":"%s","GOHOSTOS":"linux","GOROOT":"%s","GOVERSION":"%s"}' "$GSX_FAKE_TOOL_DIR" "$GSX_FAKE_LOCAL_GOROOT" "$GSX_FAKE_LOCAL_VERSION"
	exit 0
fi
if [ "$1" = "env" ] && [ "$2" = "-json" ] && [ -z "$3" ]; then
	if [ -n "$GSX_MUTATE_COMPILER_DURING_ENV" ]; then
		printf 'compiler changed during command %s' "$$" > "$GSX_FAKE_COMPILER"
	fi
	printf '{"GOFLAGS":"%s","GOWORK":"%s","GOTOOLDIR":"%s","GOHOSTOS":"linux","GOROOT":"%s","GOVERSION":"%s","GOTOOLCHAIN":"go1.26.1+auto","GOENV":"/persisted/go/env","GOGCCFLAGS":"transient"}' "$GOFLAGS" "$GOWORK" "$GSX_FAKE_TOOL_DIR" "$GSX_FAKE_SELECTED_GOROOT" "$GSX_FAKE_SELECTED_VERSION"
	exit 0
fi
if [ "$1" = "tool" ] && [ "$2" = "-n" ] && [ "$3" = "compile" ]; then
	printf '%s\n' "$GSX_FAKE_COMPILER"
	exit 0
fi
exit 1
`
	if err := os.WriteFile(command, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	t.Setenv("GSX_GO_COMMAND_TRACE", trace)
	t.Setenv("GSX_FAKE_COMPILER", compiler)
	t.Setenv("GSX_FAKE_TOOL_DIR", filepath.Dir(compiler))
	t.Setenv("GSX_FAKE_SELECTED_GOROOT", goRoot)
	t.Setenv("GSX_FAKE_LOCAL_GOROOT", goRoot)
	t.Setenv("GSX_FAKE_SELECTED_VERSION", "go1.26.1")
	t.Setenv("GSX_FAKE_LOCAL_VERSION", "go1.26.1")
	t.Setenv("GOWORK", "off")
	t.Setenv("GOENV", "off")
	t.Setenv("GOFLAGS", "")
	t.Setenv("GOPACKAGESDRIVER", "off")
	return bin
}

func TestGoCommandContextCaptureCommandContract(t *testing.T) {
	root := t.TempDir()
	trace := filepath.Join(t.TempDir(), "go-command-trace")
	compiler := filepath.Join(t.TempDir(), "compiler dir 'quoted'", "compile")
	if err := os.MkdirAll(filepath.Dir(compiler), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(compiler, []byte("compiler bytes"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeTracingGoCommand(t, trace, compiler)

	context := CaptureGoCommandContext(root)
	if context.buildEnvErr != nil {
		t.Fatal(context.buildEnvErr)
	}
	commands, err := os.ReadFile(trace)
	if err != nil {
		t.Fatal(err)
	}
	gotCommands := strings.Split(strings.TrimSpace(string(commands)), "\n")
	sort.Strings(gotCommands)
	wantCommands := []string{
		"env -changed -json",
		"env -json",
		"env -json GOTOOLDIR GOHOSTOS GOROOT GOVERSION",
	}
	if !slices.Equal(gotCommands, wantCommands) {
		t.Fatalf("capture Go commands = %q, want exact bounded contract %q", gotCommands, wantCommands)
	}

	if err := os.WriteFile(trace, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := context.goLauncher.Validate(root, context.buildEnv); err != nil {
		t.Fatal(err)
	}
	commands, err = os.ReadFile(trace)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(commands)); got != "" {
		t.Fatalf("compiler validation launched Go subprocesses: %q", got)
	}
	if _, err := context.CacheFingerprint(); err != nil {
		t.Fatal(err)
	}
	commands, err = os.ReadFile(trace)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(commands)); got != "" {
		t.Fatalf("cache fingerprint Go commands = %q, want retained capture environment with no subprocess", got)
	}
}

func TestGoCommandContextRetainedEnvironmentMatchesFreshFrozenQuery(t *testing.T) {
	root := t.TempDir()
	t.Setenv("GOWORK", "off")
	t.Setenv("GOENV", "off")
	t.Setenv("GOFLAGS", "-tags=retained")
	t.Setenv("GOPACKAGESDRIVER", "off")
	context := CaptureGoCommandContext(root)
	if context.buildEnvErr != nil {
		t.Fatal(context.buildEnvErr)
	}
	freshJSON, err := context.Run("env", "-json")
	if err != nil {
		t.Fatal(err)
	}
	fresh := map[string]string{}
	if err := json.Unmarshal(freshJSON, &fresh); err != nil {
		t.Fatal(err)
	}
	freshCanonical, err := canonicalGoEnvironment(fresh, context.buildEnv)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(context.cacheEnv, freshCanonical) {
		t.Fatalf("retained canonical environment differs from fresh frozen query:\nretained: %s\nfresh:    %s", context.cacheEnv, freshCanonical)
	}
	canonical := map[string]string{}
	if err := json.Unmarshal(context.cacheEnv, &canonical); err != nil {
		t.Fatal(err)
	}
	if _, ok := canonical["GOGCCFLAGS"]; ok {
		t.Fatal("retained canonical environment contains transient GOGCCFLAGS")
	}
	if got := canonical["GOENV"]; got != "" {
		t.Fatalf("canonical GOENV = %q, want cmd/go's empty report for disabled GOENV", got)
	}
	if _, ok := canonical["GOPACKAGESDRIVER"]; ok {
		t.Fatal("canonical environment invented non-go-env GOPACKAGESDRIVER key")
	}
}

func TestGoCommandContextProcessEnvironmentPrecedesPersistedGoEnv(t *testing.T) {
	root := t.TempDir()
	goEnv := filepath.Join(t.TempDir(), "go.env")
	if err := os.WriteFile(goEnv, []byte("GOFLAGS=-tags=persisted\nGOPROXY=https://persisted.example\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GOENV", goEnv)
	t.Setenv("GOWORK", "off")
	t.Setenv("GOFLAGS", "-tags=process")
	t.Setenv("GOPROXY", "https://process.example")
	t.Setenv("GOPACKAGESDRIVER", "off")
	context := CaptureGoCommandContext(root)
	if context.buildEnvErr != nil {
		t.Fatal(context.buildEnvErr)
	}
	canonical := map[string]string{}
	if err := json.Unmarshal(context.cacheEnv, &canonical); err != nil {
		t.Fatal(err)
	}
	if got := canonical["GOFLAGS"]; got != "-tags=process" {
		t.Fatalf("canonical GOFLAGS = %q, want explicit process value", got)
	}
	if got := canonical["GOPROXY"]; got != "https://process.example" {
		t.Fatalf("canonical GOPROXY = %q, want explicit process value", got)
	}
}

func TestGoCommandContextRejectsUnsealedGoExecutables(t *testing.T) {
	for _, test := range []struct {
		name    string
		goFlags string
		want    string
	}{
		{name: "gccgo compiler", goFlags: "-compiler=gccgo", want: "only gc can be sealed"},
		{name: "tool exec wrapper", goFlags: "-toolexec=/tmp/go-tool-wrapper", want: "executable graph cannot be sealed"},
	} {
		t.Run(test.name, func(t *testing.T) {
			compiler := filepath.Join(t.TempDir(), "compile")
			if err := os.WriteFile(compiler, []byte("compiler bytes"), 0o755); err != nil {
				t.Fatal(err)
			}
			writeTracingGoCommand(t, filepath.Join(t.TempDir(), "trace"), compiler)
			t.Setenv("GOFLAGS", test.goFlags)
			context := CaptureGoCommandContext(t.TempDir())
			if err := context.ValidateCurrent(); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ValidateCurrent error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestGoCommandContextRejectsToolchainSwitch(t *testing.T) {
	compiler := filepath.Join(t.TempDir(), "compile")
	if err := os.WriteFile(compiler, []byte("compiler bytes"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeTracingGoCommand(t, filepath.Join(t.TempDir(), "trace"), compiler)
	t.Setenv("GSX_FAKE_SELECTED_VERSION", "go1.99.0")
	context := CaptureGoCommandContext(t.TempDir())
	if err := context.ValidateCurrent(); err == nil || !strings.Contains(err.Error(), "toolchain switching") || !strings.Contains(err.Error(), "PATH-local") {
		t.Fatalf("ValidateCurrent error = %v, want direct PATH-local toolchain diagnostic", err)
	}
}

func TestGoCommandContextRunRejectsCompilerMutationDuringCommand(t *testing.T) {
	compiler := filepath.Join(t.TempDir(), "compile")
	if err := os.WriteFile(compiler, []byte("compiler bytes"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeTracingGoCommand(t, filepath.Join(t.TempDir(), "trace"), compiler)
	t.Setenv("GSX_MUTATE_COMPILER_DURING_ENV", "1")
	context := CaptureGoCommandContext(t.TempDir())
	if _, err := context.Run("env", "-json"); err == nil || !strings.Contains(err.Error(), "changed while running env -json") || !strings.Contains(err.Error(), "compiler") {
		t.Fatalf("Run error = %v, want compiler mutation during command rejection", err)
	}
}

func writeFakeGoCommand(t *testing.T, marker string) string {
	t.Helper()
	goRoot := t.TempDir()
	bin := filepath.Join(goRoot, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	command := filepath.Join(bin, "go")
	script := `#!/bin/sh
if [ "$1" = "env" ] && [ "$2" = "-changed" ]; then
	printf '{"GOFLAGS":"%s"}' "$GOFLAGS"
	exit 0
fi
if [ "$1" = "env" ] && [ "$2" = "-json" ] && [ "$3" = "GOWORK" ]; then
	printf '{"GOWORK":"%s","GOTOOLDIR":"` + filepath.Dir(marker) + `","GOHOSTOS":"linux","GOROOT":"` + goRoot + `","GOVERSION":"go1.26.1","GOTOOLCHAIN":"go1.26.1+auto"}' "$GOWORK"
	exit 0
fi
if [ "$1" = "env" ] && [ "$2" = "-json" ] && [ "$3" = "GOTOOLDIR" ]; then
	printf '{"GOTOOLDIR":"` + filepath.Dir(marker) + `","GOHOSTOS":"linux","GOROOT":"` + goRoot + `","GOVERSION":"go1.26.1"}'
	exit 0
fi
if [ "$1" = "env" ] && [ "$2" = "-json" ] && [ -z "$3" ]; then
	printf '{"GOFLAGS":"%s","GOWORK":"%s","GOTOOLDIR":"` + filepath.Dir(marker) + `","GOHOSTOS":"linux","GOROOT":"` + goRoot + `","GOVERSION":"go1.26.1","GOTOOLCHAIN":"go1.26.1+auto","GOENV":"/persisted/go/env","GOPROXY":"%s","GOGCCFLAGS":"transient-%s"}' "$GOFLAGS" "$GOWORK" "$GSX_FAKE_GOPROXY" "$$"
	exit 0
fi
if [ "$1" = "tool" ] && [ "$2" = "-n" ] && [ "$3" = "compile" ]; then
	printf '%s\n' '` + marker + `'
	exit 0
fi
exit 1
`
	if err := os.WriteFile(command, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin
}

func TestGoCommandContextFingerprintIncludesSelectedCompilerIdentity(t *testing.T) {
	root := t.TempDir()
	marker := filepath.Join(t.TempDir(), "compile")
	if err := os.WriteFile(marker, []byte("compile version one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", writeFakeGoCommand(t, marker))
	t.Setenv("GOWORK", "off")
	t.Setenv("GOENV", "off")
	t.Setenv("GOFLAGS", "")
	t.Setenv("GOPACKAGESDRIVER", "off")
	t.Setenv("GSX_FAKE_GOPROXY", "https://proxy.example")

	first, err := CaptureGoCommandContext(root).CacheFingerprint()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(marker, []byte("compile version two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	second, err := CaptureGoCommandContext(root).CacheFingerprint()
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("compiler -V=full identity changed without changing the Go-context fingerprint")
	}
}

func TestGoCommandContextFingerprintRevalidatesMemoizedCompiler(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(t *testing.T, compiler string)
	}{
		{
			name: "in-place mutation",
			mutate: func(t *testing.T, compiler string) {
				t.Helper()
				if err := os.WriteFile(compiler, []byte("compiler version two"), 0o755); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "replacement",
			mutate: func(t *testing.T, compiler string) {
				t.Helper()
				replacement := filepath.Join(t.TempDir(), "replacement")
				if err := os.WriteFile(replacement, []byte("compiler version one"), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(replacement, compiler); err != nil {
					t.Fatal(err)
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			compiler := filepath.Join(t.TempDir(), "compile")
			if err := os.WriteFile(compiler, []byte("compiler version one"), 0o755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", writeFakeGoCommand(t, compiler))
			t.Setenv("GOWORK", "off")
			t.Setenv("GOENV", "off")
			t.Setenv("GOFLAGS", "")
			t.Setenv("GOPACKAGESDRIVER", "off")

			context := CaptureGoCommandContext(root)
			if _, err := context.CacheFingerprint(); err != nil {
				t.Fatal(err)
			}
			test.mutate(t, compiler)
			if _, err := context.CacheFingerprint(); err == nil || !strings.Contains(err.Error(), "compiler") {
				t.Fatalf("memoized CacheFingerprint error = %v, want compiler mutation rejection", err)
			}
		})
	}
}

func TestGoCommandContextFingerprintIsStableAndIncludesEffectiveSettings(t *testing.T) {
	root := t.TempDir()
	marker := filepath.Join(t.TempDir(), "compiler")
	if err := os.WriteFile(marker, []byte("compiler bytes"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", writeFakeGoCommand(t, marker))
	t.Setenv("GOWORK", "off")
	t.Setenv("GOENV", "off")
	t.Setenv("GOFLAGS", "")
	t.Setenv("GOPACKAGESDRIVER", "off")

	first, err := CaptureGoCommandContext(root).CacheFingerprint()
	if err != nil {
		t.Fatal(err)
	}
	second, err := CaptureGoCommandContext(root).CacheFingerprint()
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("identical frozen Go contexts produced unstable fingerprints:\n%s\n%s", first, second)
	}

	t.Setenv("GOFLAGS", "-tags=feature")
	changed, err := CaptureGoCommandContext(root).CacheFingerprint()
	if err != nil {
		t.Fatal(err)
	}
	if changed == first {
		t.Fatal("effective GOFLAGS changed without changing the Go-context fingerprint")
	}

	t.Setenv("GOFLAGS", "")
	t.Setenv("GSX_FAKE_GOPROXY", "https://other-proxy.example")
	defaultChanged, err := CaptureGoCommandContext(root).CacheFingerprint()
	if err != nil {
		t.Fatal(err)
	}
	if defaultChanged == first {
		t.Fatal("effective Go environment default changed without changing the Go-context fingerprint")
	}
}

func TestGoCommandContextFingerprintStableAcrossRealGoQueries(t *testing.T) {
	root := t.TempDir()
	t.Setenv("GOWORK", "off")
	t.Setenv("GOENV", "off")
	t.Setenv("GOFLAGS", "")
	t.Setenv("GOPACKAGESDRIVER", "off")

	first, err := CaptureGoCommandContext(root).CacheFingerprint()
	if err != nil {
		t.Fatal(err)
	}
	second, err := CaptureGoCommandContext(root).CacheFingerprint()
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("identical real Go contexts produced unstable fingerprints:\n%s\n%s", first, second)
	}
	context := CaptureGoCommandContext(root)
	if context.buildEnvErr != nil {
		t.Fatal(context.buildEnvErr)
	}
	if got := environmentValue(context.buildEnv, "GOTOOLCHAIN"); got != "local" {
		t.Fatalf("default auto-selected local toolchain was frozen as GOTOOLCHAIN=%q, want local", got)
	}
}

func BenchmarkGoCommandContextCaptureAndFingerprint(b *testing.B) {
	root := b.TempDir()
	b.Setenv("GOWORK", "off")
	b.Setenv("GOENV", "off")
	b.Setenv("GOFLAGS", "")
	b.Setenv("GOPACKAGESDRIVER", "off")
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		if _, err := CaptureGoCommandContext(root).CacheFingerprint(); err != nil {
			b.Fatal(err)
		}
	}
}

func TestGoCommandContextFingerprintIncludesPersistedGoEnvSetting(t *testing.T) {
	root := t.TempDir()
	goEnv := filepath.Join(t.TempDir(), "go.env")
	t.Setenv("GOENV", goEnv)
	t.Setenv("GOWORK", "off")
	t.Setenv("GOPACKAGESDRIVER", "off")
	unsetEnvironment(t, "GOFLAGS")
	if err := os.WriteFile(goEnv, []byte("GOFLAGS=-tags=one\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	firstContext := CaptureGoCommandContext(root)
	first, err := firstContext.CacheFingerprint()
	if err != nil {
		t.Fatal(err)
	}
	if got := environmentValue(firstContext.buildEnv, "GOFLAGS"); got != "-tags=one" {
		t.Fatalf("frozen GOFLAGS = %q, want persisted setting", got)
	}
	if got := environmentValue(firstContext.buildEnv, "GOENV"); got != "off" {
		t.Fatalf("frozen GOENV = %q, want off after settings snapshot", got)
	}

	if err := os.WriteFile(goEnv, []byte("GOFLAGS=-tags=two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	second, err := CaptureGoCommandContext(root).CacheFingerprint()
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("persisted go env setting changed without changing the Go-context fingerprint")
	}
}

func TestGoCommandContextResolvesWorkspaceFromModuleRoot(t *testing.T) {
	workspaceRoot := t.TempDir()
	moduleRoot := filepath.Join(workspaceRoot, "module")
	if err := os.MkdirAll(moduleRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(moduleRoot, "go.mod"), []byte("module example.com/app\n\ngo 1.26.1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	goWork := filepath.Join(workspaceRoot, "go.work")
	if err := os.WriteFile(goWork, []byte("go 1.26.1\n\nuse ./module\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	unsetEnvironment(t, "GOWORK")
	t.Setenv("GOENV", "off")
	t.Setenv("GOFLAGS", "")
	t.Setenv("GOPACKAGESDRIVER", "off")

	context := CaptureGoCommandContext(moduleRoot)
	if context.buildEnvErr != nil {
		t.Fatal(context.buildEnvErr)
	}
	wantGoWork, err := filepath.EvalSymlinks(goWork)
	if err != nil {
		t.Fatal(err)
	}
	if got := environmentValue(context.buildEnv, "GOWORK"); got != wantGoWork {
		t.Fatalf("frozen GOWORK = %q, want module-root resolution %q", got, wantGoWork)
	}
	if _, err := context.CacheFingerprint(); !errors.Is(err, ErrUncacheableGoContext) {
		t.Fatalf("CacheFingerprint error = %v, want active workspace uncacheable", err)
	}
}

func TestGoCommandContextDisablesPersistentCacheForWorkspaceAndVendor(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "compile")
	if err := os.WriteFile(marker, []byte("compile version\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", writeFakeGoCommand(t, marker))
	t.Setenv("GOENV", "off")
	t.Setenv("GOPACKAGESDRIVER", "off")

	t.Run("workspace", func(t *testing.T) {
		t.Setenv("GOWORK", filepath.Join(t.TempDir(), "go.work"))
		_, err := CaptureGoCommandContext(t.TempDir()).CacheFingerprint()
		if !errors.Is(err, ErrUncacheableGoContext) {
			t.Fatalf("CacheFingerprint error = %v, want ErrUncacheableGoContext", err)
		}
	})

	t.Run("vendor", func(t *testing.T) {
		t.Setenv("GOWORK", "off")
		t.Setenv("GOFLAGS", "")
		root := t.TempDir()
		if err := os.Mkdir(filepath.Join(root, "vendor"), 0o755); err != nil {
			t.Fatal(err)
		}
		_, err := CaptureGoCommandContext(root).CacheFingerprint()
		if !errors.Is(err, ErrUncacheableGoContext) {
			t.Fatalf("CacheFingerprint error = %v, want ErrUncacheableGoContext", err)
		}
	})

	t.Run("vendor appears after memoized fingerprint", func(t *testing.T) {
		t.Setenv("GOWORK", "off")
		t.Setenv("GOFLAGS", "-mod=mod")
		root := t.TempDir()
		context := CaptureGoCommandContext(root)
		if _, err := context.CacheFingerprint(); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(filepath.Join(root, "vendor"), 0o755); err != nil {
			t.Fatal(err)
		}
		if _, err := context.CacheFingerprint(); err == nil || !strings.Contains(err.Error(), "vendor directory state changed") {
			t.Fatalf("memoized CacheFingerprint error = %v, want vendor state rejection", err)
		}
	})

	t.Run("mod vendor", func(t *testing.T) {
		t.Setenv("GOWORK", "off")
		t.Setenv("GOFLAGS", "-mod=vendor")
		_, err := CaptureGoCommandContext(t.TempDir()).CacheFingerprint()
		if !errors.Is(err, ErrUncacheableGoContext) {
			t.Fatalf("CacheFingerprint error = %v, want ErrUncacheableGoContext", err)
		}
	})
}
