package gen

import (
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
	if dc.logPath != "tmp/dev.log" {
		t.Errorf("log from toml = %q", dc.logPath)
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
	// --log-file=PATH → that path
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
