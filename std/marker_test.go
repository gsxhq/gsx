package std

import (
	"reflect"
	"testing"
)

// TestPkgMarkerPath proves std.Pkg's type lives in the std package, so
// gen.WithFilters can recover the std import path from it via reflection.
func TestPkgMarkerPath(t *testing.T) {
	got := reflect.TypeFor[pkgMarker]().PkgPath()
	want := "github.com/gsxhq/gsx/std"
	if got != want {
		t.Fatalf("reflect.TypeOf(std.Pkg).PkgPath() = %q, want %q", got, want)
	}
}
