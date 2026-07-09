package gen

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/gsxhq/gsx/internal/pretty"
)

func TestFormatSettingsPrecedence(t *testing.T) {
	tests := []struct {
		name         string
		files        map[string]string
		wantWidth    int
		wantTabWidth int
	}{{
		name:         "nothing configured: built-in defaults",
		files:        map[string]string{"a.gsx": ""},
		wantWidth:    80,
		wantTabWidth: pretty.DefaultTabWidth,
	}, {
		name: "editorconfig alone",
		files: map[string]string{
			".editorconfig": "root = true\n\n[*.gsx]\ntab_width = 3\nmax_line_length = 100\n",
			"a.gsx":         "",
		},
		wantWidth:    100,
		wantTabWidth: 3,
	}, {
		name: "gsx.toml overrides editorconfig",
		files: map[string]string{
			".editorconfig": "root = true\n\n[*.gsx]\ntab_width = 3\nmax_line_length = 100\n",
			"gsx.toml":      "[formatter]\nprint_width = 120\ntab_width = 8\n",
			"a.gsx":         "",
		},
		wantWidth:    120,
		wantTabWidth: 8,
	}, {
		name: "gsx.toml partial: unset keys still fall through to editorconfig",
		files: map[string]string{
			".editorconfig": "root = true\n\n[*.gsx]\ntab_width = 3\nmax_line_length = 100\n",
			"gsx.toml":      "[formatter]\ntab_width = 8\n",
			"a.gsx":         "",
		},
		wantWidth:    100, // from .editorconfig
		wantTabWidth: 8,   // from gsx.toml
	}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := writeTree(t, tt.files)
			cwd, _ := os.Getwd()
			t.Cleanup(func() { _ = os.Chdir(cwd) })
			if err := os.Chdir(root); err != nil {
				t.Fatal(err)
			}
			fs := formatSettingsFor(".", filepath.Join(root, "a.gsx"), newEditorConfigResolver())
			if fs.Width != tt.wantWidth || fs.TabWidth != tt.wantTabWidth {
				t.Errorf("got width=%d tabWidth=%d, want width=%d tabWidth=%d", fs.Width, fs.TabWidth, tt.wantWidth, tt.wantTabWidth)
			}
		})
	}
}

// TestFormatSettingsCachesConfigDecodePerDir proves formatSettingsFor decodes
// a directory's gsx.toml ONCE no matter how many files in that directory are
// resolved against the same *editorConfigResolver — the regression this
// guards against decoded gsx.toml once PER FILE (discoverConfig+loadConfig
// were called inline, with no cache), so N files in one directory cost N full
// TOML decodes instead of 1.
func TestFormatSettingsCachesConfigDecodePerDir(t *testing.T) {
	root := writeTree(t, map[string]string{
		"gsx.toml": "[formatter]\nprint_width = 111\n",
		"a.gsx":    "",
		"b.gsx":    "",
		"c.gsx":    "",
	})
	ec := newEditorConfigResolver()
	for _, name := range []string{"a.gsx", "b.gsx", "c.gsx"} {
		fs := formatSettingsFor(root, filepath.Join(root, name), ec)
		if fs.Width != 111 {
			t.Fatalf("%s: width = %d, want 111", name, fs.Width)
		}
	}
	if got := len(ec.configByPath); got != 1 {
		t.Errorf("configByPath has %d entries after 3 files in one directory, want 1 (gsx.toml decoded once, not once per file)", got)
	}
	if got := len(ec.cfgPathByDir); got != 1 {
		t.Errorf("cfgPathByDir has %d entries, want 1 (one directory resolved)", got)
	}
}

// TestFormatSettingsCachesConfigDecodeAcrossDirs proves that directories which
// discover the SAME ancestor gsx.toml share one decode, keyed by the resolved
// cfgPath rather than by directory: keying only by directory (a plausible but
// weaker fix) would still decode once per directory even when they all
// resolve to the same file.
func TestFormatSettingsCachesConfigDecodeAcrossDirs(t *testing.T) {
	root := writeTree(t, map[string]string{
		"go.mod":   "module ex\n\ngo 1.26.1\n",
		"gsx.toml": "[formatter]\nprint_width = 111\n",
		"a/x.gsx":  "",
		"b/y.gsx":  "",
	})
	ec := newEditorConfigResolver()
	dirA := filepath.Join(root, "a")
	dirB := filepath.Join(root, "b")
	fs1 := formatSettingsFor(dirA, filepath.Join(dirA, "x.gsx"), ec)
	fs2 := formatSettingsFor(dirB, filepath.Join(dirB, "y.gsx"), ec)
	if fs1.Width != 111 || fs2.Width != 111 {
		t.Fatalf("width = %d, %d, want 111, 111", fs1.Width, fs2.Width)
	}
	if got := len(ec.cfgPathByDir); got != 2 {
		t.Errorf("cfgPathByDir has %d entries, want 2 (one per directory discovered)", got)
	}
	if got := len(ec.configByPath); got != 1 {
		t.Errorf("configByPath has %d entries, want 1 (both directories share the same ancestor gsx.toml, decoded once)", got)
	}
}
