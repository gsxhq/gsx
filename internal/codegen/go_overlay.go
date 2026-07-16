package codegen

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/gsxhq/gsx/internal/golauncher"
)

type effectiveGoWorkspace struct {
	GOWORK    string
	GOTOOLDIR string
	GOHOSTOS  string
}

// ErrUncacheableGoContext marks a valid Go command context whose semantic
// inputs cannot be represented by the persistent generator cache. Analysis may
// still use the context; callers should bypass only the cache.
var ErrUncacheableGoContext = errors.New("codegen: Go command context is not persistently cacheable")

// GoCommandContext is one immutable snapshot of the Go command boundary used
// by both source selection and callers that must key work performed before a
// Module's first packages.Load. Its fields are deliberately private: consumers
// can run the captured command or obtain its canonical cache fingerprint, but
// cannot construct a partial environment.
type GoCommandContext struct {
	moduleRoot  string
	buildEnv    []string
	buildEnvErr error
	goLauncher  *golauncher.Launcher
	cacheOnce   sync.Once
	cacheKey    string
	cacheKeyErr error
}

// CaptureGoCommandContext freezes exactly the environment and Go launcher a
// normal Module will use. Capture errors are retained rather than returned so
// syntax-only Open callers remain usable; Run and CacheFingerprint surface the
// same error when semantic work is requested.
func CaptureGoCommandContext(moduleRoot string) *GoCommandContext {
	context := &GoCommandContext{
		moduleRoot: filepath.Clean(moduleRoot),
		buildEnv:   append([]string(nil), os.Environ()...),
	}
	packagesDriverPath, _ := exec.LookPath("gopackagesdriver")
	snapshot, err := golauncher.SnapshotLive()
	context.buildEnvErr = err
	var toolDir, hostOS string
	if context.buildEnvErr == nil {
		context.buildEnv, toolDir, hostOS, context.buildEnvErr = freezeGoCommandEnvironment(
			context.buildEnv,
			context.moduleRoot,
			packagesDriverPath,
			snapshot,
		)
	}
	if context.buildEnvErr == nil {
		context.goLauncher, context.buildEnvErr = snapshot.SealToolchain(context.moduleRoot, context.buildEnv, toolDir, hostOS)
	}
	return context
}

// Run executes the captured Go command under the captured environment. It is
// the only supported path for pre-analysis Go metadata queries that must agree
// with a Module created from this context.
func (context *GoCommandContext) Run(args ...string) ([]byte, error) {
	if context == nil {
		return nil, fmt.Errorf("codegen: nil Go command context")
	}
	if context.buildEnvErr != nil {
		return nil, context.buildEnvErr
	}
	if context.goLauncher == nil {
		return nil, fmt.Errorf("codegen: Go launcher identity is unavailable")
	}
	return context.goLauncher.Run(context.moduleRoot, context.buildEnv, args...)
}

