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
	"strings"
)

// Snapshot is the selected Go launcher's immutable file identity before its
// compiler tool has been observed under the final command environment.
type Snapshot struct {
	path   string
	info   os.FileInfo
	digest [sha256.Size]byte
}

// Launcher is a sealed Snapshot plus the compiler tool identity observed under
// the exact environment used for Go metadata queries and package loading.
type Launcher struct {
	snapshot         Snapshot
	compilerIdentity string
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

// Seal records the exact compiler selected by the final environment. The
// compiler query itself is guarded by before-and-after launcher validation.
func (snapshot *Snapshot) Seal(dir string, env []string) (*Launcher, error) {
	if snapshot == nil {
		return nil, fmt.Errorf("nil Go launcher snapshot")
	}
	output, err := snapshot.Run(dir, env, "tool", "compile", "-V=full")
	if err != nil {
		return nil, fmt.Errorf("identify Go compiler: %w", err)
	}
	identity := strings.TrimSpace(string(output))
	if identity == "" {
		return nil, fmt.Errorf("identify Go compiler: empty identity")
	}
	return &Launcher{snapshot: *snapshot, compilerIdentity: identity}, nil
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

// CompilerIdentity returns the compiler identity captured when the launcher was
// sealed. Call Validate before using it as current provenance.
func (launcher *Launcher) CompilerIdentity() string {
	if launcher == nil {
		return ""
	}
	return launcher.compilerIdentity
}

// Run executes the sealed launcher with content validation before and after.
func (launcher *Launcher) Run(dir string, env []string, args ...string) ([]byte, error) {
	if launcher == nil {
		return nil, fmt.Errorf("nil sealed Go launcher")
	}
	return launcher.snapshot.Run(dir, env, args...)
}

// Validate proves that the live Go launcher still has the captured bytes and
// resolves the same compiler tool under env.
func (launcher *Launcher) Validate(dir string, env []string) error {
	if launcher == nil {
		return fmt.Errorf("nil sealed Go launcher")
	}
	output, err := launcher.snapshot.Run(dir, env, "tool", "compile", "-V=full")
	if err != nil {
		return err
	}
	identity := strings.TrimSpace(string(output))
	if identity != launcher.compilerIdentity {
		return fmt.Errorf("Go compiler identity changed from %q to %q", launcher.compilerIdentity, identity)
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
