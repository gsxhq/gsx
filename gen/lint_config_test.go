package gen

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/gsxhq/gsx/internal/diag"
)

// TestLoadConfigLint pins the [lint] table: each configurable code takes
// off/warning/error; any other code, level or value type is a config error
// naming the file and the key.
func TestLoadConfigLint(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		toml    string
		want    diag.LintPolicy
		wantErr []string
	}{
		{name: "absent", toml: ``},
		{
			name: "all levels",
			toml: "[lint]\nchild-element-spacing = \"error\"\nduplicate-attribute = \"off\"\nurl-always-blocked = \"warning\"\n",
			want: diag.LintPolicy{"child-element-spacing": diag.LintError, "duplicate-attribute": diag.LintOff, "url-always-blocked": diag.LintWarning},
		},
		{name: "unknown code", toml: "[lint]\nchild-spacing = \"off\"\n", wantErr: []string{"gsx.toml", `unknown code "child-spacing"`, "child-element-spacing"}},
		{name: "error code is not a lint", toml: "[lint]\nparse-error = \"off\"\n", wantErr: []string{`unknown code "parse-error"`}},
		{name: "bad level", toml: "[lint]\nwrapper-cycle = \"loud\"\n", wantErr: []string{"lint.wrapper-cycle", `"loud"`, `"off", "warning" or "error"`}},
		{name: "non-string level", toml: "[lint]\nwrapper-cycle = true\n", wantErr: []string{"gsx.toml", "lint"}},
		{name: "lint not a table", toml: "lint = \"x\"\n", wantErr: []string{"gsx.toml", "lint: must be a table (got string)"}},
		{name: "renderers not a table", toml: "renderers = 1\n", wantErr: []string{"renderers: must be a table (got integer)"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "gsx.toml")
			mkfile(t, path, tc.toml)
			cfg, err := loadConfig(path)
			if tc.wantErr != nil {
				if err == nil {
					t.Fatalf("loadConfig accepted %q", tc.toml)
				}
				for _, w := range tc.wantErr {
					if !strings.Contains(err.Error(), w) {
						t.Fatalf("loadConfig err = %v, want it to mention %q", err, w)
					}
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(cfg.lint, tc.want) {
				t.Fatalf("lint = %v, want %v", cfg.lint, tc.want)
			}
		})
	}
}

// TestMergeConfigLint: a WithLint entry overrides the file's entry for that
// code only; the merge never aliases either input's map.
func TestMergeConfigLint(t *testing.T) {
	t.Parallel()
	base := config{lint: diag.LintPolicy{"child-element-spacing": diag.LintError, "wrapper-cycle": diag.LintOff}}
	var opts config
	WithLint("child-element-spacing", "off")(&opts)
	merged := mergeConfig(base, opts)
	want := diag.LintPolicy{"child-element-spacing": diag.LintOff, "wrapper-cycle": diag.LintOff}
	if !reflect.DeepEqual(merged.lint, want) {
		t.Fatalf("merged lint = %v, want %v", merged.lint, want)
	}
	merged.lint["wrapper-cycle"] = diag.LintError
	if base.lint["wrapper-cycle"] != diag.LintOff {
		t.Fatal("mergeConfig aliased the base lint map")
	}
}

func TestWithLintRejectsBadInput(t *testing.T) {
	t.Parallel()
	for _, args := range [][2]string{{"nope", "off"}, {"wrapper-cycle", "fatal"}} {
		var cfg config
		WithLint(args[0], args[1])(&cfg)
		if len(cfg.errs) != 1 || !strings.Contains(cfg.errs[0].Error(), "WithLint") {
			t.Fatalf("WithLint(%q, %q) errs = %v, want one WithLint error", args[0], args[1], cfg.errs)
		}
	}
}

func TestLSPSemanticConfigIdentityTracksLint(t *testing.T) {
	t.Parallel()
	base := config{}
	if lspSemanticConfigIdentity(base) != lspSemanticConfigIdentity(config{lint: diag.LintPolicy{"wrapper-cycle": diag.LintWarning}}) {
		t.Fatal("an explicit default lint level changed Module semantic identity")
	}
	if lspSemanticConfigIdentity(base) == lspSemanticConfigIdentity(config{lint: diag.LintPolicy{"wrapper-cycle": diag.LintError}}) {
		t.Fatal("a promoted lint did not change Module semantic identity")
	}
}

