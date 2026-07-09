package gen

import (
	"os"
	"path/filepath"
	"testing"
)

func writeTree(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for name, body := range files {
		p := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestEditorConfigTabWidth(t *testing.T) {
	root := writeTree(t, map[string]string{
		".editorconfig": "root = true\n\n[*.gsx]\ntab_width = 3\n",
		"ui/a.gsx":      "",
	})
	got := newEditorConfigResolver().settingsFor(filepath.Join(root, "ui/a.gsx"))
	if got.tabWidth != 3 {
		t.Errorf("tabWidth = %d, want 3", got.tabWidth)
	}
}

// Per the EditorConfig spec, tab_width defaults to indent_size.
func TestEditorConfigIndentSizeFallback(t *testing.T) {
	root := writeTree(t, map[string]string{
		".editorconfig": "root = true\n\n[*.gsx]\nindent_style = tab\nindent_size = 4\n",
		"ui/a.gsx":      "",
	})
	if got := newEditorConfigResolver().settingsFor(filepath.Join(root, "ui/a.gsx")); got.tabWidth != 4 {
		t.Errorf("tabWidth = %d, want 4 (from indent_size)", got.tabWidth)
	}
}

// A [*] section must not leak into .gsx when a [*.gsx] section overrides it.
func TestEditorConfigGlobSpecificity(t *testing.T) {
	root := writeTree(t, map[string]string{
		".editorconfig": "root = true\n\n[*]\ntab_width = 8\n\n[*.gsx]\ntab_width = 2\n",
		"ui/a.gsx":      "",
	})
	if got := newEditorConfigResolver().settingsFor(filepath.Join(root, "ui/a.gsx")); got.tabWidth != 2 {
		t.Errorf("tabWidth = %d, want 2", got.tabWidth)
	}
}

func TestEditorConfigMaxLineLength(t *testing.T) {
	root := writeTree(t, map[string]string{
		".editorconfig": "root = true\n\n[*.gsx]\nmax_line_length = 100\n",
		"ui/a.gsx":      "",
	})
	if got := newEditorConfigResolver().settingsFor(filepath.Join(root, "ui/a.gsx")); got.printWidth != 100 {
		t.Errorf("printWidth = %d, want 100", got.printWidth)
	}
}

// "off" means no limit. gsx has no unbounded width, so it means "unset" and the
// caller falls through to its own default.
func TestEditorConfigMaxLineLengthOff(t *testing.T) {
	root := writeTree(t, map[string]string{
		".editorconfig": "root = true\n\n[*.gsx]\nmax_line_length = off\n",
		"ui/a.gsx":      "",
	})
	if got := newEditorConfigResolver().settingsFor(filepath.Join(root, "ui/a.gsx")); got.printWidth != 0 {
		t.Errorf("printWidth = %d, want 0 (unset)", got.printWidth)
	}
}

func TestEditorConfigAbsentIsUnset(t *testing.T) {
	root := writeTree(t, map[string]string{"ui/a.gsx": ""})
	got := newEditorConfigResolver().settingsFor(filepath.Join(root, "ui/a.gsx"))
	if got.tabWidth != 0 || got.printWidth != 0 {
		t.Errorf("no .editorconfig: got %+v, want zero", got)
	}
}

// A malformed .editorconfig must never fail a format run.
func TestEditorConfigMalformedIsUnset(t *testing.T) {
	root := writeTree(t, map[string]string{
		".editorconfig": "\x00\x01 not ini [[[\n",
		"ui/a.gsx":      "",
	})
	got := newEditorConfigResolver().settingsFor(filepath.Join(root, "ui/a.gsx"))
	if got.tabWidth != 0 || got.printWidth != 0 {
		t.Errorf("malformed: got %+v, want zero", got)
	}
}
