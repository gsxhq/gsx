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
	renderers   []codegen.RendererAlias
	cls         *attrclass.Classifier
	fm          codegen.FieldMatcher
	cssMin      func(string) (string, error)
	jsMin       func(string) (string, error)
	cssMinify   bool
	jsMinify    bool
	classMerger *codegen.ClassMergerRef
}

func runWatch(cfg watchConfig) int { return runWatchWithStop(cfg, nil) }

// armedWatchSession is the shared watch/dev startup boundary. prepare resolves
// roots without reading GSX membership; armWatchSession then registers every
// current directory and captures the source-event baseline. Only after this
// value exists may initialGenerate snapshot and generate authored sources.
type armedWatchSession struct {
	session *watchSession
	watcher *fsnotify.Watcher
	sources *sourceTracker
}

func armWatchSession(cfg watchConfig) (*armedWatchSession, error) {
	session, err := prepareWatchSession(cfg)
	if err != nil {
		return nil, err
	}
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	if err := addWatchTree(watcher, session.watchRoots); err != nil {
		_ = watcher.Close()
		return nil, err
	}
	return &armedWatchSession{
		session: session,
		watcher: watcher,
		sources: newSourceTracker(session.watchRoots),
	}, nil
}

func (session *armedWatchSession) Close() error {
	if session == nil || session.watcher == nil {
		return nil
	}
	err := session.watcher.Close()
	session.watcher = nil
	return err
}

// runWatchWithStop runs the daemon until `stop` is closed (tests) or a SIGINT/
// SIGTERM arrives (nil stop). Returns a process exit code.
func runWatchWithStop(cfg watchConfig, stop <-chan struct{}) int {
	em := &emitter{ndjson: cfg.format == "ndjson", stdout: cfg.stdout, stderr: cfg.stderr}

	armed, err := armWatchSession(cfg)
	if err != nil {
		em.emitError(err)
		return 1
	}
	defer armed.Close()
	sess := armed.session
	w := armed.watcher
	sources := armed.sources
	// Start means observation is armed. Initial generation follows while source
	// events queue on w, so an edit in that window cannot disappear.
	em.start(sess.root, sess.watchRoots)
	startup, err := sess.initialGenerate()
	if err != nil {
		em.emitError(err)
		return 1
	}
	for _, r := range startup {
		em.cycle(r)
	}

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sig)

	dirty := newWatchDirtySet()
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
			changed, eventErr := applyWatchEvent(w, ev, sources, dirty.dirs, &dirty.depDirty)
			if eventErr != nil {
				em.emitError(eventErr)
				continue
			}
			if changed {
				schedule()
			}
		case <-fire:
			results, _, rerr := dirty.regenerate(sess.regenPending)
			if rerr != nil {
				// Regenerating against a stale module would emit output built on
				// the old type graph. Leave the dirty transaction intact and retry on
				// the next fire.
				em.emitError(rerr)
				continue
			}
			for _, r := range results {
				em.cycle(r)
			}
		case werr := <-w.Errors:
			em.emitError(werr)
		}
	}
}

// addWatchTree adds every non-excluded subdir under each root to the watcher
// (fsnotify is non-recursive). The root itself is always honored because it was
// explicitly selected; exclusion applies only to descendants.
func addWatchTree(w *fsnotify.Watcher, roots []string) error {
	for _, root := range roots {
		err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !d.IsDir() {
				return nil
			}
			if p != root && excludedDir(p) {
				return filepath.SkipDir
			}
			return w.Add(p)
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// applyWatchEvent handles structural directory creation before filtering file
// names. A newly created subtree can already contain source files by the time
// fsnotify delivers its directory event, so it is first made recursively
// watchable and then inventoried through the same exact source classifier used
// for ordinary file events.
func applyWatchEvent(w *fsnotify.Watcher, event fsnotify.Event, sources *sourceTracker, pending map[string]bool, depDirty *bool) (bool, error) {
	if event.Op&fsnotify.Create != 0 {
		info, err := os.Stat(event.Name)
		if err == nil && info.IsDir() {
			if excludedDir(event.Name) {
				return false, nil
			}
			if err := addWatchTree(w, []string{event.Name}); err != nil {
				return false, err
			}
			return queueWatchTree(event.Name, sources, pending, depDirty)
		}
		if err != nil && !os.IsNotExist(err) {
			return false, err
		}
	}
	return queueWatchSource(event.Name, sources, pending, depDirty), nil
}

func queueWatchTree(root string, sources *sourceTracker, pending map[string]bool, depDirty *bool) (bool, error) {
	changed := false
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if path != root && excludedDir(path) {
				return filepath.SkipDir
			}
			return nil
		}
		if queueWatchSource(path, sources, pending, depDirty) {
			changed = true
		}
		return nil
	})
	return changed, err
}

func queueWatchSource(path string, sources *sourceTracker, pending map[string]bool, depDirty *bool) bool {
	if !watchable(path) || !sources.changed(path) {
		return false
	}
	if isDepFile(path) {
		*depDirty = true
	}
	pending[filepath.Dir(path)] = true
	return true
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
				if p != root && excludedDir(p) {
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
	return strings.HasSuffix(base, ".gsx") || isDepFile(path)
}

func isDepFile(path string) bool {
	b := filepath.Base(path)
	if b == "go.mod" || b == "go.sum" {
		return true
	}
	if !strings.HasSuffix(b, ".go") {
		return false
	}
	return !pairedGeneratedOutput(path)
}

// pairedGeneratedOutput recognizes only the exact output path reserved by an
// existing same-base GSX source. An unpaired *.x.go file is authored Go and is
// therefore part of the watched dependency surface.
func pairedGeneratedOutput(path string) bool {
	if !strings.HasSuffix(filepath.Base(path), ".x.go") {
		return false
	}
	gsxPath := strings.TrimSuffix(path, ".x.go") + ".gsx"
	info, err := os.Stat(gsxPath)
	return err == nil && !info.IsDir()
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