// CacheFingerprint returns a canonical digest of the frozen effective Go
// environment, selected Go launcher bytes, and exact selected compiler path and
// bytes. The environment excludes only GOGCCFLAGS: cmd/go documents it as an
// output-only variable that cannot be modified, and its derived value embeds a
// fresh per-command temporary path. Its actual inputs (CC, CGO_*, GOOS, GOARCH,
// and toolchain identity) remain in the fingerprint. Active workspaces are
// intentionally uncacheable: their used-module source lies outside the
// module-root source manifest and therefore cannot be represented by the
// current persistent key.
func (context *GoCommandContext) CacheFingerprint() (string, error) {
	if context == nil {
		return "", fmt.Errorf("codegen: nil Go command context")
	}
	context.cacheOnce.Do(func() {
		if context.buildEnvErr != nil {
			context.cacheKeyErr = context.buildEnvErr
			return
		}
		workspace := environmentValue(context.buildEnv, "GOWORK")
		if workspace != "" && workspace != "off" {
			context.cacheKeyErr = fmt.Errorf("%w: active GOWORK %q", ErrUncacheableGoContext, workspace)
			return
		}
		environmentJSON, err := context.Run("env", "-json")
		if err != nil {
			context.cacheKeyErr = fmt.Errorf("codegen: fingerprint effective Go environment: %w", err)
			return
		}
		environment := map[string]string{}
		if err := json.Unmarshal(environmentJSON, &environment); err != nil {
			context.cacheKeyErr = fmt.Errorf("codegen: decode effective Go environment fingerprint: %w", err)
			return
		}
		// cmd/go marks GOGCCFLAGS as non-modifiable output. Its derived value
		// includes a fresh per-command temporary work path; every input that
		// determines the flags remains represented by the other environment
		// fields and the selected toolchain identities below.
		delete(environment, "GOGCCFLAGS")
		canonicalEnvironment, err := json.Marshal(environment)
		if err != nil {
			context.cacheKeyErr = fmt.Errorf("codegen: encode effective Go environment fingerprint: %w", err)
			return
		}
		goFlags := environmentValue(context.buildEnv, "GOFLAGS")
		flags, err := splitGoQuoted(goFlags)
		if err != nil {
			context.cacheKeyErr = fmt.Errorf("codegen: parse effective GOFLAGS for cache fingerprint: %w", err)
			return
		}
		for _, flag := range flags {
			name, value, hasValue := goFlagValue(flag)
			if name == "mod" && hasValue && value == "vendor" {
				context.cacheKeyErr = fmt.Errorf("%w: effective GOFLAGS selects -mod=vendor", ErrUncacheableGoContext)
				return
			}
		}
		if info, err := os.Stat(filepath.Join(context.moduleRoot, "vendor")); err == nil && info.IsDir() {
			context.cacheKeyErr = fmt.Errorf("%w: module vendor source is present", ErrUncacheableGoContext)
			return
		} else if err != nil && !os.IsNotExist(err) {
			context.cacheKeyErr = fmt.Errorf("codegen: inspect module vendor source: %w", err)
			return
		}
		if context.goLauncher == nil {
			context.cacheKeyErr = fmt.Errorf("codegen: fingerprint Go command: launcher identity is unavailable")
			return
		}
		if err := context.goLauncher.Validate(context.moduleRoot, context.buildEnv); err != nil {
			context.cacheKeyErr = fmt.Errorf("codegen: fingerprint selected Go compiler: %w", err)
			return
		}
		launcherDigest := context.goLauncher.Digest()
		compilerIdentity := context.goLauncher.CompilerIdentity()
		hash := sha256.New()
		fmt.Fprintf(hash, "gsx-go-context-v3\x00path=%s\x00env=%d\x00", context.goLauncher.Path(), len(canonicalEnvironment))
		hash.Write(canonicalEnvironment)
		fmt.Fprintf(hash, "\x00launcher-sha256=%x", launcherDigest)
		fmt.Fprintf(hash, "\x00compiler=%d\x00", len(compilerIdentity))
		hash.Write([]byte(compilerIdentity))
		context.cacheKey = fmt.Sprintf("%x", hash.Sum(nil))
	})
	return context.cacheKey, context.cacheKeyErr
}

