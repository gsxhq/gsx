package gen

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestResolveDevConfigDefaults(t *testing.T) {
	wd := t.TempDir()
	dc := resolveDevConfig(wd, nil, devFlags{})
	bin := devBinPath(wd)
	if !reflect.DeepEqual(dc.web, []string{"npx", "vite"}) {
		t.Errorf("web default = %v", dc.web)
	}
	if !reflect.DeepEqual(dc.build, []string{"go", "build", "-o", bin, "."}) {
		t.Errorf("build default = %v", dc.build)
	}
	if !reflect.DeepEqual(dc.run, []string{bin}) {
		t.Errorf("run default = %v", dc.run)
	}
	if dc.logPath != "" {
		t.Errorf("log should be off by default, got %q", dc.logPath)
	}
}

func TestResolveDevConfigTomlThenFlags(t *testing.T) {
	wd := t.TempDir()
	td := &tomlDev{
		Web:   []string{"pnpm", "vite"},
		Build: []string{"go", "build", "-tags", "dev", "-o", "tmp/app", "."},
		Run:   []string{"tmp/app"},
		Log:   "tmp/dev.log",
	}
	// toml only:
	dc := resolveDevConfig(wd, td, devFlags{})
	if !reflect.DeepEqual(dc.web, []string{"pnpm", "vite"}) {
		t.Errorf("web from toml = %v", dc.web)
	}
	// A relative [dev].log anchors to workDir (the gsx.toml project
	// directory), not the process's cwd.
	if want := filepath.Join(wd, "tmp/dev.log"); dc.logPath != want {
		t.Errorf("log from toml = %q, want %q", dc.logPath, want)
	}
	// flag overrides toml:
	dc = resolveDevConfig(wd, td, devFlags{web: []string{"yarn", "vite"}, noLog: true, logSet: true})
	if !reflect.DeepEqual(dc.web, []string{"yarn", "vite"}) {
		t.Errorf("flag should override web, got %v", dc.web)
	}
	if dc.logPath != "" {
		t.Errorf("--no-log should disable, got %q", dc.logPath)
	}
}

func TestResolveDevConfigNoWeb(t *testing.T) {
	wd := t.TempDir()
	if dc := resolveDevConfig(wd, &tomlDev{NoWeb: true}, devFlags{}); dc.web != nil {
		t.Errorf("no_web should null web, got %v", dc.web)
	}
	if dc := resolveDevConfig(wd, nil, devFlags{noWeb: true}); dc.web != nil {
		t.Errorf("--no-web should null web, got %v", dc.web)
	}
}

func TestDevBinPathStableAndScoped(t *testing.T) {
	wd := t.TempDir()
	a, b := devBinPath(wd), devBinPath(wd)
	if a != b {
		t.Errorf("devBinPath not stable: %q vs %q", a, b)
	}
	if c := devBinPath(filepath.Join(wd, "other")); c == a {
		t.Errorf("devBinPath not scoped per project")
	}
	if !strings.Contains(a, "gsx-dev") || filepath.Base(a) != "server" {
		t.Errorf("unexpected bin path %q", a)
	}
}

func TestResolveDevConfigLogBareDefaultsToCacheDir(t *testing.T) {
	wd := t.TempDir()
	// --log with no value (logSet, empty log slice) → cache-dir dev.log.
	dc := resolveDevConfig(wd, nil, devFlags{logSet: true})
	want := filepath.Join(devCacheDir(wd), "dev.log")
	if dc.logPath != want {
		t.Errorf("bare --log = %q, want %q", dc.logPath, want)
	}
}

