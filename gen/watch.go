package gen

import (
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/gsxhq/gsx/internal/attrclass"
	"github.com/gsxhq/gsx/internal/codegen"
)

// watchConfig carries everything runWatch needs, mirroring runGenerate's
// configured options so watch honors the same filters/classifier/minifiers.
type watchConfig struct {
	paths      []string
	format     string // "" (human) or "ndjson"
	stdout     io.Writer
	stderr     io.Writer
	quiet      bool
	verbose    bool
	filterPkgs []string
	aliases    []codegen.FilterAlias
	cls        *attrclass.Classifier
	fm         codegen.FieldMatcher
	cssMin     func(string) (string, error)
	jsMin      func(string) (string, error)
	cssMinify  bool
	jsMinify   bool
}

func runWatch(cfg watchConfig) int { return runWatchWithStop(cfg, nil) }

// runWatchWithStop runs the daemon until `stop` is closed (tests) or a SIGINT/
// SIGTERM arrives (nil stop). Returns a process exit code.
func runWatchWithStop(cfg watchConfig, stop <-chan struct{}) int {
	em := &emitter{ndjson: cfg.format == "ndjson", stdout: cfg.stdout, stderr: cfg.stderr}

	// Short-circuit when there are no watchable dirs (no .gsx files found).
	dirs, err := discoverDirs(cfg.paths)
	if err != nil || len(dirs) == 0 {
		return 0
	}

	sess, startup, err := newWatchSession(cfg)
	if err != nil {
		em.emitError(err)
		return 1
	}

	w, err := fsnotify.NewWatcher()
	if err != nil {
		em.emitError(err)
		return 1
	}
	defer w.Close()

	addWatchTree(w, dirs)
	// Emit start first so tests/consumers can distinguish the initial cold-
	// generate cycles (below) from subsequent regen cycles.
	em.start(sess.root, dirs)
	for _, r := range startup {
		em.cycle(r)
	}

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sig)

	var pending = map[string]bool{} // dirty package dirs
	var depDirty bool               // a .go/go.mod/go.sum changed → rebuild
	var timer *time.Timer
	const debounce = 100 * time.Millisecond
	fire := make(chan struct{}, 1)

	schedule := func() {
		if timer != nil {
			timer.Stop()
		}
		timer = time.AfterFunc(debounce, func() {
			select {
			case fire <- struct{}{}:
			default:
			}
		})
	}

	for {
		select {
		case <-stop:
			return 0
		case <-sig:
			return 0
		case ev := <-w.Events:
			if !watchable(ev.Name) {
				continue
			}
			if isDepFile(ev.Name) {
				depDirty = true
			}
			pending[filepath.Dir(ev.Name)] = true
			// Newly created dirs join the watch set.
			if ev.Op&fsnotify.Create != 0 {
				if fi, statErr := os.Stat(ev.Name); statErr == nil && fi.IsDir() && !excludedDir(ev.Name) {
					_ = w.Add(ev.Name)
				}
			}
			schedule()
		case <-fire:
			if depDirty {
				if rerr := sess.rebuild(); rerr != nil {
					// Skip this cycle: regenerating against a stale resolver
					// would produce output built on the old type graph. Leave
					// depDirty=true and pending intact so the next fire retries.
					em.emitError(rerr)
					continue
				}
				depDirty = false
			}
			for dir := range pending {
				if onlyGeneratedRemains(dir) {
					continue
				}
				em.cycle(sess.regen(dir))
			}
			pending = map[string]bool{}
		case werr := <-w.Errors:
			em.emitError(werr)
		}
	}
}

// addWatchTree adds every non-excluded subdir under each root to the watcher
// (fsnotify is non-recursive).
func addWatchTree(w *fsnotify.Watcher, roots []string) {
	for _, root := range roots {
		filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
			if err != nil || !d.IsDir() {
				return nil
			}
			if excludedDir(p) {
				return filepath.SkipDir
			}
			_ = w.Add(p)
			return nil
		})
	}
}

func watchable(path string) bool {
	base := filepath.Base(path)
	if strings.HasSuffix(base, ".x.go") {
		return false // never react to our own output
	}
	return strings.HasSuffix(base, ".gsx") || isDepFile(path)
}

func isDepFile(path string) bool {
	b := filepath.Base(path)
	if b == "go.mod" || b == "go.sum" {
		return true
	}
	return strings.HasSuffix(b, ".go") && !strings.HasSuffix(b, ".x.go")
}

// excludedDir reports whether a directory should be skipped: a project-local
// build/scratch dir named tmp/dist/node_modules/.git. Only the dir's own name
// is checked — an ancestor named "tmp" (e.g. a project under /private/tmp) must
// NOT exclude its descendants.
func excludedDir(path string) bool {
	switch filepath.Base(path) {
	case "tmp", "dist", "node_modules", ".git":
		return true
	}
	return false
}

// onlyGeneratedRemains reports whether dir has no .gsx (so regenerating is moot,
// e.g. a dir that only held a since-deleted source). Conservative: returns false
// when any .gsx is present.
func onlyGeneratedRemains(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return true
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".gsx") {
			return false
		}
	}
	return true
}
