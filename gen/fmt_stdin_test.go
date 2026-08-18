package gen

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gsxhq/gsx/internal/attrclass"
	"github.com/gsxhq/gsx/internal/codegen"
)

// fmtCaptureStdin drives runFmt with the given stdin content.
func fmtCaptureStdin(t *testing.T, stdin string, args []string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	wd, _ := os.Getwd()
	code := runFmt(strings.NewReader(stdin), &out, &errb, args, nil, nil, codegen.Options{Classifier: attrclass.Builtin()}, wd)
	return code, out.String(), errb.String()
}

// TestFmtExitCodeFailureWinsOverDiffers pins the exit-code contract: with -l a
// differing file alone is 1, but a parse failure anywhere in the same run is 2
// even though another file also differs.
func TestFmtExitCodeFailureWinsOverDiffers(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	bad := writeFile(t, dir, "bad.gsx", "package views\n\ncomponent Broken( {\n")
	dirty := writeFile(t, dir, "dirty.gsx", unformattedGsx)

	if code, _, errb := fmtCapture(t, []string{"-l", dirty}); code != 1 {
		t.Fatalf("-l dirty: exit = %d, want 1 (stderr=%q)", code, errb)
	}
	code, out, errb := fmtCapture(t, []string{"-l", bad, dirty})
	if code != 2 {
		t.Fatalf("-l bad+dirty: exit = %d, want 2 (stderr=%q)", code, errb)
	}
	if !strings.Contains(out, dirty) {
		t.Errorf("-l still lists the differing file alongside the failure:\n%s", out)
	}
	if !strings.Contains(errb, "bad.gsx") {
		t.Errorf("stderr missing the failing file: %q", errb)
	}
	// A nonexistent path is a failure too, not a "differs".
	if code, _, _ := fmtCapture(t, []string{"-l", filepath.Join(dir, "missing.gsx")}); code != 2 {
		t.Fatalf("-l missing: exit = %d, want 2", code)
	}
}

