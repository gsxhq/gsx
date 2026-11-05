package gen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRunFixWhitespace covers `gsx fix whitespace`: -l and -d report without
// writing (exit 1), the default rewrites in place, and a second run is a no-op.
// No module is loaded: the fixer is syntactic.
func TestRunFixWhitespace(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "page.gsx")
	src := "package views\n\ncomponent Page() {\n\t<p>download <a>it</a>\n\t.</p>\n}\n"
	want := "package views\n\ncomponent Page() {\n\t<p>download <a>it</a>.</p>\n}\n"
	clean := filepath.Join(dir, "clean.gsx")
	for p, s := range map[string]string{path: src, clean: "package views\n\ncomponent Clean() {\n\t<p>a . b</p>\n}\n"} {
		if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	code, out, errb := runCapture(t, []string{"-C", dir, "fix", "whitespace", "-l"})
	if code != 1 || strings.TrimSpace(out) != path {
		t.Fatalf("-l: code %d, out %q, err %q", code, out, errb)
	}
	code, out, _ = runCapture(t, []string{"-C", dir, "fix", "whitespace", "-d"})
	if code != 1 || !strings.Contains(out, "@@ -1,6 +1,5 @@\n") || !strings.Contains(out, "-\t<p>download <a>it</a>\n-\t.</p>\n+\t<p>download <a>it</a>.</p>\n") {
		t.Fatalf("-d: code %d, out:\n%s", code, out)
	}
	if got, _ := os.ReadFile(path); string(got) != src {
		t.Fatalf("-l/-d wrote the file:\n%s", got)
	}

	if code, out, errb := runCapture(t, []string{"-C", dir, "fix", "whitespace", "-l", "-w"}); code != 0 || strings.TrimSpace(out) != path || !strings.Contains(errb, "fixed 1 whitespace finding(s) in "+path) {
		t.Fatalf("-l -w: code %d, out %q, err %q", code, out, errb)
	}
	if got, _ := os.ReadFile(path); string(got) != want {
		t.Fatalf("fixed file:\n%s\nwant:\n%s", got, want)
	}
	if code, out, _ := runCapture(t, []string{"-C", dir, "fix", "whitespace", "-l"}); code != 0 || out != "" {
		t.Fatalf("second run: code %d, out %q", code, out)
	}

	if code, _, errb := runCapture(t, []string{"fix", "imports"}); code != 2 || !strings.Contains(errb, `unknown fixer "imports"`) {
		t.Fatalf("unknown fixer: code %d, err %q", code, errb)
	}
}
