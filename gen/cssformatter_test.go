package gen

import (
	"testing"

	"github.com/gsxhq/gsx/internal/rawfmt"
)

func TestWithCSSFormatterOption(t *testing.T) {
	t.Parallel()
	var cfg config
	f := func(src []byte) ([]byte, error) { return src, nil }
	WithCSSFormatter(rawfmt.Formatter(f))(&cfg)
	if cfg.cssFmt == nil {
		t.Fatal("WithCSSFormatter did not set cfg.cssFmt")
	}
}

func TestMergeConfigCSSFormatterOptsWins(t *testing.T) {
	t.Parallel()
	base := config{cssFmt: func(src []byte) ([]byte, error) { return []byte("base"), nil }}
	opts := config{cssFmt: func(src []byte) ([]byte, error) { return []byte("opts"), nil }}
	merged := mergeConfig(base, opts)
	got, _ := merged.cssFmt(nil)
	if string(got) != "opts" {
		t.Fatalf("merged.cssFmt = %q, want opts override", got)
	}
}

func TestMergeConfigCSSFormatterFallsBackToBase(t *testing.T) {
	t.Parallel()
	base := config{cssFmt: func(src []byte) ([]byte, error) { return []byte("base"), nil }}
	merged := mergeConfig(base, config{})
	if merged.cssFmt == nil {
		t.Fatal("merged.cssFmt should fall back to base")
	}
	got, _ := merged.cssFmt(nil)
	if string(got) != "base" {
		t.Fatalf("merged.cssFmt = %q, want base", got)
	}
}
