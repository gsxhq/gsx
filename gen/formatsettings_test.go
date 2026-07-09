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
			w, tw := formatSettingsFor(".", filepath.Join(root, "a.gsx"), newEditorConfigResolver())
			if w != tt.wantWidth || tw != tt.wantTabWidth {
				t.Errorf("got width=%d tabWidth=%d, want width=%d tabWidth=%d", w, tw, tt.wantWidth, tt.wantTabWidth)
			}
		})
	}
}
