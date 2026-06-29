package main

import "testing"

func TestPresetsFromEmbed(t *testing.T) {
	if len(presets) != 42 {
		t.Fatalf("want 42 embedded presets, got %d", len(presets))
	}
	for i, p := range presets {
		if p.GSX == "" || p.Invoke == "" {
			t.Fatalf("preset %d empty: %+v", i, p)
		}
	}
}
