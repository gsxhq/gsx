package gen

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
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
	sources, err := newSourceTracker(session.watchRoots, session.requestedRoots)
	if err != nil {
		_ = watcher.Close()
		return nil, fmt.Errorf("inventory watched sources: %w", err)
	}
	if err := addRequestedRootSentinels(watcher, sources); err != nil {
		_ = watcher.Close()
		return nil, err
	}
	return &armedWatchSession{
		session: session,
		watcher: watcher,
		sources: sources,
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
	dirty := newWatchDirtySet()
	startup, err := sess.initialGenerate()
	if err != nil {
		em.emitError(err)
		return 1
	}
	dirty.retainOperational(startup)
	for _, r := range publishableStartupResults(startup) {
		em.cycle(r)
	}

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sig)

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
		case ev, ok := <-w.Events:
			if !ok {
				em.emitError(fsnotify.ErrClosed)
				return 1
			}
			changed, eventErr := applyWatchEvent(w, ev, sources, dirty.dirs, &dirty.depDirty)
			if eventErr != nil {
				em.emitError(eventErr)
				return 1
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
		case werr, ok := <-w.Errors:
			if !ok || errors.Is(werr, fsnotify.ErrClosed) {
				if werr == nil {
					werr = fsnotify.ErrClosed
				}
				em.emitError(werr)
				return 1
			}
			em.emitError(werr)
			changed, reconcileErr := reconcileWatchState(w, sess, sources, dirty)
			if reconcileErr != nil {
				em.emitError(fmt.Errorf("reconcile after watch error: %w", reconcileErr))
				return 1
			}
			if changed {
				schedule()
			}
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

// addRequestedRootSentinels watches the complete structural chain from each
// requested root to the broader observation tree that contains it. When an
// excluded ancestor is deleted its own watch disappears; the next surviving
// ancestor still observes recreation and can re-arm only the requested branch.
func addRequestedRootSentinels(w *fsnotify.Watcher, sources *sourceTracker) error {
	paths := make(map[string]bool, len(sources.requestedBranches)+len(sources.sentinelParents))
	for path := range sources.requestedBranches {
		paths[path] = true
	}
	for path := range sources.sentinelParents {
		paths[path] = true
	}
	ordered := make([]string, 0, len(paths))
	for path := range paths {
		ordered = append(ordered, path)
	}
	sort.Strings(ordered)
	for _, path := range ordered {
		info, err := os.Stat(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return err
		}
		if !info.IsDir() {
			continue
		}
		if err := w.Add(path); err != nil {
			return fmt.Errorf("watch requested-root sentinel %s: %w", path, err)
		}
	}
	return nil
}

// reconcileWatchState repairs both halves of watch authority after event loss:
// native registrations are re-armed for every currently existing tree, and an
// exact disk inventory is diffed against the event-derived source baseline.
func reconcileWatchState(w *fsnotify.Watcher, session *watchSession, sources *sourceTracker, dirty *watchDirtySet) (bool, error) {
	for _, root := range session.watchRoots {
		if _, err := os.Stat(root); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return false, err
		}
		if err := addWatchTree(w, []string{root}); err != nil {
			return false, err
		}
	}
	if err := addRequestedRootSentinels(w, sources); err != nil {
		return false, err
	}
	return sources.reconcile(session.watchRoots, dirty.dirs, &dirty.depDirty)
}

