package gen

import (
	"crypto/sha1"
	"encoding/hex"
	"os"
	"path/filepath"
)

// devConfig is the resolved dev-loop configuration for `gsx dev`. web == nil
// means the front door is disabled (--no-web); logPath == "" means logging off.
type devConfig struct {
	web     []string
	build   []string
	run     []string
	logPath string
	host    string // hostname for VITE_DEV_URL; "" means localhost
}

// devFlags carries the CLI-flag layer for resolveDevConfig. A nil command slice
// means "flag not given" (fall through to toml/default). logSet distinguishes
// "--log not given" from "--log given" (with or without a value); noLog forces
// logging off; noWeb forces the front door off.
type devFlags struct {
	web, build, run, log []string
	noWeb, noLog, logSet bool
}

// devCacheDir returns the per-project artifact dir for `gsx dev`:
// <UserCacheDir>/gsx-dev/<hash(abs workDir)>, falling back to TempDir. The hash
// keys per checkout so worktrees of the same project never collide.
func devCacheDir(workDir string) string {
	abs, err := filepath.Abs(workDir)
	if err != nil {
		abs = workDir
	}
	sum := sha1.Sum([]byte(abs))
	hash := hex.EncodeToString(sum[:])[:8]
	base, err := os.UserCacheDir()
	if err != nil || base == "" {
		base = os.TempDir()
	}
	return filepath.Join(base, "gsx-dev", hash)
}

// devBinPath is the built server binary path inside the project's cache dir.
func devBinPath(workDir string) string {
	return filepath.Join(devCacheDir(workDir), "server")
}

// resolveDevConfig applies precedence flag > [dev] table > convention default.
// The build/run defaults reference the cache-dir binary so the working tree
// stays clean.
func resolveDevConfig(workDir string, td *tomlDev, fl devFlags) devConfig {
	bin := devBinPath(workDir)
	dc := devConfig{
		web:   []string{"npx", "vite"},
		build: []string{"go", "build", "-o", bin, "."},
		run:   []string{bin},
	}

	// Layer: [dev] table.
	if td != nil {
		if len(td.Web) > 0 {
			dc.web = td.Web
		}
		if td.NoWeb {
			dc.web = nil
		}
		if len(td.Build) > 0 {
			dc.build = td.Build
		}
		if len(td.Run) > 0 {
			dc.run = td.Run
		}
		if td.Log != "" {
			dc.logPath = td.Log
		}
		if td.Host != "" {
			dc.host = td.Host
		}
	}

	// Layer: CLI flags (win).
	if len(fl.web) > 0 {
		dc.web = fl.web
	}
	if fl.noWeb {
		dc.web = nil
	}
	if len(fl.build) > 0 {
		dc.build = fl.build
	}
	if len(fl.run) > 0 {
		dc.run = fl.run
	}
	if fl.logSet {
		switch {
		case fl.noLog:
			dc.logPath = ""
		case len(fl.log) > 0:
			dc.logPath = fl.log[0]
		default: // bare --log → cache-dir dev.log
			dc.logPath = filepath.Join(devCacheDir(workDir), "dev.log")
		}
	}
	return dc
}