func TestDevFlagsLogResolution(t *testing.T) {
	wd := t.TempDir()
	// --log (bool) → cache-dir default
	dc := resolveDevConfig(wd, nil, devFlagsFromValues("", "", "", "", true, false, false))
	if dc.logPath != filepath.Join(devCacheDir(wd), "dev.log") {
		t.Errorf("--log should enable cache-dir log, got %q", dc.logPath)
	}
	// --log-file=PATH → that path, cwd-anchored (unlike [dev].log): a path
	// typed at a shell means the shell's cwd, not workDir.
	dc = resolveDevConfig(wd, nil, devFlagsFromValues("", "", "", "tmp/dev.log", false, false, false))
	if dc.logPath != "tmp/dev.log" {
		t.Errorf("--log-file should set path, got %q", dc.logPath)
	}
	// --no-log → off (even overriding a gsx.toml [dev].log)
	dc = resolveDevConfig(wd, &tomlDev{Log: "tmp/dev.log"}, devFlagsFromValues("", "", "", "", false, false, true))
	if dc.logPath != "" {
		t.Errorf("--no-log should disable, got %q", dc.logPath)
	}
	// none → off
	dc = resolveDevConfig(wd, nil, devFlagsFromValues("", "", "", "", false, false, false))
	if dc.logPath != "" {
		t.Errorf("no log flags should be off, got %q", dc.logPath)
	}
}

// TestResolveDevConfigLogAnchoring pins the CONFIG-layer/FLAG-layer asymmetry:
// a relative [dev].log anchors to workDir (the gsx.toml project directory) —
// `gsx dev ./proj` run from elsewhere must still write proj/tmp/dev.log, not
// $CWD/tmp/dev.log — while an already-absolute [dev].log passes through
// untouched, and the --log-file flag keeps meaning "relative to the shell's
// cwd" because a path typed at a shell means the shell's cwd.
func TestResolveDevConfigLogAnchoring(t *testing.T) {
	wd := t.TempDir() // workDir: deliberately NOT the test process's cwd.
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if wd == cwd {
		t.Fatalf("test workDir accidentally equals process cwd %q; test would not catch cwd-anchoring", wd)
	}

	// Relative [dev].log anchors to workDir, not the process cwd.
	dc := resolveDevConfig(wd, &tomlDev{Log: "tmp/dev.log"}, devFlags{})
	want := filepath.Join(wd, "tmp/dev.log")
	if dc.logPath != want {
		t.Errorf("relative [dev].log = %q, want workDir-anchored %q", dc.logPath, want)
	}
	if cwdAnchored := filepath.Join(cwd, "tmp/dev.log"); dc.logPath == cwdAnchored && want != cwdAnchored {
		t.Errorf("relative [dev].log resolved against cwd (%q) instead of workDir", cwdAnchored)
	}

	// An absolute [dev].log is untouched.
	abs := filepath.Join(t.TempDir(), "custom", "backend.log")
	dc = resolveDevConfig(wd, &tomlDev{Log: abs}, devFlags{})
	if dc.logPath != abs {
		t.Errorf("absolute [dev].log = %q, want unchanged %q", dc.logPath, abs)
	}

	// --log-file keeps cwd anchoring: resolveDevConfig must not rewrite it.
	dc = resolveDevConfig(wd, nil, devFlagsFromValues("", "", "", "tmp/dev.log", false, false, false))
	if dc.logPath != "tmp/dev.log" {
		t.Errorf("--log-file should stay cwd-relative (unanchored), got %q", dc.logPath)
	}
}

// TestResolveDevConfigLogAnchorIsAbsIdempotent pins the invariant #158's
// GSX_DEV_LOG computation (filepath.Abs(dc.logPath), see dev.go) relies on:
// once resolveDevConfig anchors a config-layer relative path to workDir, that
// path is already absolute, so a subsequent filepath.Abs is a no-op — Abs
// never reintroduces a dependency on the process's cwd. Verified, not
// assumed: a future change to either site that broke this would still leave
// GSX_DEV_LOG naming a real file, just not necessarily the same one gsx dev
// writes when run from outside workDir.
func TestResolveDevConfigLogAnchorIsAbsIdempotent(t *testing.T) {
	wd := t.TempDir()
	dc := resolveDevConfig(wd, &tomlDev{Log: "tmp/dev.log"}, devFlags{})
	abs, err := filepath.Abs(dc.logPath)
	if err != nil {
		t.Fatal(err)
	}
	if abs != dc.logPath {
		t.Errorf("filepath.Abs(%q) = %q, want no-op (path already workDir-anchored)", dc.logPath, abs)
	}
}
