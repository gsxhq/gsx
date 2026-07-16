package golauncher

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestParseCompilerPathUsesOpaqueSingleLine(t *testing.T) {
	want := filepath.Join(string(filepath.Separator), "compiler dir 'single' \"double\"", "compile")
	got, err := parseCompilerPath([]byte(want + "\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got != filepath.Clean(want) {
		t.Fatalf("parseCompilerPath = %q, want opaque path %q", got, filepath.Clean(want))
	}
	for _, output := range []string{"", "relative/compiler\n", want, want + "\nextra\n"} {
		if _, err := parseCompilerPath([]byte(output)); err == nil {
			t.Fatalf("parseCompilerPath(%q) succeeded, want fail-closed rejection", output)
		}
	}
}

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

func TestValidateRejectsCompilerMutation(t *testing.T) {
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
if [ "$1" = "tool" ] && [ "$2" = "-n" ] && [ "$3" = "compile" ]; then
	printf '%s\n' "$FAKE_COMPILER"
	exit 0
fi
exec "$REAL_GO" "$@"
`
	if err := os.WriteFile(wrapper, []byte(source), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("REAL_GO", realGo)
	t.Setenv("PATH", dir)
	compiler := filepath.Join(t.TempDir(), "compile")
	if err := os.WriteFile(compiler, []byte("compiler version one"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_COMPILER", compiler)

	snapshot, err := SnapshotLive()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	env := os.Environ()
	launcher, err := snapshot.Seal(root, env)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(compiler, []byte("compiler version two"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := launcher.Validate(root, env); err == nil || !strings.Contains(err.Error(), "compiler") || !strings.Contains(err.Error(), "content changed") {
		t.Fatalf("Validate error = %v, want in-place compiler content rejection", err)
	}
}

func TestValidateRejectsCompilerReplacement(t *testing.T) {
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
if [ "$1" = "tool" ] && [ "$2" = "-n" ] && [ "$3" = "compile" ]; then
	printf '%s\n' "$FAKE_COMPILER"
	exit 0
fi
exec "$REAL_GO" "$@"
`
	if err := os.WriteFile(wrapper, []byte(source), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("REAL_GO", realGo)
	t.Setenv("PATH", dir)
	compiler := filepath.Join(t.TempDir(), "compile")
	if err := os.WriteFile(compiler, []byte("compiler bytes"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_COMPILER", compiler)

	snapshot, err := SnapshotLive()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	env := os.Environ()
	launcher, err := snapshot.Seal(root, env)
	if err != nil {
		t.Fatal(err)
	}
	replacement := filepath.Join(t.TempDir(), "replacement")
	if err := os.WriteFile(replacement, []byte("compiler bytes"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacement, compiler); err != nil {
		t.Fatal(err)
	}
	if err := launcher.Validate(root, env); err == nil || !strings.Contains(err.Error(), "compiler identity changed") {
		t.Fatalf("Validate error = %v, want compiler replacement rejection", err)
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
