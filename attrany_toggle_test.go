package gsx

import (
	"strings"
	"testing"
)

func attrAnyToggle(t *testing.T, name string, v any) (string, error) {
	t.Helper()
	var b strings.Builder
	gw := W(&b)
	gw.AttrAnyToggle(name, v)
	return b.String(), gw.Err()
}

// AttrAnyToggle is emitted for a value whose type is known only at runtime
// (a mixed type parameter, T string | bool) on a name codegen resolved to be a
// boolean attribute. A bool-kinded value toggles; anything else is a string.
func TestAttrAnyToggle(t *testing.T) {
	cases := []struct {
		name string
		v    any
		want string
	}{
		{"required", true, " required"},
		{"required", false, ""},
		{"required", "foo", ` required="foo"`}, // the string instantiation
		{"required", "a<b", ` required="a&lt;b"`},
		// underlying-bool value (a named bool through the same type param) toggles too
		{"required", spreadFlag(false), ""},
	}
	for _, c := range cases {
		got, err := attrAnyToggle(t, c.name, c.v)
		if err != nil || got != c.want {
			t.Errorf("AttrAnyToggle(%q, %#v) = (%q, %v); want (%q, nil)", c.name, c.v, got, err, c.want)
		}
	}
}

func TestAttrAnyToggleUnsupported(t *testing.T) {
	_, err := attrAnyToggle(t, "required", struct{ X int }{1})
	if err == nil {
		t.Fatal("AttrAnyToggle with a struct: want an error")
	}
}
