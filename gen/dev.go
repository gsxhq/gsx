package gen

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/fsnotify/fsnotify"
)

// runDev owns the dev loop: it generates (warm Module), builds+runs the Go
// server, supervises Vite, watches sources + .env, and drives the browser. It
// returns 0 on clean shutdown (SIGINT/SIGTERM), 1 on a fatal startup error.
func runDev(args []string, stdout, stderr io.Writer, merged config, td *tomlDev, workDir string) int {
	// --- flags ---
	fs := flag.NewFlagSet("dev", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var (
		webFlag, buildFlag, runFlag, logFlag string
		noWeb                                bool
	)
	fs.StringVar(&webFlag, "web", "", "front-door command (default: npx vite)")
	fs.BoolVar(&noWeb, "no-web", false, "don't run the front door; manage only the Go side")
	fs.StringVar(&buildFlag, "build", "", "server build command (default: go build -o <cache>/server .)")
	fs.StringVar(&runFlag, "run", "", "server run command (default: <cache>/server)")
	fs.StringVar(&logFlag, "log", "\x00", "write the backend log to PATH (bare: cache-dir dev.log; off by default)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	logSet := logFlag != "\x00"

	dc := resolveDevConfig(workDir, td, devFlags{
		web:    splitArgv(webFlag),
		build:  splitArgv(buildFlag),
		run:    splitArgv(runFlag),
		log:    logFileArg(logFlag, logSet),
		noWeb:  noWeb,
		noLog:  logSet && logFlag == "",
		logSet: logSet,
	})

	// --- env + ports ---
	env := append(os.Environ(), loadDotEnv(workDir)...)
	goPort := envPort(env, "GO_PORT", "7777")
	viteURL := envValue(env, "VITE_DEV_URL", "http://localhost:5173")
	healthURL := "http://localhost:" + goPort + "/healthz"

	var termMu sync.Mutex
	mkWriter := func(name string) io.Writer { return &prefixWriter{name: name, w: stdout, mu: &termMu} }

	// --- backend log (opt-in) ---
	serverOut := mkWriter("server")
	if dc.logPath != "" {
		_ = os.MkdirAll(filepath.Dir(dc.logPath), 0o755)
		if lf, err := os.Create(dc.logPath); err == nil { // truncate once at startup
			defer lf.Close()
			serverOut = io.MultiWriter(serverOut, lf)
			fmt.Fprintf(stderr, "gsx dev: backend log → %s\n", dc.logPath)
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// --- warm watch session: initial cold generate ---
	//
	// Guard against newWatchSession panicking when there are no .gsx files.
	// Without any .gsx source the overlay has nothing to generate, and the watch
	// session cannot open a Module. Fail clearly so the user adds .gsx source.
	gsxDirs, _ := discoverDirs([]string{workDir})
	if len(gsxDirs) == 0 {
		fmt.Fprintf(stderr, "gsx dev: no .gsx files found under %s\n", workDir)
		return 1
	}

	wcfg := watchConfig{
		paths: []string{workDir}, stdout: stdout, stderr: stderr,
		filterPkgs: merged.filterPkgs, aliases: merged.aliases, cls: merged.classifier(),
		fm: merged.fieldMatcher, cssMin: merged.effectiveCSSMin(), jsMin: merged.effectiveJSMin(),
		cssMinify: merged.cssMinLevel.enabled(), jsMinify: merged.jsMinLevel.enabled(),
		classMerger: merged.classMerger,
	}
	sess, startup, err := newWatchSession(wcfg)
	if err != nil {
		fmt.Fprintf(stderr, "gsx dev: %v\n", err)
		return 1
	}
	// Drive the overlay from the cold generate (e.g. a pre-existing codegen error).
	postEvent(viteURL, aggregateEvent(startup))

	// --- Vite (front door), unless --no-web ---
	var vite *exec.Cmd
	if dc.web != nil {
		vite = exec.Command(dc.web[0], dc.web[1:]...)
		vite.Env, vite.Stdout, vite.Stderr = env, mkWriter("vite"), mkWriter("vite")
		setProcGroup(vite)
		if err := vite.Start(); err != nil {
			fmt.Fprintf(stderr, "gsx dev: starting front door: %v\n", err)
			return 1
		}
	}

	// --- Go server: initial build + run ---
	srv := &devServer{build: dc.build, run: dc.run, env: env, out: serverOut, healthURL: healthURL}
	if err := srv.rebuild(ctx); err == nil {
		if waitHealthy(ctx, healthURL, 10*time.Second) {
			postReload(viteURL)
		}
	}

	// --- fsnotify watcher (sources + .env) ---
	w, err := fsnotify.NewWatcher()
	if err != nil {
		fmt.Fprintf(stderr, "gsx dev: %v\n", err)
		srv.stop()
		if vite != nil {
			killProcGroup(vite, 5*time.Second)
		}
		return 1
	}
	defer w.Close()
	addWatchTree(w, []string{workDir})

	fmt.Fprintf(stdout, "gsx dev: watching %s — open %s\n", workDir, viteURL)

	var (
		pending  = map[string]bool{}
		depDirty bool
		envDirty bool
		timer    *time.Timer
		fire     = make(chan struct{}, 1)
	)
	schedule := func() {
		if timer != nil {
			timer.Stop()
		}
		timer = time.AfterFunc(120*time.Millisecond, func() {
			select {
			case fire <- struct{}{}:
			default:
			}
		})
	}

	for {
		select {
		case <-ctx.Done():
			fmt.Fprintln(stdout, "\ngsx dev: shutting down…")
			srv.stop()
			if vite != nil {
				killProcGroup(vite, 5*time.Second)
			}
			return 0

		case ev := <-w.Events:
			if isEnvFile(ev.Name) {
				envDirty = true
				schedule()
				continue
			}
			if !watchable(ev.Name) {
				continue
			}
			if isDepFile(ev.Name) {
				depDirty = true
			}
			pending[filepath.Dir(ev.Name)] = true
			if ev.Op&fsnotify.Create != 0 {
				if fi, statErr := os.Stat(ev.Name); statErr == nil && fi.IsDir() && !excludedDir(ev.Name) {
					_ = w.Add(ev.Name)
				}
			}
			schedule()

		case <-fire:
			// .env change → restart server with fresh env (no rebuild) + reload.
			if envDirty {
				envDirty = false
				env = append(os.Environ(), loadDotEnv(workDir)...)
				srv.env = env
				if err := srv.restartNoBuild(); err == nil && waitHealthy(ctx, healthURL, 10*time.Second) {
					postReload(viteURL)
				}
				// fall through: an .env-only fire has empty pending.
			}
			if len(pending) == 0 && !depDirty {
				continue
			}
			results, rerr := sess.regenPending(pending, depDirty)
			goChanged := depDirty
			pending = map[string]bool{}
			depDirty = false
			if rerr != nil {
				fmt.Fprintf(serverOut, "regen failed: %v\n", rerr)
				continue // preserve nothing; next event retries
			}
			// Overlay state from this cycle.
			postEvent(viteURL, aggregateEvent(results))
			ok := true
			wrote := false
			for _, r := range results {
				ok = ok && r.OK
				wrote = wrote || len(r.Written) > 0
			}
			if !ok {
				continue // keep last-good server up; overlay shows the error
			}
			if goChanged || wrote {
				if err := srv.rebuild(ctx); err == nil && waitHealthy(ctx, healthURL, 10*time.Second) {
					postReload(viteURL)
				}
			}

		case werr := <-w.Errors:
			fmt.Fprintf(stderr, "gsx dev: watch error: %v\n", werr)
		}
	}
}

// devTomlFor re-reads the [dev] table from the discovered gsx.toml. The codegen
// config path (loadConfig) deliberately ignores [dev]; runDev needs it, so we
// decode the file once more here. Returns nil when there's no config or no [dev].
func devTomlFor(configPath string) *tomlDev {
	if configPath == "" {
		return nil
	}
	var tc tomlConfig
	if _, err := toml.DecodeFile(configPath, &tc); err != nil {
		return nil
	}
	return tc.Dev
}

// isEnvFile reports whether path is a .env file we should restart the server on.
func isEnvFile(path string) bool {
	b := filepath.Base(path)
	return b == ".env" || strings.HasPrefix(b, ".env.")
}

// splitArgv splits a flag value into argv on whitespace (the common case; use
// the gsx.toml [dev] array form for exact quoting). Empty ⇒ nil (not set).
func splitArgv(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return strings.Fields(s)
}

// logFileArg maps the --log flag into the devFlags.log slice: a bare --log (set,
// empty) yields nil (resolveDevConfig fills the cache-dir default); --log PATH
// yields [PATH]; unset yields nil.
func logFileArg(v string, set bool) []string {
	if set && v != "" {
		return []string{v}
	}
	return nil
}
