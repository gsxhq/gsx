package gen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMinifyLevel_Basics(t *testing.T) {
	t.Parallel()
	if MinifyNone != 0 {
		t.Fatalf("MinifyNone must be the zero value, got %d", MinifyNone)
	}
	if MinifyNone.enabled() {
		t.Fatal("MinifyNone must be disabled")
	}
	if !MinifyFull.enabled() {
		t.Fatal("MinifyFull must be enabled")
	}
	if MinifyNone.String() != "none" || MinifyFull.String() != "full" {
		t.Fatalf("String(): none=%q full=%q", MinifyNone.String(), MinifyFull.String())
	}
}

func TestParseMinifyLevel(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]MinifyLevel{"none": MinifyNone, "full": MinifyFull} {
		got, err := parseMinifyLevel(in)
		if err != nil || got != want {
			t.Fatalf("parseMinifyLevel(%q) = %v, %v", in, got, err)
		}
	}
	for _, bad := range []string{"safe", "on", "off", "aggressive"} {
		if _, err := parseMinifyLevel(bad); err == nil {
			t.Fatalf("parseMinifyLevel(%q) should error", bad)
		}
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
	t.Parallel()
	// Absent [minify] → both default to none.
	cfg, err := loadConfig(writeTOML(t, "[filters]\nupper = \"example.com/x.Up\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.cssMinLevel != MinifyNone || cfg.jsMinLevel != MinifyNone {
		t.Fatalf("absent [minify] should default to none/none, got %v/%v", cfg.cssMinLevel, cfg.jsMinLevel)
	}

	// Explicit levels.
	cfg, err = loadConfig(writeTOML(t, "[minify]\ncss = \"full\"\njs = \"none\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.cssMinLevel != MinifyFull || cfg.jsMinLevel != MinifyNone {
		t.Fatalf("got css=%v js=%v", cfg.cssMinLevel, cfg.jsMinLevel)
	}

	// Invalid level → error naming the key.
	if _, err := loadConfig(writeTOML(t, "[minify]\ncss = \"agressive\"\n")); err == nil {
		t.Fatal("invalid minify.css should error")
	}
}

func TestMergeConfig_MinifyPrecedence(t *testing.T) {
	t.Parallel()
	// option > config: opts pin via WithMinifyLevel beats file base.
	base := config{cssMinLevel: MinifyNone, jsMinLevel: MinifyNone}
	var opts config
	WithMinifyLevel(MinifyFull, MinifyFull)(&opts)
	merged := mergeConfig(base, opts)
	if merged.cssMinLevel != MinifyFull || merged.jsMinLevel != MinifyFull {
		t.Fatalf("WithMinifyLevel should win: got %v/%v", merged.cssMinLevel, merged.jsMinLevel)
	}

	// No option set → base (env/file) value flows through unchanged.
	merged = mergeConfig(base, config{})
	if merged.cssMinLevel != MinifyNone || merged.jsMinLevel != MinifyNone {
		t.Fatalf("no option should keep base: got %v/%v", merged.cssMinLevel, merged.jsMinLevel)
	}
}

func TestMinifyLevel_Full(t *testing.T) {
	t.Parallel()
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
	// enum values must stay none=0/full=1 (cache-key stability).
	if MinifyNone != 0 || MinifyFull != 1 {
		t.Fatalf("enum values drifted: none=%d full=%d", MinifyNone, MinifyFull)
	}
}

func TestEffectiveMinifier_Full(t *testing.T) {
	t.Parallel()
	// full with no custom minifier → built-in full installed.
	cfg := config{cssMinLevel: MinifyFull, jsMinLevel: MinifyFull}
	if cfg.effectiveCSSMin() == nil {
		t.Fatal("full should install a built-in CSS minifier")
	}
	if cfg.effectiveJSMin() == nil {
		t.Fatal("full should install a built-in JS minifier")
	}
	// none → nil (no ext minifier runs; verbatim output).
	cfg = config{cssMinLevel: MinifyNone, jsMinLevel: MinifyNone}
	if cfg.effectiveCSSMin() != nil || cfg.effectiveJSMin() != nil {
		t.Fatal("none should not install an ext minifier")
	}
	// custom minifier wins over full.
	custom := func(s string) (string, error) { return s, nil }
	cfg = config{cssMinLevel: MinifyFull, cssMin: custom}
	if got := cfg.effectiveCSSMin(); got == nil {
		t.Fatal("custom minifier must be returned")
	}
	// custom JS minifier wins over full.
	cfg = config{jsMinLevel: MinifyFull, jsMin: custom}
	if cfg.effectiveJSMin() == nil {
		t.Fatal("custom JS minifier must win")
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

	merged, _, err := resolveConfig(config{}, dir)
	if err != nil {
		t.Fatal(err)
	}
	res, err := generateCached([]string{"."}, merged.filterPkgs, merged.aliases, merged.classifier(), merged.fieldMatcher, false, merged.effectiveCSSMin(), merged.effectiveJSMin(), merged.cssMinLevel.enabled(), merged.jsMinLevel.enabled(), merged.classMerger)
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
	// full shortens #ffffff → #fff (none would keep #ffffff).
	if !strings.Contains(string(b), "#fff") || strings.Contains(string(b), "#ffffff") {
		t.Fatalf("[minify] css=full should shorten the hex; got:\n%s", b)
	}
}

// TestMinifyGate_LevelGoverns pins the documented model: the minify pass is
// gated by the LEVEL (cssMinLevel.enabled()), not by the presence of a custom
// minifier. A custom WithCSSMinifier/WithJSMinifier REPLACES the built-in full
// minifier when minification is enabled (level full); at the default level none
// it is not called. effectiveCSSMin still resolves WHICH minifier runs when the
// gate is on.
func TestMinifyGate_LevelGoverns(t *testing.T) {
	t.Parallel()
	custom := func(s string) (string, error) { return s, nil }
	// default level (none): gate off, even with a custom minifier installed.
	if (config{}).cssMinLevel.enabled() || (config{cssMin: custom}).cssMinLevel.enabled() {
		t.Fatal("at level none the minify gate must be off (custom minifier not called)")
	}
	// full level: gate on.
	if !(config{cssMinLevel: MinifyFull}).cssMinLevel.enabled() {
		t.Fatal("at level full the minify gate must be on")
	}
	// when the gate is on, a custom minifier is the resolved implementation.
	if got := (config{cssMinLevel: MinifyFull, cssMin: custom}).effectiveCSSMin(); got == nil {
		t.Fatal("custom CSS minifier must be the resolved minifier at level full")
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

	merged, _, err := resolveConfig(config{}, dir)
	if err != nil {
		t.Fatal(err)
	}
	res, err := generateCached([]string{"."}, merged.filterPkgs, merged.aliases, merged.classifier(), merged.fieldMatcher, false, merged.cssMin, merged.jsMin, merged.cssMinLevel.enabled(), merged.jsMinLevel.enabled(), merged.classMerger)
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