// freezeGoCommandEnvironment snapshots every Go setting whose effective value
// differs from an empty/default environment, then disables later GOENV reads.
// Explicit process settings already present in buildEnv remain authoritative;
// changed values additionally capture settings persisted by `go env -w`.
// GOWORK is resolved separately because its automatic module-root search is
// directory-dependent and must also be fixed at Open. In normal mode gsx owns
// one source-inventory overlay and cannot combine it soundly with another
// overlay or an external packages driver.
func freezeGoCommandEnvironment(buildEnv []string, moduleRoot, packagesDriverPath string, snapshot *golauncher.Snapshot) ([]string, string, string, error) {
	driver := environmentValue(buildEnv, "GOPACKAGESDRIVER")
	switch {
	case driver == "off":
	case driver != "":
		return nil, "", "", fmt.Errorf("codegen: GOPACKAGESDRIVER %q is not supported in normal mode", driver)
	default:
		if packagesDriverPath != "" {
			return nil, "", "", fmt.Errorf("codegen: PATH-discovered gopackagesdriver %q is not supported in normal mode", packagesDriverPath)
		}
	}

	changedOutput, err := snapshot.Run(moduleRoot, buildEnv, "env", "-changed", "-json")
	if err != nil {
		return nil, "", "", fmt.Errorf("codegen: resolve effective Go environment: %w", err)
	}
	changed := map[string]string{}
	if err := json.Unmarshal(changedOutput, &changed); err != nil {
		return nil, "", "", fmt.Errorf("codegen: decode effective Go environment: %w", err)
	}
	keys := make([]string, 0, len(changed))
	for key := range changed {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		buildEnv = environmentWithValue(buildEnv, key, changed[key])
	}

	environmentOutput, err := snapshot.Run(moduleRoot, buildEnv, "env", "-json", "GOWORK", "GOTOOLDIR", "GOHOSTOS")
	if err != nil {
		return nil, "", "", fmt.Errorf("codegen: resolve effective Go workspace and toolchain: %w", err)
	}
	var workspace effectiveGoWorkspace
	if err := json.Unmarshal(environmentOutput, &workspace); err != nil {
		return nil, "", "", fmt.Errorf("codegen: decode effective Go workspace and toolchain: %w", err)
	}
	if workspace.GOWORK == "" {
		workspace.GOWORK = "off"
	}

	effectiveGoFlags := environmentValue(buildEnv, "GOFLAGS")
	flags, err := splitGoQuoted(effectiveGoFlags)
	if err != nil {
		return nil, "", "", fmt.Errorf("codegen: parse effective GOFLAGS: %w", err)
	}
	var overlayValue string
	var overlayHasValue, overlaySeen bool
	for _, flag := range flags {
		name, value, hasValue := goFlagValue(flag)
		if name == "overlay" {
			overlaySeen = true
			overlayValue = value
			overlayHasValue = hasValue
		}
	}
	if overlaySeen && (!overlayHasValue || overlayValue != "") {
		return nil, "", "", fmt.Errorf("codegen: effective GOFLAGS -overlay is not supported in normal mode")
	}

	buildEnv = environmentWithValue(buildEnv, "GOFLAGS", effectiveGoFlags)
	buildEnv = environmentWithValue(buildEnv, "GOWORK", workspace.GOWORK)
	buildEnv = environmentWithValue(buildEnv, "GOENV", "off")
	// x/tools/go/packages consults the live process PATH when no driver is
	// explicit in Config.Env. Pin the already-validated no-driver state so a
	// later process-environment mutation cannot escape this frozen boundary.
	return environmentWithValue(buildEnv, "GOPACKAGESDRIVER", "off"), workspace.GOTOOLDIR, workspace.GOHOSTOS, nil
}

func (m *Module) validateGoCommandLauncher() error {
	if m.opts.Bundle != nil {
		return nil
	}
	if m.goLauncher == nil {
		return fmt.Errorf("codegen: active Go command identity is unavailable; create a new Module after restoring the build environment")
	}
	if err := m.goLauncher.Validate(m.opts.ModuleRoot, m.buildEnv); err != nil {
		return fmt.Errorf("codegen: active Go command changed since Module.Open (%s); create a new Module after changing the build environment", err)
	}
	return nil
}

func goFlagValue(flag string) (name, value string, hasValue bool) {
	trimmed := strings.TrimPrefix(flag, "-")
	trimmed = strings.TrimPrefix(trimmed, "-")
	if name, value, found := strings.Cut(trimmed, "="); found {
		return name, value, true
	}
	return trimmed, "", false
}

func environmentValue(env []string, key string) string {
	prefix := key + "="
	for index := len(env) - 1; index >= 0; index-- {
		if strings.HasPrefix(env[index], prefix) {
			return env[index][len(prefix):]
		}
	}
	return ""
}

func environmentWithValue(env []string, key, value string) []string {
	prefix := key + "="
	out := make([]string, 0, len(env)+1)
	for _, entry := range env {
		if !strings.HasPrefix(entry, prefix) {
			out = append(out, entry)
		}
	}
	return append(out, prefix+value)
}

func isGoFlagSpace(char byte) bool {
	return char == ' ' || char == '\t' || char == '\n' || char == '\r'
}

// splitGoQuoted mirrors cmd/internal/quoted.Split, the parser used by the Go
// command for GOFLAGS. Keeping the same grammar avoids inventing a second flag
// interpretation at the overlay boundary.
func splitGoQuoted(source string) ([]string, error) {
	var fields []string
	for len(source) > 0 {
		for len(source) > 0 && isGoFlagSpace(source[0]) {
			source = source[1:]
		}
		if len(source) == 0 {
			break
		}
		if source[0] == '"' || source[0] == '\'' {
			quote := source[0]
			source = source[1:]
			index := 0
			for index < len(source) && source[index] != quote {
				index++
			}
			if index >= len(source) {
				return nil, fmt.Errorf("unterminated %c string", quote)
			}
			fields = append(fields, source[:index])
			source = source[index+1:]
			continue
		}
		index := 0
		for index < len(source) && !isGoFlagSpace(source[index]) {
			index++
		}
		fields = append(fields, source[:index])
		source = source[index:]
	}
	return fields, nil
}
