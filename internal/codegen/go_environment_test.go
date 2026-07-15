package codegen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func unsetEnvironment(t *testing.T, key string) {
	t.Helper()
	value, present := os.LookupEnv(key)
	if err := os.Unsetenv(key); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if !present {
			_ = os.Unsetenv(key)
			return
		}
		_ = os.Setenv(key, value)
	})
}

func TestOpenRejectsGoEnvOverlay(t *testing.T) {
	root := t.TempDir()
	goEnv := filepath.Join(root, "go.env")
	writeFile(t, root, "go.mod", "module example.com/app\n\ngo 1.26.1\n")
	writeFile(t, root, "go.env", "GOFLAGS=-overlay="+filepath.Join(root, "overlay.json")+"\n")
	unsetEnvironment(t, "GOFLAGS")
	t.Setenv("GOENV", goEnv)

	module, err := Open(Options{ModuleRoot: root, ModulePath: "example.com/app"})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := module.externalImporter(); err == nil || !strings.Contains(err.Error(), "GOFLAGS -overlay") {
		t.Fatalf("externalImporter error = %v, want effective GOFLAGS -overlay boundary", err)
	}
	if got := module.externalLoads(); got != 0 {
		t.Fatalf("external loads = %d, want rejection before packages.Load", got)
	}
}

func TestOpenRejectsExplicitGoPackagesDriver(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "go.mod", "module example.com/app\n\ngo 1.26.1\n")
	t.Setenv("GOPACKAGESDRIVER", filepath.Join(root, "driver"))

	module, err := Open(Options{ModuleRoot: root, ModulePath: "example.com/app"})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := module.externalImporter(); err == nil || !strings.Contains(err.Error(), "GOPACKAGESDRIVER") {
		t.Fatalf("externalImporter error = %v, want explicit GOPACKAGESDRIVER boundary", err)
	}
	if got := module.externalLoads(); got != 0 {
		t.Fatalf("external loads = %d, want rejection before packages.Load", got)
	}
}

func TestOpenRejectsPathDiscoveredGoPackagesDriver(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "go.mod", "module example.com/app\n\ngo 1.26.1\n")
	driver := filepath.Join(root, "gopackagesdriver")
	if err := os.WriteFile(driver, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	unsetEnvironment(t, "GOPACKAGESDRIVER")
	t.Setenv("PATH", root+string(os.PathListSeparator)+os.Getenv("PATH"))

	module, err := Open(Options{ModuleRoot: root, ModulePath: "example.com/app"})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := module.externalImporter(); err == nil || !strings.Contains(err.Error(), "gopackagesdriver") {
		t.Fatalf("externalImporter error = %v, want PATH-discovered GOPACKAGESDRIVER boundary", err)
	}
	if got := module.externalLoads(); got != 0 {
		t.Fatalf("external loads = %d, want rejection before packages.Load", got)
	}
}

