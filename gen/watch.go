package gen

import (
	"crypto/sha256"
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
	paths       []string
	format      string // "" (human) or "ndjson"
	stdout      io.Writer
	stderr      io.Writer
	quiet       bool
	verbose     bool
	filterPkgs  []string
	aliases     []codegen.FilterAlias
	cls         *attrclass.Classifier
	fm          codegen.FieldMatcher
	cssMin      func(string) (string, error)
	jsMin       func(string) (string, error)
	cssMinify   bool
	jsMinify    bool
	classMerger *codegen.ClassMergerRef
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
	sources := newSourceTracker(dirs)
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
			if !sources.changed(ev.Name) {
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
			results, rerr := sess.regenPending(pending, depDirty)
			if rerr != nil {
				// Regenerating against a stale module would emit output built on
				// the old type graph. Leave depDirty+pending intact and retry on
				// the next fire.
				em.emitError(rerr)
				continue
			}
			for _, r := range results {
				em.cycle(r)
			}
			pending = map[string]bool{}
			depDirty = false
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

type sourceTracker struct {
	files map[string]sourceSnapshot
}

type sourceSnapshot struct {
	hash [32]byte
}

func newSourceTracker(roots []string) *sourceTracker {
	t := &sourceTracker{files: map[string]sourceSnapshot{}}
	for _, root := range roots {
		filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() {
				if excludedDir(p) {
					return filepath.SkipDir
				}
				return nil
			}
			if snap, ok := readSourceSnapshot(p); ok {
				t.files[filepath.Clean(p)] = snap
			}
			return nil
		})
	}
	return t
}

func (t *sourceTracker) changed(path string) bool {
	if t == nil {
		return true
	}
	path = filepath.Clean(path)
	snap, ok := readSourceSnapshot(path)
	prev, had := t.files[path]
	if !ok {
		if had {
			delete(t.files, path)
			return true
		}
		return false
	}
	if !had || snap.hash != prev.hash {
		t.files[path] = snap
		return true
	}
	return false
}

func readSourceSnapshot(path string) (sourceSnapshot, bool) {
	if !watchable(path) {
		return sourceSnapshot{}, false
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return sourceSnapshot{}, false
	}
	return sourceSnapshot{hash: sha256.Sum256(b)}, true
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
