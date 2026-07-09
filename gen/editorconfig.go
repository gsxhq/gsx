package gen

import (
	"strconv"
	"sync"

	"github.com/editorconfig/editorconfig-core-go/v2"
)

// editorSettings holds the .editorconfig values gsx honors, resolved for one
// file. A zero field means the key was absent (or unusable), and the caller
// falls through to the next configuration layer.
//
// indent_style is deliberately NOT honored. gofmt emits tabs for Go, always;
// satisfying `indent_style = space` would mean re-indenting gofmt's output,
// which is the one thing every layout rule in gsx fmt is built to avoid.
type editorSettings struct {
	tabWidth   int // from tab_width, or indent_size per the EditorConfig spec
	printWidth int // from max_line_length; "off" resolves to 0 (unset)
}

// editorConfigResolver resolves .editorconfig per file. Resolution walks up to
// the nearest `root = true`, so it is per-file, not per-directory: sections are
// filename globs ([*.gsx]). The library's CachedParser memoizes each
// .editorconfig file it reads, which is what keeps `gsx fmt ./...` from
// re-reading the same file once per source file.
type editorConfigResolver struct {
	mu  sync.Mutex
	cfg *editorconfig.Config
}

func newEditorConfigResolver() *editorConfigResolver {
	return &editorConfigResolver{cfg: &editorconfig.Config{Parser: editorconfig.NewCachedParser()}}
}

// settingsFor never fails: a missing, unreadable, or malformed .editorconfig
// yields the zero editorSettings, exactly like formatSettingsFor's own
// discovery/decode fallbacks. gsx fmt must not die on someone else's config.
func (r *editorConfigResolver) settingsFor(path string) editorSettings {
	r.mu.Lock()
	def, err := r.cfg.Load(path)
	r.mu.Unlock()
	if err != nil || def == nil {
		return editorSettings{}
	}
	s := editorSettings{tabWidth: def.TabWidth}
	if s.tabWidth < 0 {
		s.tabWidth = 0
	}
	// max_line_length lives in Raw; "off" means no limit, which gsx expresses as
	// "unset" so the caller's own default applies.
	if raw, ok := def.Raw["max_line_length"]; ok && raw != "off" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			s.printWidth = n
		}
	}
	return s
}
