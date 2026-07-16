// Package golauncher captures and validates the Go launcher used across a
// semantic operation. It detects path changes, replacement files, in-place
// mutations, and compiler-tool drift without relying on inode identity alone.
package golauncher

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
)

// Snapshot is the selected Go launcher's immutable file identity before its
// compiler tool has been observed under the final command environment.
type Snapshot struct {
	path   string
	info   os.FileInfo
	digest [sha256.Size]byte
}

// Launcher is a sealed Snapshot plus the exact compiler tool selected under
// the environment used for Go metadata queries and package loading.
type Launcher struct {
	snapshot Snapshot
	env      []string
	compiler compilerSnapshot
}

type compilerSnapshot struct {
	path   string
	info   os.FileInfo
	digest [sha256.Size]byte
}

// SnapshotLive resolves the process's selected Go command and records both its
// filesystem identity and content digest.
func SnapshotLive() (*Snapshot, error) {
	path, err := exec.LookPath("go")
	if err != nil {
		return nil, fmt.Errorf("locate Go command: %w", err)
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve Go command path: %w", err)
	}
	info, digest, err := inspect(path)
	if err != nil {
		return nil, fmt.Errorf("inspect Go command %q: %w", path, err)
	}
	return &Snapshot{path: path, info: info, digest: digest}, nil
}

// Path returns the absolute launcher path captured by SnapshotLive.
func (snapshot *Snapshot) Path() string {
	if snapshot == nil {
		return ""
	}
	return snapshot.path
}

// Seal records the exact compiler selected by the final environment. `go tool
// -n compile` is the Go command's authoritative selected executable path; no
// arguments follow the tool name, so its single output line is the opaque path
// even when that path contains spaces or quotes. The query itself is guarded
// by before-and-after launcher validation.
func (snapshot *Snapshot) Seal(dir string, env []string) (*Launcher, error) {
	if snapshot == nil {
		return nil, fmt.Errorf("nil Go launcher snapshot")
	}
	output, err := snapshot.Run(dir, env, "tool", "-n", "compile")
	if err != nil {
		return nil, fmt.Errorf("locate Go compiler: %w", err)
	}
	compilerPath, err := parseCompilerPath(output)
	if err != nil {
		return nil, err
	}
	info, digest, err := inspect(compilerPath)
	if err != nil {
		return nil, fmt.Errorf("inspect selected Go compiler %q: %w", compilerPath, err)
	}
	return snapshot.sealedLauncher(env, compilerPath, info, digest), nil
}

// SealToolchain seals the compiler selected by cmd/go's builtin-tool contract
// without starting a third Go process. `go env GOTOOLDIR` is cmd/go's
// build.ToolDir, and builtin ToolPath appends the installed-host executable
// suffix. If that shipped path cannot be inspected, Seal falls back to `go
// tool -n compile`, preserving cmd/go's dynamic builtin-tool resolution.
func (snapshot *Snapshot) SealToolchain(dir string, env []string, toolDir, hostOS string) (*Launcher, error) {
	if snapshot == nil {
		return nil, fmt.Errorf("nil Go launcher snapshot")
	}
	if !filepath.IsAbs(toolDir) {
		return nil, fmt.Errorf("Go tool directory is not absolute: %q", toolDir)
	}
	suffix, err := toolExecutableSuffix(hostOS)
	if err != nil {
		return nil, err
	}
	compilerPath := filepath.Join(toolDir, "compile"+suffix)
	if err := snapshot.validateLive(); err != nil {
		return nil, err
	}
	info, digest, err := inspect(compilerPath)
	if err != nil {
		return snapshot.Seal(dir, env)
	}
	if err := snapshot.validateLive(); err != nil {
		return nil, fmt.Errorf("Go launcher changed while selecting compiler: %w", err)
	}
	return snapshot.sealedLauncher(env, compilerPath, info, digest), nil
}

// RequireLocalToolchain proves that this PATH-selected Go command is the exact
// executable installed at the effective GOROOT/bin/go path. Callers can then
// freeze GOTOOLCHAIN=local without changing which command handles subsequent
// semantic operations.
func (snapshot *Snapshot) RequireLocalToolchain(goRoot, hostOS string) error {
	if snapshot == nil {
		return fmt.Errorf("nil Go launcher snapshot")
	}
	if !filepath.IsAbs(goRoot) {
		return fmt.Errorf("Go root is not absolute: %q", goRoot)
	}
	suffix, err := toolExecutableSuffix(hostOS)
	if err != nil {
		return err
	}
	toolchainPath := filepath.Join(goRoot, "bin", "go"+suffix)
	info, digest, err := inspect(toolchainPath)
	if err != nil {
		return fmt.Errorf("inspect effective Go toolchain command %q: %w", toolchainPath, err)
	}
	if snapshot.info == nil || !os.SameFile(snapshot.info, info) || snapshot.digest != digest {
		return fmt.Errorf("effective Go toolchain command %q is not the captured PATH-local command %q", toolchainPath, snapshot.path)
	}
	return snapshot.validateLive()
}

func toolExecutableSuffix(hostOS string) (string, error) {
	switch hostOS {
	case "windows":
		return ".exe", nil
	case "":
		return "", fmt.Errorf("Go host OS is empty")
	default:
		return "", nil
	}
}

func (snapshot *Snapshot) sealedLauncher(env []string, compilerPath string, info os.FileInfo, digest [sha256.Size]byte) *Launcher {
	return &Launcher{
		snapshot: *snapshot,
		env:      append([]string(nil), env...),
		compiler: compilerSnapshot{path: compilerPath, info: info, digest: digest},
	}
}

