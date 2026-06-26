package gen

import (
	"os"
	"testing"
)

func TestApplyEnvOverrides_Minify(t *testing.T) {
	t.Setenv("GSX_MINIFY", "off")
	cfg, err := applyEnvOverrides(config{})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.cssMinLevel != MinifyNone || cfg.jsMinLevel != MinifyNone {
		t.Fatalf("GSX_MINIFY=off → none/none, got %v/%v", cfg.cssMinLevel, cfg.jsMinLevel)
	}

	t.Setenv("GSX_MINIFY", "on")
	cfg, err = applyEnvOverrides(config{cssMinLevel: MinifyNone, jsMinLevel: MinifyNone})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.cssMinLevel != MinifySafe || cfg.jsMinLevel != MinifySafe {
		t.Fatalf("GSX_MINIFY=on → safe/safe, got %v/%v", cfg.cssMinLevel, cfg.jsMinLevel)
	}

	t.Setenv("GSX_MINIFY", "banana")
	if _, err := applyEnvOverrides(config{}); err == nil {
		t.Fatal("GSX_MINIFY=banana must error")
	}
}

func TestApplyEnvOverrides_MinifyVocabulary(t *testing.T) {
	cases := map[string]MinifyLevel{
		"off": MinifyNone, "none": MinifyNone,
		"on": MinifySafe, "safe": MinifySafe,
		"full": MinifyFull,
	}
	for raw, want := range cases {
		t.Setenv("GSX_MINIFY", raw)
		cfg, err := applyEnvOverrides(config{})
		if err != nil {
			t.Fatalf("GSX_MINIFY=%q: %v", raw, err)
		}
		if cfg.cssMinLevel != want || cfg.jsMinLevel != want {
			t.Fatalf("GSX_MINIFY=%q → css=%v js=%v, want %v", raw, cfg.cssMinLevel, cfg.jsMinLevel, want)
		}
	}
	t.Setenv("GSX_MINIFY", "banana")
	if _, err := applyEnvOverrides(config{}); err == nil {
		t.Fatal("GSX_MINIFY=banana must error")
	}
}

func TestApplyEnvOverrides_AbsentIsNoop(t *testing.T) {
	// No GSX_* set: file value (none) is preserved untouched.
	base := config{cssMinLevel: MinifyNone, jsMinLevel: MinifySafe}
	cfg, err := applyEnvOverrides(base)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.cssMinLevel != MinifyNone || cfg.jsMinLevel != MinifySafe {
		t.Fatalf("absent env should preserve file values, got %v/%v", cfg.cssMinLevel, cfg.jsMinLevel)
	}
}

func TestResolveConfig_EnvWithoutFile(t *testing.T) {
	// In an empty temp dir (no gsx.toml), env still applies.
	dir := t.TempDir()
	chdir(t, dir)
	t.Setenv("GSX_MINIFY", "off")
	merged, path, err := resolveConfig(config{})
	if err != nil {
		t.Fatal(err)
	}
	if path != "" {
		t.Fatalf("expected no config path, got %q", path)
	}
	if merged.cssMinLevel != MinifyNone || merged.jsMinLevel != MinifyNone {
		t.Fatalf("env-only resolve → none/none, got %v/%v", merged.cssMinLevel, merged.jsMinLevel)
	}
}

// chdir changes to dir for the duration of the test.
func chdir(t *testing.T, dir string) {
	t.Helper()
	orig, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chdir(orig) })
}
