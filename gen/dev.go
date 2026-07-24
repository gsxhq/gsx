package gen

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/fsnotify/fsnotify"

	"github.com/gsxhq/gsx/internal/diag"
)

// genDevToken returns a random 16-byte value hex-encoded (32 chars). runDev
// generates one per process and passes it to its managed vite child via
// GSX_DEV_TOKEN so front-door respawn verification (see verifyFrontDoor) can
// tell "our vite" apart from another gsx project's vite racing onto the same
// freed port during backoff.
func genDevToken() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generating dev token: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// runDev owns the dev loop: it generates (warm Module), builds+runs the Go
// server, supervises Vite, watches sources + .env, and drives the browser. It
// returns 0 on clean shutdown (SIGINT/SIGTERM), 1 on a fatal startup error.
func runDev(args []string, stdout, stderr io.Writer, merged config, td *tomlDev, workDir string) int {
	// --- flags ---
	fs := flag.NewFlagSet("dev", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var (
		webFlag, buildFlag, runFlag, logFileFlag string
		logFlag, noWeb, noLog                    bool
	)
	fs.StringVar(&webFlag, "web", "", "front-door command (default: npx vite)")
	fs.BoolVar(&noWeb, "no-web", false, "don't run the front door; manage only the Go side")
	fs.StringVar(&buildFlag, "build", "", "server build command (default: go build -o <cache>/server .)")
	fs.StringVar(&runFlag, "run", "", "server run command (default: <cache>/server)")
	fs.BoolVar(&logFlag, "log", false, "enable the backend log at the cache-dir default path")
	fs.StringVar(&logFileFlag, "log-file", "", "enable the backend log at `path`")
	fs.BoolVar(&noLog, "no-log", false, "disable the backend log (overrides gsx.toml [dev].log)")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	dc := resolveDevConfig(workDir, td, devFlagsFromValues(webFlag, buildFlag, runFlag, logFileFlag, logFlag, noWeb, noLog))

	// --- env + ports ---
	env := mergeDotEnv(os.Environ(), loadDotEnv(workDir))
	env, viteURL, warning, err := resolveViteDevEnv(env, dc.host)
	if err != nil {
		fmt.Fprintf(stderr, "gsx dev: %v\n", err)
		return 1
	}
	if warning != "" {
		fmt.Fprintf(stderr, "gsx dev: %s\n", warning)
	}
	goPort := envPort(env, "GO_PORT", "7777")
	healthURL := "http://localhost:" + goPort + "/healthz"

	var termMu sync.Mutex
	mkWriter := func(name string) io.Writer { return &prefixWriter{name: name, w: stdout, mu: &termMu} }

	// --- backend log (opt-in) ---
	gsxOut := mkWriter("gsx")
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

	// --- warm watch session: arm observation before the initial snapshot ---
	wcfg := watchConfig{
		paths: []string{workDir}, stdout: stdout, stderr: stderr,
		filterPkgs: merged.filterPkgs, aliases: merged.aliases, renderers: merged.renderers,
		cls:    merged.classifier(),
		cssMin: merged.effectiveCSSMin(), jsMin: merged.effectiveJSMin(), jsonMin: merged.effectiveJSONMin(),
		cssMinify: merged.cssMinLevel.enabled(), jsMinify: merged.jsMinLevel.enabled(),
		verbatimTags: merged.serialization == SerializationVerbatim,
		classMerger:  merged.classMerger,
	}
	armed, err := armWatchSession(wcfg)
	if err != nil {
		fmt.Fprintf(stderr, "gsx dev: %v\n", err)
		return 1
	}
	defer armed.Close()
	sess := armed.session
	w := armed.watcher
	sources := armed.sources
	dirty := newWatchDirtySet()
	startup, err := sess.initialGenerate()
	if err != nil {
		fmt.Fprintf(stderr, "gsx dev: %v\n", err)
		return 1
	}
	dirty.retainOperational(startup)
	// Drive the overlay from the cold generate (e.g. a pre-existing codegen
	// error). A mixed operational failure has not committed its filesystem
	// transaction, so only its errors/diagnostics are publishable at this point.
	// post/reload gate every browser push on the managed front door being up
	// (see frontDoor). With --no-web the front door is externally managed and
	// pushes always go out.
	var fd *frontDoor
	webUp := func() bool { return fd == nil || fd.up() }
	post := func(body []byte) { postEvent(viteURL, body, webUp) }
	reload := func() { postReload(viteURL, webUp) }

	status := devStatus{Phase: "idle", Server: serverStat{Port: goPort}, FrontDoor: frontStat{State: "external"}}
	if dc.web != nil {
		status.FrontDoor.State = "up"
	}
	postStatus := func() { post(statusEvent(status)) }

	publishedStartup := publishableStartupResults(startup)
	post(aggregateEvent(publishedStartup))
	reportHardErrors(gsxOut, publishedStartup)

	// --- Vite (front door), unless --no-web ---
	// devToken pairs gsx dev with the vite it spawns: only the vite child's
	// env carries it (the Go server's env/srv.env is untouched — it has no
	// use for it, keeping the separation tidy). --no-web / externally-run
	// vite never receives it, so pollCommands sending the header below is
	// harmless there too (an untokened plugin ignores the request header and
	// keeps stamping "1").
	devToken, err := genDevToken()
	if err != nil {
		fmt.Fprintf(stderr, "gsx dev: %v\n", err)
		return 1
	}
	fdCh := make(chan frontStat, 8)
	if dc.web != nil {
		spawn := func() (*exec.Cmd, error) {
			c := exec.Command(dc.web[0], dc.web[1:]...)
			c.Dir, c.Stdout, c.Stderr = workDir, mkWriter("vite"), mkWriter("vite")
			c.Env = append(slices.Clone(env), "GSX_DEV_TOKEN="+devToken)
			setProcGroup(c)
			return c, c.Start()
		}
		fd = newFrontDoor(spawn, viteURL, devToken, func(s frontStat) {
			select {
			case fdCh <- s:
			default: // never block the monitor on a full channel
			}
		}, stdout)
		if err := fd.start(); err != nil {
			fmt.Fprintf(stderr, "gsx dev: starting front door: %v\n", err)
			return 1
		}
	}

	cmds := make(chan string, 8)
	go pollCommands(ctx, viteURL, devToken, webUp, cmds)

	// --- Go server: initial build + run ---
	srv := &devServer{dir: workDir, build: dc.build, run: dc.run, env: env, out: serverOut, buildOut: gsxOut, healthURL: healthURL}
	startOK := true
	for _, r := range startup {
		startOK = startOK && r.OK
	}
	// A failed cold generate wrote poison .x.go — the build cannot succeed, and
	// its buildErrorEvent would replace the rich gsx overlay already posted
	// above. Skip the initial build; the first successful cycle rebuilds and
	// starts the server (poison→good always changes bytes, so `wrote` fires).
	if startOK {
		if out, err := srv.rebuild(ctx); err != nil {
			post(buildErrorEvent(buildFailureMessage(out, err)))
			startOK = false
		} else if waitHealthy(ctx, healthURL, 10*time.Second) {
			status.Server.Healthy = true
			reload()
		}
	}
	status.LastCycle = &cycleStat{OK: startOK, At: time.Now()}
	postStatus()
	// overlayUp: an error overlay is currently shown in the browser. A later
	// successful cycle must reload to clear it even when nothing was written —
	// still needed for build-error and .env recovery paths.
	overlayUp := !startOK

	// Observation has been armed since before initialGenerate, so source and
	// .env events that arrived during startup are already queued on w.
	if dc.web != nil {
		fmt.Fprintf(stdout, "gsx dev: watching %s — open %s\n", workDir, viteURL)
	} else {
		fmt.Fprintf(stdout, "gsx dev: managing Go side only (no front door) — watching %s\n", workDir)
	}
	shutdownProcesses := func() {
		srv.stop()
		if fd != nil {
			fd.shutdown(5 * time.Second)
		}
	}

	var (
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

	// cycle runs one generate→build→reload pass over the current dirty set.
	// force (set by the panel's "rebuild" command) rebuilds+reloads even when
	// nothing is dirty on disk. Every exit path threads status/postStatus so
	// the panel always reflects the outcome of the cycle it triggered.
	cycle := func(force bool) {
		if len(dirty.dirs) == 0 && !dirty.depDirty {
			return
		}
		status.Phase = "generating"
		results, goChanged, rerr := dirty.regenerate(sess.regenPending)
		if rerr != nil {
			fmt.Fprintf(serverOut, "regen failed: %v\n", rerr)
			post(buildErrorEvent("regen failed: " + rerr.Error()))
			overlayUp = true
			status.Phase = "idle"
			status.LastCycle = &cycleStat{OK: false, Errors: 1, At: time.Now()}
			postStatus()
			return // retained dirty state is retried on the next relevant event
		}
		// Overlay state from this cycle.
		post(aggregateEvent(results))
		reportHardErrors(gsxOut, results)
		ok := true
		wrote := false
		errs := 0
		for _, r := range results {
			ok = ok && r.OK
			wrote = wrote || len(r.Written) > 0 || len(r.Removed) > 0
			for _, d := range r.Diags {
				if d.Severity == diag.Error {
					errs++
				}
			}
		}
		if !ok {
			overlayUp = true
			status.Phase = "idle"
			status.LastCycle = &cycleStat{OK: false, Errors: errs, At: time.Now()}
			postStatus()
			return // keep last-good server up; overlay shows the error
		}
		// Successful cycle. Rebuild when code changed (or the panel forced it);
		// reload the browser if we rebuilt OR we're recovering from a shown
		// error overlay — the latter must clear even when nothing was written
		// (fixed .gsx → identical .x.go).
		doReload := overlayUp || force
		if goChanged || wrote || force {
			status.Phase = "building"
			if out, err := srv.rebuild(ctx); err != nil {
				post(buildErrorEvent(buildFailureMessage(out, err)))
				overlayUp = true
				status.Phase = "idle"
				status.Server.Healthy = false
				status.LastCycle = &cycleStat{OK: false, Errors: 1, At: time.Now()}
				postStatus()
				return
			}
			status.Phase = "starting"
			if waitHealthy(ctx, healthURL, 10*time.Second) {
				doReload = true
				status.Server.Healthy = true
			} else {
				status.Server.Healthy = false
			}
		}
		if doReload {
			reload()
		}
		overlayUp = false
		status.Phase = "idle"
		status.LastCycle = &cycleStat{OK: true, Errors: 0, At: time.Now()}
		postStatus()
	}

	for {
		select {
		case <-ctx.Done():
			fmt.Fprintln(stdout, "\ngsx dev: shutting down…")
			if timer != nil {
				timer.Stop()
			}
			shutdownProcesses()
			return 0

		case c := <-cmds:
			switch c {
			case "rebuild":
				fmt.Fprintln(stdout, "gsx dev: panel: rebuild")
				dirty.depDirty = true
				cycle(true)
			case "restart-server":
				fmt.Fprintln(stdout, "gsx dev: panel: restart-server")
				status.Phase = "starting"
				postStatus()
				if err := srv.restartNoBuild(); err == nil && waitHealthy(ctx, healthURL, 10*time.Second) {
					status.Server.Healthy = true
					reload()
				} else {
					status.Server.Healthy = false
				}
				status.Phase = "idle"
				postStatus()
			default:
				fmt.Fprintf(stderr, "gsx dev: unknown panel command %q\n", c)
			}

		case fs := <-fdCh:
			status.FrontDoor = fs
			postStatus()

		case ev, ok := <-w.Events:
			if !ok {
				fmt.Fprintf(stderr, "gsx dev: watch error: %v\n", fsnotify.ErrClosed)
				shutdownProcesses()
				return 1
			}
			// Parent sentinels exist only to observe recreation of an explicitly
			// selected root. Ignore sibling files before special-casing .env.
			if !sources.observed(ev.Name) {
				continue
			}
			if isEnvFile(ev.Name) {
				envDirty = true
				schedule()
				continue
			}
			changed, eventErr := applyWatchEvent(w, ev, sources, dirty.dirs, &dirty.depDirty)
			if eventErr != nil {
				fmt.Fprintf(stderr, "gsx dev: watch event: %v\n", eventErr)
				shutdownProcesses()
				return 1
			}
			if changed {
				schedule()
			}

		case <-fire:
			// .env change → restart server with fresh env (no rebuild) + reload.
			if envDirty {
				envDirty = false
				env = mergeDotEnv(os.Environ(), loadDotEnv(workDir))
				var envErr error
				var envWarning string
				env, viteURL, envWarning, envErr = resolveViteDevEnv(env, dc.host)
				if envErr != nil {
					fmt.Fprintf(stderr, "gsx dev: %v\n", envErr)
					overlayUp = true
					continue
				}
				if envWarning != "" {
					fmt.Fprintf(stderr, "gsx dev: %s\n", envWarning)
				}
				// Vite reads .env itself (loadEnv + native .env watch), so only the Go server is restarted here.
				srv.env = env
				goPort = envPort(env, "GO_PORT", "7777")
				status.Server.Port = goPort
				healthURL = "http://localhost:" + goPort + "/healthz"
				srv.healthURL = healthURL
				if err := srv.restartNoBuild(); err == nil && waitHealthy(ctx, healthURL, 10*time.Second) {
					reload()
					overlayUp = false
				}
				// fall through: an .env-only fire has no source dirtiness.
			}
			cycle(false)

		case werr, ok := <-w.Errors:
			if !ok || errors.Is(werr, fsnotify.ErrClosed) {
				if werr == nil {
					werr = fsnotify.ErrClosed
				}
				fmt.Fprintf(stderr, "gsx dev: watch error: %v\n", werr)
				shutdownProcesses()
				return 1
			}
			fmt.Fprintf(stderr, "gsx dev: watch error: %v\n", werr)
			changed, reconcileErr := reconcileWatchState(w, sess, sources, dirty)
			if reconcileErr != nil {
				fmt.Fprintf(stderr, "gsx dev: reconcile after watch error: %v\n", reconcileErr)
				shutdownProcesses()
				return 1
			}
			if changed {
				schedule()
			}
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

// isEnvFile reports whether path is the .env file we read+restart on. Other
// .env.* variants are Vite's domain (it reads + watches them itself).
func isEnvFile(path string) bool { return filepath.Base(path) == ".env" }

// splitArgv splits a flag value into argv on whitespace (the common case; use
// the gsx.toml [dev] array form for exact quoting). Empty ⇒ nil (not set).
func splitArgv(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return strings.Fields(s)
}

// devFlagsFromValues builds the CLI-flag layer for resolveDevConfig from the
// parsed gsx dev flag values. A web/build/run string is whitespace-split via
// splitArgv (nil when empty = "flag not given"). Logging precedence among the
// flags is resolved by resolveDevConfig via logSet/noLog/log; here we only
// translate which flags were set.
func devFlagsFromValues(web, build, run, logFile string, logCache, noWeb, noLog bool) devFlags {
	var logArg []string
	if logFile != "" {
		logArg = []string{logFile}
	}
	return devFlags{
		web:    splitArgv(web),
		build:  splitArgv(build),
		run:    splitArgv(run),
		log:    logArg,
		noWeb:  noWeb,
		noLog:  noLog,
		logSet: logCache || logFile != "" || noLog,
	}
}
