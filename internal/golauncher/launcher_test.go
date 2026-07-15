package golauncher

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestRunRejectsInPlaceLauncherMutation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell launcher probe is Unix-only")
	}
	realGo, err := exec.LookPath("go")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	wrapper := filepath.Join(dir, "go")
	const source = `#!/bin/sh
if [ "$1" = "env" ] && [ "$2" = "GOVERSION" ]; then
	printf '#!/bin/sh\nexec "$REAL_GO" "$@"\n' > "$0"
	printf 'go1.25\n'
	exit 0
fi
exec "$REAL_GO" "$@"
`
	if err := os.WriteFile(wrapper, []byte(source), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("REAL_GO", realGo)
	t.Setenv("PATH", dir)

	snapshot, err := SnapshotLive()
	if err != nil {
		t.Fatal(err)
	}
	launcher, err := snapshot.Seal(t.TempDir(), os.Environ())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := launcher.Run(t.TempDir(), os.Environ(), "env", "GOVERSION"); err == nil || !strings.Contains(err.Error(), "changed while running") {
		t.Fatalf("Run error = %v, want in-place launcher mutation rejection", err)
	}
}

func TestValidateRejectsCompilerIdentityDrift(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell launcher probe is Unix-only")
	}
	realGo, err := exec.LookPath("go")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	wrapper := filepath.Join(dir, "go")
	const source = `#!/bin/sh
if [ "$1" = "tool" ] && [ "$2" = "compile" ] && [ "$3" = "-V=full" ]; then
	printf '%s\n' "$FAKE_COMPILER_ID"
	exit 0
fi
exec "$REAL_GO" "$@"
`
	if err := os.WriteFile(wrapper, []byte(source), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("REAL_GO", realGo)
	t.Setenv("PATH", dir)
	t.Setenv("FAKE_COMPILER_ID", "compile version one")

	snapshot, err := SnapshotLive()
	if err != nil {
		t.Fatal(err)
	}
	launcher, err := snapshot.Seal(t.TempDir(), os.Environ())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_COMPILER_ID", "compile version two")
	if err := launcher.Validate(t.TempDir(), os.Environ()); err == nil || !strings.Contains(err.Error(), "compiler identity changed") {
		t.Fatalf("Validate error = %v, want compiler identity rejection", err)
	}
}

func TestRunKeepsSuccessfulStderrOutOfStdout(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell launcher probe is Unix-only")
	}
	realGo, err := exec.LookPath("go")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	wrapper := filepath.Join(dir, "go")
	const source = `#!/bin/sh
if [ "$1" = "env" ] && [ "$2" = "GOVERSION" ]; then
	printf 'successful warning\n' >&2
	printf 'go1.26.1\n'
	exit 0
fi
exec "$REAL_GO" "$@"
`
	if err := os.WriteFile(wrapper, []byte(source), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("REAL_GO", realGo)
	t.Setenv("PATH", dir)

	snapshot, err := SnapshotLive()
	if err != nil {
		t.Fatal(err)
	}
	launcher, err := snapshot.Seal(t.TempDir(), os.Environ())
	if err != nil {
		t.Fatal(err)
	}
	output, err := launcher.Run(t.TempDir(), os.Environ(), "env", "GOVERSION")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(output), "go1.26.1\n"; got != want {
		t.Fatalf("Run stdout = %q, want %q without successful stderr", got, want)
	}
}
