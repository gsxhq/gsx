package codegen

import (
	"testing"

	"github.com/gsxhq/gsx/internal/attrclass"
)

// TestPresetFlagsCoverEveryPreset pins that every url preset attrclass knows
// has a runtime flag to emit: a preset added to attrclass without one would
// otherwise only fail at the first spread site it reaches, as a panic.
func TestPresetFlagsCoverEveryPreset(t *testing.T) {
	for _, name := range attrclass.PresetNames() {
		if _, ok := presetFlags[name]; !ok {
			t.Errorf("preset %q has no entry in presetFlags", name)
		}
	}
	if len(presetFlags) != len(attrclass.PresetNames()) {
		t.Errorf("presetFlags has %d entries, attrclass knows %d presets", len(presetFlags), len(attrclass.PresetNames()))
	}
}

func TestPresetFlagsExpr(t *testing.T) {
	if got := presetFlagsExpr(nil); got != "" {
		t.Errorf("presetFlagsExpr(nil) = %q, want empty", got)
	}
	// Duplicates collapse; the expression is a _gsxrt-qualified constant.
	if got, want := presetFlagsExpr([]string{"htmx", "htmx"}), "_gsxrt.PresetHTMX"; got != want {
		t.Errorf("presetFlagsExpr([htmx htmx]) = %q, want %q", got, want)
	}
}
