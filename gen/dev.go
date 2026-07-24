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
	var tdUpstream, tdHealth string
	if td != nil {
		tdUpstream, tdHealth = td.Upstream, td.Health
	}
	origin, healthURL, goPort, err := resolveUpstream(tdUpstream, tdHealth, env)
	if err != nil {
		fmt.Fprintf(stderr, "gsx dev: %v\n", err)
		return 1
	}

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

	status := devStatus{Phase: "idle", PhaseSince: time.Now(), Server: serverStat{Port: goPort, Upstream: origin}, FrontDoor: frontStat{State: "external"}}
	if dc.web != nil {
		status.FrontDoor.State = "up"
	}
	postStatus := func() { post(statusEvent(status)) }
	// setPhase transitions status.Phase and stamps PhaseSince to now, then
	// posts immediately — every transition (save → generating → building →
	// starting → idle) is observable by an open panel, not just cycle ends.
	// Call sites that mutate other status fields (LastCycle, Server.Healthy)
	// AFTER the phase settles keep their own trailing postStatus() so that
	// final data reaches the panel too; setPhase's own post is not redundant
	// there; where nothing changes after, the trailing postStatus() is
	// dropped as it would just repeat the same payload.
	setPhase := func(phase string) {
		status.Phase = phase
		status.PhaseSince = time.Now()
		postStatus()
	}

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
		// The backend log's location rides the same env bus as the upstream
		// origin (GSX_DEV_UPSTREAM): resolved once, absolute, so the plugin
		// never guesses paths relative to a cwd it doesn't share. Absent when
		// logging is off — the plugin treats that as "no log to read".
		// filepath.Abs matches os.Create's resolution of the same value at
		// startup: a [dev].log config value already arrives workDir-anchored
		// (see resolveDevConfig), so Abs is a no-op there; a --log-file/default
		// value is still resolved against this process's cwd, same as os.Create
		// — either way the env names the file actually being written.
		childEnv := append(slices.Clone(env), "GSX_DEV_TOKEN="+devToken, "GSX_DEV_UPSTREAM="+origin)
		if dc.logPath != "" {
			if abs, aerr := filepath.Abs(dc.logPath); aerr == nil {
				childEnv = append(childEnv, "GSX_DEV_LOG="+abs)
			}
		}
		spawn := func() (*exec.Cmd, error) {
			c := exec.Command(dc.web[0], dc.web[1:]...)
			c.Dir, c.Stdout, c.Stderr = workDir, mkWriter("vite"), mkWriter("vite")
			c.Env = slices.Clone(childEnv)
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
	// contactCh fires exactly once, the moment pollCommands' first successful
	// response proves the front door is actually serving our plugin (its gate
	// opens optimistically on process start — see frontDoor — well before
	// vite/npm has finished booting on a warm start). The status-cache-driven
	// dev panel would otherwise wait forever for a cycle that may never come
	// on an idle project; this re-posts the CURRENT status once contact is
	// made, deliberately fresh rather than a longer retry of the possibly-stale
	// startup post.
	contactCh := make(chan struct{}, 1)
	onContact := func() {
		select {
		case contactCh <- struct{}{}:
		default: // already signaled; onContact only ever fires once anyway
		}
	}
	go pollCommands(ctx, viteURL, devToken, webUp, cmds, onContact)

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
	initCycleStart := time.Now()
	if startOK {
		setPhase("building")
		if out, err := srv.rebuild(ctx); err != nil {
			post(buildErrorEvent(buildFailureMessage(out, err)))
			startOK = false
		} else {
			setPhase("starting")
			if waitHealthy(ctx, healthURL, 10*time.Second) {
				status.Server.Healthy = true
				reload()
			}
		}
	}
	// setPhase("idle") posts once with the phase settled; Server.Healthy (set
	// above, before this point) and LastCycle (set below, after) both change
	// around it, so the explicit postStatus() below carrying LastCycle is
	// kept — it is the one the panel needs, not a redundant repeat.
	setPhase("idle")
	status.LastCycle = &cycleStat{OK: startOK, At: time.Now(), DurationMs: time.Since(initCycleStart).Milliseconds()}
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
		cycleStart := time.Now()
		setPhase("generating")
		results, goChanged, rerr := dirty.regenerate(sess.regenPending)
		if rerr != nil {
			fmt.Fprintf(serverOut, "regen failed: %v\n", rerr)
			post(buildErrorEvent("regen failed: " + rerr.Error()))
			overlayUp = true
			// setPhase("idle") posts once; LastCycle (below) changes after it,
			// so the explicit postStatus() carrying it is kept, not redundant.
			setPhase("idle")
			status.LastCycle = &cycleStat{OK: false, Errors: 1, At: time.Now(), DurationMs: time.Since(cycleStart).Milliseconds()}
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
			// setPhase("idle") posts once; LastCycle (below) changes after it,
			// so the explicit postStatus() carrying it is kept, not redundant.
			setPhase("idle")
			status.LastCycle = &cycleStat{OK: false, Errors: errs, At: time.Now(), DurationMs: time.Since(cycleStart).Milliseconds()}
			postStatus()
			return // keep last-good server up; overlay shows the error
		}
		// Successful cycle. Rebuild when code changed (or the panel forced it);
		// reload the browser if we rebuilt OR we're recovering from a shown
		// error overlay — the latter must clear even when nothing was written
		// (fixed .gsx → identical .x.go).
		doReload := overlayUp || force
		if goChanged || wrote || force {
			setPhase("building")
			if out, err := srv.rebuild(ctx); err != nil {
				post(buildErrorEvent(buildFailureMessage(out, err)))
				overlayUp = true
				// setPhase("idle") posts once; Server.Healthy and LastCycle
				// (both below) change after it, so the explicit postStatus()
				// carrying them is kept, not redundant.
				setPhase("idle")
				status.Server.Healthy = false
				status.LastCycle = &cycleStat{OK: false, Errors: 1, At: time.Now(), DurationMs: time.Since(cycleStart).Milliseconds()}
				postStatus()
				return
			}
			setPhase("starting")
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
		// setPhase("idle") posts once; LastCycle (below) changes after it, so
		// the explicit postStatus() carrying it is kept, not redundant.
		setPhase("idle")
		status.LastCycle = &cycleStat{OK: true, Errors: 0, At: time.Now(), DurationMs: time.Since(cycleStart).Milliseconds()}
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
				// setPhase posts on its own; Server.Healthy settles fully
				// BEFORE the trailing setPhase("idle"), so that second
				// setPhase's own post already carries it — no separate
				// trailing postStatus() needed here, unlike cycle()'s
				// terminal paths where LastCycle changes after the phase set.
				setPhase("starting")
				if err := srv.restartNoBuild(); err == nil && waitHealthy(ctx, healthURL, 10*time.Second) {
					status.Server.Healthy = true
					reload()
				} else {
					status.Server.Healthy = false
				}
				setPhase("idle")
			default:
				fmt.Fprintf(stderr, "gsx dev: unknown panel command %q\n", c)
			}

		case fs := <-fdCh:
			status.FrontDoor = fs
			postStatus()

		case <-contactCh:
			// First proof the front door is actually serving our plugin
			// (see contactCh's declaration above): re-post the current
			// status so a panel that opened before any cycle ran no longer
			// waits forever on an idle project.
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
				newEnv := mergeDotEnv(os.Environ(), loadDotEnv(workDir))
				resolvedEnv, newViteURL, envWarning, envErr := resolveViteDevEnv(newEnv, dc.host)
				if envErr != nil {
					// A broken .env edit (e.g. an explicit VITE_PORT that's now
					// in use) must not crash a running dev loop OR corrupt
					// env/viteURL: resolveViteDevEnv's failure return is the
					// zero value for both, and env/viteURL are read by every
					// later post()/reload()/pollCommands call in this
					// function — assigning them here would silently and
					// permanently break every future post (postBest treats an
					// empty base URL as a same-origin path and no-ops). Keep
					// both exactly as they were, log + overlay the error
					// (mirrors the upErr handling just below: both are
					// resolution failures from the same .env-fire path, and
					// the browser should learn of either from the overlay,
					// not just the terminal), and let the developer retry.
					fmt.Fprintf(stderr, "gsx dev: %v\n", envErr)
					post(buildErrorEvent(envErr.Error()))
					overlayUp = true
					continue
				}
				env, viteURL = resolvedEnv, newViteURL
				if envWarning != "" {
					fmt.Fprintf(stderr, "gsx dev: %s\n", envWarning)
				}
				newOrigin, newHealthURL, newPort, upErr := resolveUpstream(tdUpstream, tdHealth, env)
				if upErr != nil {
					// A broken [dev].upstream (e.g. a now-unset ${VAR}, or an
					// env edit that produces a bare trailing ":") must not crash
					// a running dev loop or corrupt the last-known-good probe
					// target: log + overlay it and keep everything (server env,
					// healthURL, status) exactly as it was — mirrors the
					// envErr handling just above.
					fmt.Fprintf(stderr, "gsx dev: %v\n", upErr)
					post(buildErrorEvent(upErr.Error()))
					overlayUp = true
					continue
				}
				origin, healthURL, goPort = newOrigin, newHealthURL, newPort
				// Vite reads .env itself (loadEnv + native .env watch), so only the Go server is restarted here.
				srv.env = env
				status.Server.Port = goPort
				status.Server.Upstream = origin
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
