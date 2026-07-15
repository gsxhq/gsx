package codegen

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func writeFakeGoCommand(t *testing.T, marker string) string {
	t.Helper()
	bin := t.TempDir()
	command := filepath.Join(bin, "go")
	script := `#!/bin/sh
if [ "$1" = "env" ] && [ "$2" = "-changed" ]; then
	printf '{}'
	exit 0
fi
if [ "$1" = "env" ] && [ "$2" = "-json" ] && [ "$3" = "GOWORK" ]; then
	printf '{"GOWORK":"%s"}' "$GOWORK"
	exit 0
fi
if [ "$1" = "env" ] && [ "$2" = "-json" ]; then
	printf '{"GOARCH":"amd64","CGO_ENABLED":"0","GOFLAGS":"%s","GOOS":"linux","GOVERSION":"go1.26.1","GOWORK":"%s"}' "$GOFLAGS" "$GOWORK"
	exit 0
fi
if [ "$1" = "tool" ] && [ "$2" = "compile" ] && [ "$3" = "-V=full" ]; then
	/bin/cat '` + marker + `'
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
	marker := filepath.Join(t.TempDir(), "compiler-version")
	if err := os.WriteFile(marker, []byte("compile version one\n"), 0o644); err != nil {
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

func TestGoCommandContextDisablesPersistentCacheForWorkspaceAndVendor(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "compiler-version")
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

	t.Run("mod vendor", func(t *testing.T) {
		t.Setenv("GOWORK", "off")
		t.Setenv("GOFLAGS", "-mod=vendor")
		_, err := CaptureGoCommandContext(t.TempDir()).CacheFingerprint()
		if !errors.Is(err, ErrUncacheableGoContext) {
			t.Fatalf("CacheFingerprint error = %v, want ErrUncacheableGoContext", err)
		}
	})
}
