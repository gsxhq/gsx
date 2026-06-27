package gen

import (
	"testing"

	"github.com/gsxhq/gsx/internal/rawfmt"
)

func TestWithJSFormatterOption(t *testing.T) {
	t.Parallel()
	var cfg config
	WithJSFormatter(rawfmt.Formatter(func(src []byte) ([]byte, error) { return src, nil }))(&cfg)
	if cfg.jsFmt == nil {
		t.Fatal("WithJSFormatter did not set cfg.jsFmt")
	}
}

func TestMergeConfigJSFormatterOptsWins(t *testing.T) {
	t.Parallel()
	base := config{jsFmt: func(src []byte) ([]byte, error) { return []byte("base"), nil }}
	opts := config{jsFmt: func(src []byte) ([]byte, error) { return []byte("opts"), nil }}
	got, _ := mergeConfig(base, opts).jsFmt(nil)
	if string(got) != "opts" {
		t.Fatalf("merged.jsFmt = %q, want opts override", got)
	}
}

func TestMergeConfigJSFormatterFallsBackToBase(t *testing.T) {
	t.Parallel()
	base := config{jsFmt: func(src []byte) ([]byte, error) { return []byte("base"), nil }}
	merged := mergeConfig(base, config{})
	if merged.jsFmt == nil {
		t.Fatal("merged.jsFmt should fall back to base")
	}
}
