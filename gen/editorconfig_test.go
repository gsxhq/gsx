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

// EditorConfig has no glob-specificity concept: per the upstream doc comment,
// "the last section has preference over the priors". Whichever section is
// written last in the file wins, regardless of how specific its glob is. Both
// orderings are asserted here so a "more specific glob wins" implementation
// would fail this test. Do NOT "fix" fixture B to expect 2 — the less
// specific [*] section winning is the correct, spec-mandated behavior because
// it comes last.
func TestEditorConfigLastSectionWins(t *testing.T) {
	root := writeTree(t, map[string]string{
		".editorconfig": "root = true\n\n[*]\ntab_width = 8\n\n[*.gsx]\ntab_width = 2\n",
		"ui/a.gsx":      "",
	})
	if got := newEditorConfigResolver().settingsFor(filepath.Join(root, "ui/a.gsx")); got.tabWidth != 2 {
		t.Errorf("[*] then [*.gsx]: tabWidth = %d, want 2 (last section, [*.gsx], wins)", got.tabWidth)
	}

	root2 := writeTree(t, map[string]string{
		".editorconfig": "root = true\n\n[*.gsx]\ntab_width = 2\n\n[*]\ntab_width = 8\n",
		"ui/a.gsx":      "",
	})
	if got := newEditorConfigResolver().settingsFor(filepath.Join(root2, "ui/a.gsx")); got.tabWidth != 8 {
		t.Errorf("[*.gsx] then [*]: tabWidth = %d, want 8 (last section, [*], wins despite being less specific)", got.tabWidth)
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

// Resolution walks UP the directory tree until it finds "root = true" or
// reaches the filesystem root. t.TempDir() lives under the system temp dir,
// so without an anchor this test would keep walking past the fixture into
// ancestor directories the test has no control over; any .editorconfig that
// ever appeared there would silently change the result. Placing a bare
// "root = true" (no keys) at the top of the fixture tree stops the walk right
// there, so the assertion tests "a reachable section sets no keys", not the
// untestable "no .editorconfig file exists anywhere".
func TestEditorConfigNoKeysIsUnset(t *testing.T) {
	root := writeTree(t, map[string]string{
		".editorconfig": "root = true\n",
		"ui/a.gsx":      "",
	})
	got := newEditorConfigResolver().settingsFor(filepath.Join(root, "ui/a.gsx"))
	if got.tabWidth != 0 || got.printWidth != 0 {
		t.Errorf("no keys: got %+v, want zero", got)
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
