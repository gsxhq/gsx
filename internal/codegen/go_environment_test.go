package codegen

import (
	"os"
	"os/exec"
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

func TestModuleFreezesEffectiveGoEnvAcrossColdReloads(t *testing.T) {
	root, uiDir := writeTargetDeclarationTestModule(t)
	goEnv := filepath.Join(root, "go.env")
	writeFile(t, root, "go.env", "GOFLAGS=-tags=feature\n")
	writeFile(t, uiDir, "card.gsx", "package ui\ncomponent Card(value Active) { <div>{ value }</div> }\n")
	writeFile(t, uiDir, "feature.go", "//go:build feature\n\npackage ui\ntype Active int\n")
	unsetEnvironment(t, "GOFLAGS")
	t.Setenv("GOENV", goEnv)

	module, err := Open(Options{ModuleRoot: root, ModulePath: "example.com/app"})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	// Mutate GOENV before the first cold load: freezing on first use is too late.
	writeFile(t, root, "go.env", "GOFLAGS=\n")
	first, diagnostics, err := module.Generate(uiDir)
	if err != nil {
		t.Fatalf("first Generate: %v", err)
	}
	if hasError(diagnostics) || len(first[filepath.Join(uiDir, "card.gsx")]) == 0 {
		t.Fatalf("first Generate output=%v diagnostics=%v", keysOfGenerated(first), diagnostics)
	}

	writeFile(t, root, "go.env", "GOFLAGS=-tags=other\n")
	module.rebuildFset()
	second, diagnostics, err := module.Generate(uiDir)
	if err != nil {
		t.Fatalf("second Generate: %v", err)
	}
	if hasError(diagnostics) || len(second[filepath.Join(uiDir, "card.gsx")]) == 0 {
		t.Fatalf("second Generate output=%v diagnostics=%v", keysOfGenerated(second), diagnostics)
	}
	if string(second[filepath.Join(uiDir, "card.gsx")]) != string(first[filepath.Join(uiDir, "card.gsx")]) {
		t.Fatal("same Module changed generated output after GOENV mutation and cold reload")
	}
}

func TestModuleFreezesGoEnvBuildPlatformAcrossColdReloads(t *testing.T) {
	root, uiDir := writeTargetDeclarationTestModule(t)
	goEnv := filepath.Join(root, "go.env")
	writeFile(t, root, "go.env", "GOOS=plan9\nGOARCH=amd64\nCGO_ENABLED=0\n")
	writeFile(t, uiDir, "card.gsx", "package ui\ncomponent Card(value Active) { <div>{ value }</div> }\n")
	writeFile(t, uiDir, "platform.go", "//go:build plan9 && amd64\n\npackage ui\ntype Active int\n")
	unsetEnvironment(t, "GOOS")
	unsetEnvironment(t, "GOARCH")
	unsetEnvironment(t, "CGO_ENABLED")
	t.Setenv("GOENV", goEnv)

	module, err := Open(Options{ModuleRoot: root, ModulePath: "example.com/app"})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	// The semantic cold load must use Open-time GOENV values even if the file is
	// changed before packages.Load first runs.
	writeFile(t, root, "go.env", "GOOS=windows\nGOARCH=amd64\nCGO_ENABLED=0\n")
	first, diagnostics, err := module.Generate(uiDir)
	if err != nil {
		t.Fatalf("first Generate: %v", err)
	}
	if hasError(diagnostics) || len(first[filepath.Join(uiDir, "card.gsx")]) == 0 {
		t.Fatalf("first Generate output=%v diagnostics=%v", keysOfGenerated(first), diagnostics)
	}

	writeFile(t, root, "go.env", "GOOS=linux\nGOARCH=amd64\nCGO_ENABLED=1\n")
	module.rebuildFset()
	second, diagnostics, err := module.Generate(uiDir)
	if err != nil {
		t.Fatalf("second Generate: %v", err)
	}
	if hasError(diagnostics) || len(second[filepath.Join(uiDir, "card.gsx")]) == 0 {
		t.Fatalf("second Generate output=%v diagnostics=%v", keysOfGenerated(second), diagnostics)
	}
	if string(second[filepath.Join(uiDir, "card.gsx")]) != string(first[filepath.Join(uiDir, "card.gsx")]) {
		t.Fatal("same Module changed generated output after GOENV platform mutation and cold reload")
	}
}

func TestModuleRejectsLiveGoLauncherDriftBeforeColdLoad(t *testing.T) {
	for _, test := range []struct {
		name       string
		warmBefore bool
	}{
		{name: "before first load"},
		{name: "after FileSet rebuild", warmBefore: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			root, uiDir := writeTargetDeclarationTestModule(t)
			originalPath := os.Getenv("PATH")
			module, err := Open(Options{ModuleRoot: root, ModulePath: "example.com/app"})
			if err != nil {
				t.Fatal(err)
			}
			if test.warmBefore {
				if _, diagnostics, err := module.Generate(uiDir); err != nil || hasError(diagnostics) {
					t.Fatalf("warm Generate error=%v diagnostics=%v", err, diagnostics)
				}
				module.rebuildFset()
			}

			fakeDir := t.TempDir()
			marker := filepath.Join(fakeDir, "invoked")
			fakeGo := filepath.Join(fakeDir, "go")
			if err := os.WriteFile(fakeGo, []byte("#!/bin/sh\nprintf invoked > '"+marker+"'\nexit 97\n"), 0o755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", fakeDir+string(os.PathListSeparator)+originalPath)
			_, _, err = module.Generate(uiDir)
			if err == nil || !strings.Contains(err.Error(), "create a new Module") {
				t.Fatalf("Generate error = %v, want frozen Go-launcher drift guidance", err)
			}
			if _, statErr := os.Stat(marker); !os.IsNotExist(statErr) {
				t.Fatalf("live-PATH fake Go command executed before drift rejection: %v", statErr)
			}
			wantLoads := 0
			if test.warmBefore {
				wantLoads = 1
			}
			if got := module.externalLoads(); got != wantLoads {
				t.Fatalf("external loads = %d, want %d (drift rejected before packages.Load)", got, wantLoads)
			}
		})
	}
}

func TestModuleRejectsInPlaceGoLauncherMutationDuringColdLoad(t *testing.T) {
	root, uiDir := writeTargetDeclarationTestModule(t)
	realGo, err := exec.LookPath("go")
	if err != nil {
		t.Fatal(err)
	}
	launcherDir := t.TempDir()
	launcher := filepath.Join(launcherDir, "go")
	const source = `#!/bin/sh
if [ "$1" = "list" ]; then
	printf '#!/bin/sh\nexec "$REAL_GO" "$@"\n' > "$0"
fi
exec "$REAL_GO" "$@"
`
	if err := os.WriteFile(launcher, []byte(source), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("REAL_GO", realGo)
	t.Setenv("PATH", launcherDir)
	t.Setenv("GOPACKAGESDRIVER", "off")

	module, err := Open(Options{ModuleRoot: root, ModulePath: "example.com/app"})
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = module.Generate(uiDir)
	if err == nil || !strings.Contains(err.Error(), "create a new Module") {
		t.Fatalf("Generate error = %v, want in-place Go-launcher mutation rejection", err)
	}
	if got := module.externalLoads(); got != 1 {
		t.Fatalf("external loads = %d, want one rejected cold load", got)
	}
}

func TestOpenBundleIgnoresGoCommandEnvironmentBoundaries(t *testing.T) {
	root := t.TempDir()
	t.Setenv("GOFLAGS", "-overlay="+filepath.Join(root, "missing-overlay.json"))
	t.Setenv("GOPACKAGESDRIVER", filepath.Join(root, "missing-driver"))

	module, err := Open(Options{ModuleRoot: root, ModulePath: "example.com/app", Bundle: testBundle(targetTestImporter(), funcTables{})})
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
