package gen

import (
	"os"
	"path/filepath"
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