// applyWatchEvent handles structural directory creation before filtering file
// names. A newly created subtree can already contain source files by the time
// fsnotify delivers its directory event, so it is first made recursively
// watchable and then inventoried through the same exact source classifier used
// for ordinary file events.
func applyWatchEvent(w *fsnotify.Watcher, event fsnotify.Event, sources *sourceTracker, pending map[string]bool, depDirty *bool) (bool, error) {
	if !sources.observed(event.Name) {
		return false, nil
	}
	if event.Op&(fsnotify.Remove|fsnotify.Rename) != 0 && sources.requestedStructure(event.Name) {
		return sources.removeTree(event.Name, pending, depDirty), nil
	}
	if event.Op&fsnotify.Create != 0 {
		info, err := os.Stat(event.Name)
		if err == nil && info.IsDir() {
			if sources.requestedStructure(event.Name) {
				return queueRequestedBranches(w, event.Name, sources, pending, depDirty)
			}
			if excludedDir(event.Name) && !sources.explicitRoot(event.Name) {
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

func queueRequestedBranches(w *fsnotify.Watcher, created string, sources *sourceTracker, pending map[string]bool, depDirty *bool) (bool, error) {
	if err := addRequestedRootSentinels(w, sources); err != nil {
		return false, err
	}
	changed := false
	for _, root := range sources.requestedBranches[filepath.Clean(created)] {
		info, err := os.Stat(root)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return false, err
		}
		if !info.IsDir() {
			continue
		}
		if err := addWatchTree(w, []string{root}); err != nil {
			return false, err
		}
		branchChanged, err := queueWatchTree(root, sources, pending, depDirty)
		if err != nil {
			return false, err
		}
		changed = changed || branchChanged
	}
	return changed, nil
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
	files             map[string]sourceSnapshot
	roots             []string
	explicitRoots     map[string]bool
	requestedBranches map[string][]string
	sentinelParents   map[string]bool
}

type sourceSnapshot struct {
	hash [32]byte
}

func newSourceTracker(roots, explicitRoots []string) (*sourceTracker, error) {
	t := &sourceTracker{
		roots:             append([]string(nil), roots...),
		explicitRoots:     map[string]bool{},
		requestedBranches: map[string][]string{},
		sentinelParents:   map[string]bool{},
	}
	for _, root := range explicitRoots {
		root = filepath.Clean(root)
		t.explicitRoots[root] = true
		anchor := nearestRequestedAnchor(root, roots)
		if anchor == "" {
			t.requestedBranches[root] = append(t.requestedBranches[root], root)
			t.sentinelParents[filepath.Dir(root)] = true
			continue
		}
		for path := root; path != anchor; path = filepath.Dir(path) {
			t.requestedBranches[path] = append(t.requestedBranches[path], root)
		}
	}
	files, err := sourceInventory(roots)
	if err != nil {
		return nil, err
	}
	t.files = files
	return t, nil
}

func nearestRequestedAnchor(requested string, roots []string) string {
	anchor := ""
	for _, root := range roots {
		root = filepath.Clean(root)
		if root == requested || !pathWithinTree(root, requested) {
			continue
		}
		if len(root) > len(anchor) {
			anchor = root
		}
	}
	return anchor
}

func sourceInventory(roots []string) (map[string]sourceSnapshot, error) {
	files := map[string]sourceSnapshot{}
	for _, root := range roots {
		err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
			if err != nil {
				if p == root && os.IsNotExist(err) {
					return nil
				}
				return err
			}
			if d.IsDir() {
				if p != root && excludedDir(p) {
					return filepath.SkipDir
				}
				return nil
			}
			if snap, ok := readSourceSnapshot(p); ok {
				files[filepath.Clean(p)] = snap
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return files, nil
}

// reconcile replaces the event-derived baseline with an authoritative walk and
// queues the exact additions, removals, and byte changes. It is used whenever
// the watcher reports that event delivery may be incomplete.
func (t *sourceTracker) reconcile(roots []string, pending map[string]bool, depDirty *bool) (bool, error) {
	files, err := sourceInventory(roots)
	if err != nil {
		return false, err
	}
	paths := make(map[string]bool, len(t.files)+len(files))
	for path := range t.files {
		paths[path] = true
	}
	for path := range files {
		paths[path] = true
	}
	changed := false
	for path := range paths {
		before, had := t.files[path]
		after, has := files[path]
		if had == has && (!had || before == after) {
			continue
		}
		changed = true
		pending[filepath.Dir(path)] = true
		if isDepFile(path) {
			*depDirty = true
		}
	}
	t.files = files
	return changed, nil
}

func (t *sourceTracker) explicitRoot(path string) bool {
	return t != nil && t.explicitRoots[filepath.Clean(path)]
}

func (t *sourceTracker) requestedStructure(path string) bool {
	return t != nil && len(t.requestedBranches[filepath.Clean(path)]) != 0
}

func (t *sourceTracker) removeTree(root string, pending map[string]bool, depDirty *bool) bool {
	root = filepath.Clean(root)
	changed := false
	for path := range t.files {
		if !pathWithinTree(root, path) {
			continue
		}
		delete(t.files, path)
		pending[filepath.Dir(path)] = true
		if isDepFile(path) {
			*depDirty = true
		}
		changed = true
	}
	return changed
}

func (t *sourceTracker) observed(path string) bool {
	if t == nil {
		return true
	}
	path = filepath.Clean(path)
	for _, root := range t.roots {
		if pathWithinTree(root, path) {
			return true
		}
	}
	return false
}

func pathWithinTree(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
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
