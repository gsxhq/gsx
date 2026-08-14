package gen

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"golang.org/x/mod/modfile"

	"github.com/gsxhq/gsx/internal/codegen"
)

// reopenConsumerModules handles the cross-module half of an authored-Go edit.
// A session module other than an edited one can consume the edited module's
// packages only through a directory replace (or a shared go.work workspace),
// and it type-checks them via its retained external importer — the edited
// module's warm fast path cannot see that consumer. Each linked consumer is
// reopened (discarding all retained analysis so the next Generate reloads
// authoritatively) and every one of its discovered GSX dirs is queued for
// regeneration: the pre-incremental reopen-all behavior, scoped to modules
// that can actually observe the edit.
//
// Linked roots are iterated directly rather than filtered out of a fresh
// groupByModule(dirs) pass. moduleConsumesModule fails a linked root TOWARD
// "consuming" when its own go.mod is unreadable or unparseable — the same
// go.mod groupByModule would fail to attribute any of that root's dirs to,
// dropping them into its noModule return with no group produced. Driving the
// loop from a group lookup would make that promise unreachable: the broken
// root would never surface as a group.root, so neither the reopen attempt
// nor its error result below would ever run, and the "fail toward reopen"
// comment on moduleConsumesModule would describe code with no matching path.
// Attempting s.openModule(root) directly for every linked root keeps the
// error result reachable for exactly that case (openModule reads the same
// go.mod and fails the same way), while a clean root's dirs still come from
// one shared groupByModule(dirs) pass.
//
// Callers must run this only for the Go edits that did NOT already escalate
// via goEditNeedsReopen: a nested module CONTAINED by another session module
// (the shape goEditNeedsReopen escalates) can simultaneously be a
// replace/go.work consumer of a third module, but containment escalation
// already reopens every session module for that edit, making a consumer-link
// reopen of the same root redundant. Containment wins outright; this pass is
// for the sibling shape containment cannot see — two module roots where
// neither contains the other.
func (s *watchSession) reopenConsumerModules(goByRoot map[string][]string) ([]string, []cycleResult) {
	linked := map[string]bool{}
	for root := range s.modules {
		if _, edited := goByRoot[root]; edited {
			continue
		}
		for editedRoot := range goByRoot {
			if s.moduleConsumesModule(root, editedRoot) {
				linked[root] = true
				break
			}
		}
	}
	if len(linked) == 0 {
		return nil, nil
	}
	dirs, err := discoverDirs(s.cfg.paths)
	if err != nil {
		return nil, []cycleResult{{Err: err}}
	}
	groups, _ := groupByModule(dirs)
	dirsByRoot := make(map[string][]string, len(groups))
	for _, group := range groups {
		dirsByRoot[group.root] = group.dirs
	}
	linkedRoots := make([]string, 0, len(linked))
	for root := range linked {
		linkedRoots = append(linkedRoots, root)
	}
	sort.Strings(linkedRoots)
	var affected []string
	var results []cycleResult
	for _, root := range linkedRoots {
		fresh, err := s.openModule(root)
		if err != nil {
			// An unopenable consumer module has no narrower authoritative retry
			// than a full reopen; report the failure unscoped so the dirty set
			// retains depDirty semantics. This is the only place a linked
			// consumer's own broken go.mod can surface — see the func doc.
			results = append(results, cycleResult{Err: fmt.Errorf("reopen replace-linked module %s: %w", root, err)})
			continue
		}
		s.modules[root] = fresh
		affected = append(affected, dirsByRoot[root]...)
	}
	return affected, results
}

// moduleConsumesModule reports whether the module at consumerRoot can import
// packages from the module at editedRoot: a go.mod directory replace pointing
// at editedRoot, or the consumer's effective go.work workspace using
// editedRoot. Unreadable or unparseable module metadata is treated as
// consuming — a spurious reopen costs one reload, while a missed link serves
// stale types until an unrelated go.mod touch.
func (s *watchSession) moduleConsumesModule(consumerRoot, editedRoot string) bool {
	editedRoot = normalizeModuleDir(editedRoot)
	gomod := filepath.Join(consumerRoot, "go.mod")
	data, err := os.ReadFile(gomod)
	if err != nil {
		return true
	}
	file, err := modfile.Parse(gomod, data, nil)
	if err != nil {
		return true
	}
	for _, replace := range file.Replace {
		// A filesystem replacement is exactly the form with no version; a
		// module-version replacement resolves through the proxy/cache and cannot
		// observe a local edit.
		if replace == nil || replace.New.Version != "" {
			continue
		}
		target := replace.New.Path
		if !filepath.IsAbs(target) {
			target = filepath.Join(consumerRoot, target)
		}
		if normalizeModuleDir(target) == editedRoot {
			return true
		}
	}
	return s.workspaceUsesModule(consumerRoot, editedRoot)
}

// workspaceUsesModule reports whether the consumer module's effective go.work
// has a use directive resolving to editedRoot. In workspace mode cmd/go links
// the two modules with no replace directive, so the consumer's external
// importer observes the edited module's source directly. The workspace file is
// the one frozen into the consumer Module's Go command universe (GOWORK may
// name a file far from the module tree, or disable an ancestor go.work with
// "off"); rediscovering it from the filesystem would answer for a different
// build than the one the Module actually type-checks with.
func (s *watchSession) workspaceUsesModule(consumerRoot, editedRoot string) bool {
	editedRoot = normalizeModuleDir(editedRoot)
	var gowork string
	var err error
	if consumer := s.modules[consumerRoot]; consumer != nil {
		gowork, err = consumer.GoWorkFile()
	} else {
		gowork, err = codegen.ResolveGoWorkFile(consumerRoot)
	}
	if err != nil {
		return true // unknown Go command universe: reopen rather than serve stale types
	}
	if gowork == "" {
		return false // authoritative GOWORK is off / no workspace
	}
	data, err := os.ReadFile(gowork)
	if err != nil {
		return true
	}
	work, err := modfile.ParseWork(gowork, data, nil)
	if err != nil {
		return true
	}
	base := filepath.Dir(gowork)
	for _, use := range work.Use {
		target := use.Path
		if !filepath.IsAbs(target) {
			target = filepath.Join(base, target)
		}
		if normalizeModuleDir(target) == editedRoot {
			return true
		}
	}
	return false
}

// normalizeModuleDir resolves path to its symlink-free canonical form so a
// module-directory comparison isn't fooled by two lexically different
// spellings of the same directory. This matters because the two sides of a
// consumer-link comparison can come from different authorities: `go env
// GOWORK` returns cmd/go's symlink-resolved answer (on darwin, TMPDIR sits
// under /var, itself a symlink to /private/var), while a session's own
// module roots and replace targets are tracked lexically (filepath.Abs, no
// symlink resolution). EvalSymlinks requires the path to exist; a replace
// target or module root that doesn't (yet) exist on disk falls back to
// filepath.Clean so a not-yet-created directory still compares consistently
// with itself.
func normalizeModuleDir(path string) string {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return filepath.Clean(path)
	}
	return resolved
}
