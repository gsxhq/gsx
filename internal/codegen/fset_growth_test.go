package codegen

import (
	"os"
	"testing"
)

func TestOpenReadsFsetRebuildThreshold(t *testing.T) {
	// Default when env unset.
	t.Setenv("GSX_FSET_REBUILD_BYTES", "")
	os.Unsetenv("GSX_FSET_REBUILD_BYTES")
	m, err := Open(Options{ModuleRoot: t.TempDir(), ModulePath: "example.com/x"})
	if err != nil {
		t.Fatal(err)
	}
	if m.fsetRebuildBytes != defaultFsetRebuildBytes {
		t.Errorf("default threshold = %d, want %d", m.fsetRebuildBytes, defaultFsetRebuildBytes)
	}
	// Env override (including 0 = disabled).
	for _, tc := range []struct {
		env  string
		want int
	}{{"4096", 4096}, {"0", 0}, {"bogus", defaultFsetRebuildBytes}, {"-5", defaultFsetRebuildBytes}} {
		t.Setenv("GSX_FSET_REBUILD_BYTES", tc.env)
		m, err := Open(Options{ModuleRoot: t.TempDir(), ModulePath: "example.com/x"})
		if err != nil {
			t.Fatal(err)
		}
		if m.fsetRebuildBytes != tc.want {
			t.Errorf("env %q: threshold = %d, want %d", tc.env, m.fsetRebuildBytes, tc.want)
		}
	}
}