func parseCompilerPath(output []byte) (string, error) {
	if !bytes.HasSuffix(output, []byte("\n")) {
		return "", fmt.Errorf("locate Go compiler: go tool -n compile returned no terminating newline")
	}
	path := string(output[:len(output)-1])
	if path == "" || strings.ContainsAny(path, "\r\n") {
		return "", fmt.Errorf("locate Go compiler: go tool -n compile returned invalid path %q", path)
	}
	if !filepath.IsAbs(path) {
		return "", fmt.Errorf("locate Go compiler: go tool -n compile returned non-absolute path %q", path)
	}
	return filepath.Clean(path), nil
}

// Run executes the captured absolute launcher only while its live PATH
// selection and bytes still match the snapshot. Validation on both sides of
// execution catches a launcher rewritten by the command itself.
func (snapshot *Snapshot) Run(dir string, env []string, args ...string) ([]byte, error) {
	if snapshot == nil {
		return nil, fmt.Errorf("nil Go launcher snapshot")
	}
	if err := snapshot.validateLive(); err != nil {
		return nil, err
	}
	command := exec.Command(snapshot.path, args...)
	command.Dir = dir
	command.Env = append([]string(nil), env...)
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	runErr := command.Run()
	if err := snapshot.validateLive(); err != nil {
		return nil, fmt.Errorf("Go launcher changed while running %s: %w", strings.Join(args, " "), err)
	}
	if runErr != nil {
		detail := stderr.String()
		if strings.TrimSpace(detail) == "" {
			detail = stdout.String()
		}
		return nil, fmt.Errorf("%s %s: %w: %s", snapshot.path, strings.Join(args, " "), runErr, strings.TrimSpace(detail))
	}
	return stdout.Bytes(), nil
}

// Path returns the sealed launcher's absolute captured path.
func (launcher *Launcher) Path() string {
	if launcher == nil {
		return ""
	}
	return launcher.snapshot.path
}

// Digest returns the captured launcher content digest for cache provenance.
func (launcher *Launcher) Digest() [sha256.Size]byte {
	if launcher == nil {
		return [sha256.Size]byte{}
	}
	return launcher.snapshot.digest
}

// CompilerIdentity returns the selected compiler path and content digest
// captured when the launcher was sealed. Call Validate before using it as
// current provenance.
func (launcher *Launcher) CompilerIdentity() string {
	if launcher == nil {
		return ""
	}
	return fmt.Sprintf("path=%s\x00sha256=%x", launcher.compiler.path, launcher.compiler.digest)
}

// Run executes the sealed launcher with content validation before and after.
func (launcher *Launcher) Run(dir string, env []string, args ...string) ([]byte, error) {
	if launcher == nil {
		return nil, fmt.Errorf("nil sealed Go launcher")
	}
	return launcher.snapshot.Run(dir, env, args...)
}

// Validate proves that the live Go launcher and exact selected compiler still
// have the captured filesystem identities and bytes. The selected compiler
// path cannot drift without the frozen environment or launcher changing: Seal
// obtains it from the Go command under that boundary and Validate rejects an
// environment mismatch before inspecting the path. The working directory is
// deliberately not part of builtin compiler selection; callers may seal before
// discovering the module directory used for later package loading.
func (launcher *Launcher) Validate(_ string, env []string) error {
	if launcher == nil {
		return fmt.Errorf("nil sealed Go launcher")
	}
	if !slices.Equal(env, launcher.env) {
		return fmt.Errorf("Go compiler selection environment changed after launcher seal")
	}
	type compilerInspection struct {
		info   os.FileInfo
		digest [sha256.Size]byte
		err    error
	}
	compilerResult := make(chan compilerInspection, 1)
	go func() {
		info, digest, err := inspect(launcher.compiler.path)
		compilerResult <- compilerInspection{info: info, digest: digest, err: err}
	}()
	launcherErr := launcher.snapshot.validateLive()
	compiler := <-compilerResult
	if launcherErr != nil {
		return launcherErr
	}
	if compiler.err != nil {
		return fmt.Errorf("inspect live Go compiler %q: %w", launcher.compiler.path, compiler.err)
	}
	if launcher.compiler.info == nil || !os.SameFile(launcher.compiler.info, compiler.info) {
		return fmt.Errorf("Go compiler identity changed at %q", launcher.compiler.path)
	}
	if compiler.digest != launcher.compiler.digest {
		return fmt.Errorf("Go compiler %q content changed", launcher.compiler.path)
	}
	return nil
}

func (snapshot *Snapshot) validateLive() error {
	livePath, err := exec.LookPath("go")
	if err != nil {
		return fmt.Errorf("live Go command no longer resolves: %w", err)
	}
	livePath, err = filepath.Abs(livePath)
	if err != nil {
		return fmt.Errorf("resolve live Go command path: %w", err)
	}
	info, digest, err := inspect(livePath)
	if err != nil {
		return fmt.Errorf("inspect live Go command %q: %w", livePath, err)
	}
	if snapshot.info == nil || !os.SameFile(snapshot.info, info) {
		return fmt.Errorf("live Go command no longer matches captured %q (now %q)", snapshot.path, livePath)
	}
	if digest != snapshot.digest {
		return fmt.Errorf("live Go command %q content changed", livePath)
	}
	return nil
}

func inspect(path string) (os.FileInfo, [sha256.Size]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, [sha256.Size]byte{}, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, [sha256.Size]byte{}, err
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return nil, [sha256.Size]byte{}, err
	}
	var digest [sha256.Size]byte
	copy(digest[:], hash.Sum(nil))
	return info, digest, nil
}
