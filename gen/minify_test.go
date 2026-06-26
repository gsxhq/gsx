package gen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMinifyLevel_Basics(t *testing.T) {
	if MinifySafe != 0 {
		t.Fatalf("MinifySafe must be the zero value, got %d", MinifySafe)
	}
	if !MinifySafe.enabled() {
		t.Fatal("MinifySafe must be enabled")
	}
	if MinifyNone.enabled() {
		t.Fatal("MinifyNone must be disabled")
	}
	if MinifySafe.String() != "safe" || MinifyNone.String() != "none" {
		t.Fatalf("String(): safe=%q none=%q", MinifySafe.String(), MinifyNone.String())
	}
}

func TestParseMinifyLevel(t *testing.T) {
	for in, want := range map[string]MinifyLevel{"safe": MinifySafe, "none": MinifyNone} {
		got, err := parseMinifyLevel(in)
		if err != nil || got != want {
			t.Fatalf("parseMinifyLevel(%q) = %v, %v", in, got, err)
		}
	}
	if _, err := parseMinifyLevel("aggressive"); err == nil {
		t.Fatal("parseMinifyLevel(aggressive) must error")
	}
}

// writeTOML writes a gsx.toml into a temp dir and returns its path.
func writeTOML(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "gsx.toml")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadConfig_Minify(t *testing.T) {
	// Absent [minify] → both default to safe.
	cfg, err := loadConfig(writeTOML(t, "[filters]\nupper = \"example.com/x.Up\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.cssMinLevel != MinifySafe || cfg.jsMinLevel != MinifySafe {
		t.Fatalf("absent [minify] should be safe/safe, got %v/%v", cfg.cssMinLevel, cfg.jsMinLevel)
	}

	// Explicit levels.
	cfg, err = loadConfig(writeTOML(t, "[minify]\ncss = \"none\"\njs = \"safe\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.cssMinLevel != MinifyNone || cfg.jsMinLevel != MinifySafe {
		t.Fatalf("got css=%v js=%v", cfg.cssMinLevel, cfg.jsMinLevel)
	}

	// Invalid level → error naming the key.
	if _, err := loadConfig(writeTOML(t, "[minify]\ncss = \"agressive\"\n")); err == nil {
		t.Fatal("invalid minify.css should error")
	}
}

func TestMergeConfig_MinifyPrecedence(t *testing.T) {
	// option > config: opts pin via WithMinifyLevel beats file base.
	base := config{cssMinLevel: MinifyNone, jsMinLevel: MinifyNone}
	var opts config
	WithMinifyLevel(MinifySafe, MinifySafe)(&opts)
	merged := mergeConfig(base, opts)
	if merged.cssMinLevel != MinifySafe || merged.jsMinLevel != MinifySafe {
		t.Fatalf("WithMinifyLevel should win: got %v/%v", merged.cssMinLevel, merged.jsMinLevel)
	}

	// No option set → base (env/file) value flows through unchanged.
	merged = mergeConfig(base, config{})
	if merged.cssMinLevel != MinifyNone || merged.jsMinLevel != MinifyNone {
		t.Fatalf("no option should keep base: got %v/%v", merged.cssMinLevel, merged.jsMinLevel)
	}
}

func TestMinifyLevel_Full(t *testing.T) {
	if !MinifyFull.enabled() {
		t.Fatal("MinifyFull must be enabled")
	}
	if MinifyFull.String() != "full" {
		t.Fatalf("MinifyFull.String() = %q", MinifyFull.String())
	}
	got, err := parseMinifyLevel("full")
	if err != nil || got != MinifyFull {
		t.Fatalf("parseMinifyLevel(full) = %v, %v", got, err)
	}
	// Existing values are unchanged (cache/behaviour stability).
	if MinifySafe != 0 || MinifyNone != 1 || MinifyFull != 2 {
		t.Fatalf("enum values drifted: safe=%d none=%d full=%d", MinifySafe, MinifyNone, MinifyFull)
	}
}

func TestEffectiveMinifier_Full(t *testing.T) {
	// full with no custom minifier → built-in full installed.
	cfg := config{cssMinLevel: MinifyFull, jsMinLevel: MinifyFull}
	if cfg.effectiveCSSMin() == nil {
		t.Fatal("full should install a built-in CSS minifier")
	}
	if cfg.effectiveJSMin() == nil {
		t.Fatal("full should install a built-in JS minifier")
	}
	// safe → nil (built-in safe runs via the gate, not via an ext func).
	cfg = config{cssMinLevel: MinifySafe, jsMinLevel: MinifySafe}
	if cfg.effectiveCSSMin() != nil || cfg.effectiveJSMin() != nil {
		t.Fatal("safe should not install an ext minifier")
	}
	// custom minifier wins over full.
	custom := func(s string) (string, error) { return s, nil }
	cfg = config{cssMinLevel: MinifyFull, cssMin: custom}
	if got := cfg.effectiveCSSMin(); got == nil {
		t.Fatal("custom minifier must be returned")
	}
}

func TestGenerate_MinifyFullViaConfig(t *testing.T) {
	dir := t.TempDir()
	repoRoot, _ := filepath.Abs("..")
	if err := os.WriteFile(filepath.Join(dir, "go.mod"),
		[]byte("module example.com/x\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "page.gsx"),
		[]byte("package x\n\ncomponent Page() {\n\t<style>\n\t\t.card { color: #ffffff; }\n\t</style>\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "gsx.toml"), []byte("[minify]\ncss = \"full\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".git"), []byte(""), 0o644); err != nil { // bound the config walk to dir
		t.Fatal(err)
	}
	chdir(t, dir)

	merged, _, err := resolveConfig(config{})
	if err != nil {
		t.Fatal(err)
	}
	res, err := generateCached([]string{"."}, merged.filterPkgs, merged.aliases, merged.classifier(), merged.fieldMatcher, false, merged.effectiveCSSMin(), merged.effectiveJSMin(), merged.cssMinLevel.enabled(), merged.jsMinLevel.enabled())
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Errs) > 0 {
		t.Fatalf("generate errors: %v", res.Errs)
	}
	b, err := os.ReadFile(filepath.Join(dir, "page.x.go"))
	if err != nil {
		t.Fatal(err)
	}
	// full shortens #ffffff → #fff (safe would keep #ffffff).
	if !strings.Contains(string(b), "#fff") || strings.Contains(string(b), "#ffffff") {
		t.Fatalf("[minify] css=full should shorten the hex; got:\n%s", b)
	}
}

func TestGenerate_MinifyNoneViaConfig(t *testing.T) {
	dir := t.TempDir()
	repoRoot, _ := filepath.Abs("..")
	if err := os.WriteFile(filepath.Join(dir, "go.mod"),
		[]byte("module example.com/x\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "page.gsx"),
		[]byte("package x\n\ncomponent Page() {\n\t<style>\n\t\t.card { margin: 1px  2px; }\n\t</style>\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "gsx.toml"), []byte("[minify]\ncss = \"none\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".git"), []byte(""), 0o644); err != nil { // bound the config walk to dir
		t.Fatal(err)
	}
	chdir(t, dir)

	merged, _, err := resolveConfig(config{})
	if err != nil {
		t.Fatal(err)
	}
	res, err := generateCached([]string{"."}, merged.filterPkgs, merged.aliases, merged.classifier(), merged.fieldMatcher, false, merged.cssMin, merged.jsMin, merged.cssMinLevel.enabled(), merged.jsMinLevel.enabled())
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Errs) > 0 {
		t.Fatalf("generate errors: %v", res.Errs)
	}
	b, err := os.ReadFile(filepath.Join(dir, "page.x.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "1px  2px") {
		t.Fatalf("[minify] css=none should preserve double space; got:\n%s", b)
	}
}