func TestComputeKeyVariesByLint(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	mkfile(t, filepath.Join(root, "go.mod"), "module example.com/x\n\ngo 1.26.1\n")
	dir := filepath.Join(root, "view")
	mkfile(t, filepath.Join(dir, "view.go"), "package view\n")
	mkfile(t, filepath.Join(dir, "view.gsx"), "package view\ncomponent View() { <p/> }\n")
	graph := loadGraphMust(t, root)
	key := func(lint diag.LintPolicy) string {
		k, err := computeTestKey(t, dir, root, graph, cacheKeyConfig{
			buildContext: "bctx", codegenIdentity: "codegenid", classifierFingerprint: "clsfp",
			lint: lint.Fingerprint(),
		})
		if err != nil {
			t.Fatal(err)
		}
		return k
	}
	def := key(nil)
	if def != key(diag.LintPolicy{"child-element-spacing": diag.LintWarning}) {
		t.Fatal("an explicit default lint level changed the cache key")
	}
	if def == key(diag.LintPolicy{"child-element-spacing": diag.LintError}) {
		t.Fatal("promoting a lint to error must change the cache key")
	}
}

// TestGenerateLintLevels drives `gsx generate` over one package with a
// child-element-spacing finding under each [lint] level: the default warns
// and writes real output (exit 0); "error" fails (exit 1) and writes a poison
// .x.go so go build fails too, even right after a cached warning run; "off"
// is silent; an unknown code is a config error (exit 2).
func TestGenerateLintLevels(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping go-run e2e in -short mode")
	}
	tmp := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmp, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	mkfile(t, filepath.Join(tmp, "go.mod"), "module lintcfg\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot(t)+"\n")
	mkfile(t, filepath.Join(tmp, "views", "views.gsx"), "package views\n\ncomponent C() {\n\t<p>\n\t\tRead the\n\t\t<a href=\"/d\">docs</a>\n\t</p>\n}\n")
	cfgPath := filepath.Join(tmp, "gsx.toml")
	xgo := filepath.Join(tmp, "views", "views.x.go")
	t.Setenv("GSXCACHE", t.TempDir())
	gen := func() (int, string, string) {
		var out, errb bytes.Buffer
		code := run([]string{"-C", tmp, "generate", "./views"}, &out, &errb)
		return code, out.String(), errb.String()
	}
	const finding = "views.gsx:5:11: warning[child-element-spacing]: line break after this text is dropped: no space renders before <a>"

	code, _, stderr := gen()
	if code != 0 || !strings.Contains(stderr, finding) {
		t.Fatalf("default: code=%d stderr=%q, want 0 and %q", code, stderr, finding)
	}
	if src, _ := os.ReadFile(xgo); strings.Contains(string(src), "GSX GENERATION FAILED") {
		t.Fatal("a warning must not poison the output")
	}

	mkfile(t, cfgPath, "[lint]\nchild-element-spacing = \"error\"\n")
	code, _, stderr = gen()
	if code != 1 || !strings.Contains(stderr, "error[child-element-spacing]") {
		t.Fatalf("error: code=%d stderr=%q, want 1 and an error[child-element-spacing]", code, stderr)
	}
	if src, _ := os.ReadFile(xgo); !strings.Contains(string(src), "GSX GENERATION FAILED") {
		t.Fatalf("a lint error must poison the output, got:\n%s", src)
	}

	mkfile(t, cfgPath, "[lint]\nchild-element-spacing = \"off\"\n")
	code, _, stderr = gen()
	if code != 0 || strings.Contains(stderr, "child-element-spacing") {
		t.Fatalf("off: code=%d stderr=%q, want 0 and no finding", code, stderr)
	}

	mkfile(t, cfgPath, "[lint]\nchild-spacing = \"off\"\n")
	code, _, stderr = gen()
	if code != 2 || !strings.Contains(stderr, `unknown code "child-spacing"`) {
		t.Fatalf("unknown code: code=%d stderr=%q, want 2 and the unknown code", code, stderr)
	}
}