// TestFmtStdin covers -stdin-filename: the piped bytes are what gets formatted
// (the working copy at that path is never read, and is never written), the
// name flows into -l/-d output and error messages, and the exit codes match
// the file mode.
func TestFmtStdin(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	// The on-disk file is a DIFFERENT (already-canonical) source, so any output
	// that reflects it proves the path was read.
	const onDisk = "package views\n\ncomponent OnDisk() {\n\t<p>disk</p>\n}\n"
	p := writeFile(t, dir, "hi.gsx", onDisk)
	const canonical = "package views\n\ncomponent Hi(name string) {\n\t<p>{ name }</p>\n}\n"

	t.Run("default writes formatted stdin to stdout", func(t *testing.T) {
		code, out, errb := fmtCaptureStdin(t, unformattedGsx, []string{"-stdin-filename", p})
		if code != 0 {
			t.Fatalf("exit = %d, want 0 (stderr=%q)", code, errb)
		}
		if out != canonical {
			t.Errorf("stdout = %q, want %q", out, canonical)
		}
		if got, _ := os.ReadFile(p); string(got) != onDisk {
			t.Errorf("stdin mode modified the file on disk")
		}
	})
	t.Run("-l names the file and exits 1 when it differs", func(t *testing.T) {
		code, out, _ := fmtCaptureStdin(t, unformattedGsx, []string{"-l", "-stdin-filename", p})
		if code != 1 || out != p+"\n" {
			t.Errorf("exit = %d, stdout = %q; want 1 and %q", code, out, p+"\n")
		}
		code, out, _ = fmtCaptureStdin(t, canonical, []string{"-l", "-stdin-filename", p})
		if code != 0 || out != "" {
			t.Errorf("canonical stdin: exit = %d, stdout = %q; want 0 and empty", code, out)
		}
	})
	t.Run("-d diffs against the stdin bytes", func(t *testing.T) {
		code, out, _ := fmtCaptureStdin(t, unformattedGsx, []string{"-d", "-stdin-filename", p})
		if code != 1 {
			t.Fatalf("exit = %d, want 1", code)
		}
		if !strings.Contains(out, "--- "+p+".orig") || !strings.Contains(out, "-component   Hi(name string) {\n") {
			t.Errorf("diff does not name the file / show the stdin source:\n%s", out)
		}
		if strings.Contains(out, "OnDisk") {
			t.Errorf("diff reflects the on-disk file, not stdin:\n%s", out)
		}
	})
	t.Run("relative filename resolves against workDir", func(t *testing.T) {
		var out, errb bytes.Buffer
		code := runFmt(strings.NewReader(unformattedGsx), &out, &errb, []string{"-l", "-stdin-filename", "hi.gsx"}, nil, nil, codegen.Options{Classifier: attrclass.Builtin()}, dir)
		if code != 1 || out.String() != p+"\n" {
			t.Errorf("exit = %d, stdout = %q; want 1 and %q", code, out.String(), p+"\n")
		}
	})
	t.Run("nonexistent path is fine: nothing is read from it", func(t *testing.T) {
		ghost := filepath.Join(dir, "new.gsx")
		code, out, errb := fmtCaptureStdin(t, unformattedGsx, []string{"-stdin-filename", ghost})
		if code != 0 || out != canonical {
			t.Errorf("exit = %d, stdout = %q, stderr = %q; want 0 and canonical output", code, out, errb)
		}
	})
	t.Run("parse error exits 2 and names the file", func(t *testing.T) {
		code, _, errb := fmtCaptureStdin(t, "package views\n\ncomponent Broken( {\n", []string{"-stdin-filename", p})
		if code != 2 || !strings.Contains(errb, "hi.gsx") {
			t.Errorf("exit = %d, stderr = %q; want 2 naming hi.gsx", code, errb)
		}
	})
	t.Run("usage errors", func(t *testing.T) {
		for _, args := range [][]string{
			{"-w", "-stdin-filename", p},
			{"-stdin-filename", p, p},
			{"-stdin-filename", filepath.Join(dir, "notgsx.go")},
		} {
			if code, _, errb := fmtCaptureStdin(t, unformattedGsx, args); code != 2 || errb == "" {
				t.Errorf("%v: exit = %d, stderr = %q; want 2 with a message", args, code, errb)
			}
		}
	})
}

// TestFmtStdinUnusedImportsSeeStdin proves stdin mode analyzes the PIPED
// content for unused imports, not the working copy: the on-disk file uses
// bytes and leaves strings unused; the stdin content is the reverse. Removal
// must follow stdin. (One Module open — both directions share it.)
func TestFmtStdinUnusedImportsSeeStdin(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping module-resolution test in -short mode")
	}
	dir := newModule(t, "fmtmod")
	onDisk := "package fmtmod\n\nimport (\n\t\"bytes\"\n\t\"strings\"\n)\n\ncomponent Page() {\n\t<div>{bytes.ToUpper(nil)}</div>\n}\n"
	piped := "package fmtmod\n\nimport (\n\t\"bytes\"\n\t\"strings\"\n)\n\ncomponent Page() {\n\t<div>{strings.ToUpper(\"x\")}</div>\n}\n"
	page := writeFile(t, dir, "page.gsx", onDisk)

	code, out, errb := fmtCaptureStdin(t, piped, []string{"-stdin-filename", page})
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, errb)
	}
	if strings.Contains(out, `"bytes"`) {
		t.Errorf("bytes is unused in the stdin content and must be removed:\n%s", out)
	}
	if !strings.Contains(out, `"strings"`) {
		t.Errorf("strings is used in the stdin content and must be kept:\n%s", out)
	}
	if got, _ := os.ReadFile(page); string(got) != onDisk {
		t.Errorf("stdin mode modified the file on disk")
	}
}