func TestNormalModeFreezesPathDriverDiscoveryAtOpen(t *testing.T) {
	t.Run("present at Open remains rejected", func(t *testing.T) {
		root := t.TempDir()
		writeFile(t, root, "go.mod", "module example.com/app\n\ngo 1.26.1\n")
		driver := filepath.Join(root, "gopackagesdriver")
		if err := os.WriteFile(driver, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		originalPath := os.Getenv("PATH")
		unsetEnvironment(t, "GOPACKAGESDRIVER")
		t.Setenv("PATH", root+string(os.PathListSeparator)+originalPath)
		module, err := Open(Options{ModuleRoot: root, ModulePath: "example.com/app"})
		if err != nil {
			t.Fatal(err)
		}
		t.Setenv("PATH", originalPath)
		if _, err := module.externalImporter(); err == nil || !strings.Contains(err.Error(), "gopackagesdriver") {
			t.Fatalf("externalImporter error = %v, want PATH state captured at Open", err)
		}
	})

	t.Run("absent at Open ignores later driver", func(t *testing.T) {
		root := t.TempDir()
		writeFile(t, root, "go.mod", "module example.com/app\n\ngo 1.26.1\n")
		originalPath := os.Getenv("PATH")
		unsetEnvironment(t, "GOPACKAGESDRIVER")
		module, err := Open(Options{ModuleRoot: root, ModulePath: "example.com/app"})
		if err != nil {
			t.Fatal(err)
		}
		driver := filepath.Join(root, "gopackagesdriver")
		if err := os.WriteFile(driver, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		t.Setenv("PATH", root+string(os.PathListSeparator)+originalPath)
		if _, err := module.externalImporter(); err != nil {
			t.Fatalf("externalImporter: %v, want later PATH mutation outside frozen Module environment", err)
		}
	})
}

func TestOpenAllowsEmptyOverlayAndDisabledPathDriver(t *testing.T) {
	root, _ := writeTargetDeclarationTestModule(t)
	driver := filepath.Join(root, "gopackagesdriver")
	if err := os.WriteFile(driver, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GOFLAGS", "-overlay=")
	t.Setenv("GOPACKAGESDRIVER", "off")
	t.Setenv("PATH", root+string(os.PathListSeparator)+os.Getenv("PATH"))

	module, err := Open(Options{ModuleRoot: root, ModulePath: "example.com/app"})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := module.externalImporter(); err != nil {
		t.Fatalf("externalImporter: %v", err)
	}
	if got := module.externalLoads(); got != 1 {
		t.Fatalf("external loads = %d, want one normal packages.Load", got)
	}
}

func TestNormalModeUsesLastGoFlagsOverlay(t *testing.T) {
	t.Run("last empty disables earlier overlay", func(t *testing.T) {
		root, _ := writeTargetDeclarationTestModule(t)
		t.Setenv("GOFLAGS", "-overlay="+filepath.Join(root, "missing-overlay.json")+" -overlay=")
		t.Setenv("GOPACKAGESDRIVER", "off")
		module, err := Open(Options{ModuleRoot: root, ModulePath: "example.com/app"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := module.externalImporter(); err != nil {
			t.Fatalf("externalImporter: %v, want final empty overlay to disable the earlier value", err)
		}
	})

	t.Run("last non-empty overlay is rejected", func(t *testing.T) {
		root, _ := writeTargetDeclarationTestModule(t)
		t.Setenv("GOFLAGS", "-overlay= -overlay="+filepath.Join(root, "missing-overlay.json"))
		t.Setenv("GOPACKAGESDRIVER", "off")
		module, err := Open(Options{ModuleRoot: root, ModulePath: "example.com/app"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := module.externalImporter(); err == nil || !strings.Contains(err.Error(), "GOFLAGS -overlay") {
			t.Fatalf("externalImporter error = %v, want final non-empty overlay rejected", err)
		}
		if got := module.externalLoads(); got != 0 {
			t.Fatalf("external loads = %d, want rejection before packages.Load", got)
		}
	})
}

func TestOpenBundleIgnoresGoCommandEnvironmentBoundaries(t *testing.T) {
	root := t.TempDir()
	t.Setenv("GOFLAGS", "-overlay="+filepath.Join(root, "missing-overlay.json"))
	t.Setenv("GOPACKAGESDRIVER", filepath.Join(root, "missing-driver"))

	module, err := Open(Options{ModuleRoot: root, ModulePath: "example.com/app", Bundle: &Bundle{}})
	if err != nil {
		t.Fatalf("Bundle Open error = %v, want no Go-command environment validation", err)
	}
	if _, err := module.externalImporter(); err != nil {
		t.Fatalf("Bundle externalImporter error = %v, want no Go-command environment validation", err)
	}
	if got := module.externalLoads(); got != 0 {
		t.Fatalf("Bundle external loads = %d, want zero", got)
	}
}
