package gsx

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

type nvString string
type nvBool bool
type nvInt int
type nvFloat float64
type nvTags []string // named SLICE of string — element is still `string`

type nvStringer struct{}

func (nvStringer) String() string { return "STR" }

func TestAnyRenderVal(t *testing.T) {
	cases := []struct {
		name string
		v    any
		want string
		kind valKind
		ok   bool
	}{
		{"string", "a<b", "a<b", kindString, true},
		{"bytes", []byte("a<b"), "a<b", kindString, true},
		{"bool true", true, "true", kindBool, true},
		{"bool false", false, "false", kindBool, true},
		{"int", 5, "5", kindNumber, true},
		{"int negative", -5, "-5", kindNumber, true},
		{"uint", uint(5), "5", kindNumber, true},
		{"float64", 1.5, "1.5", kindNumber, true},
		{"float32", float32(1.5), "1.5", kindNumber, true},

		// Stringer wins over the underlying kind — mirrors classify's order, or
		// time.Duration (a named int64 WITH String()) would render 1000000000.
		{"Stringer", nvStringer{}, "STR", kindString, true},
		{"time.Duration", time.Second, "1s", kindString, true},

		// Named scalars: the whole point — classified by UNDERLYING kind.
		{"named string", nvString("y"), "y", kindString, true},
		{"named bool", nvBool(true), "true", kindBool, true},
		{"named int", nvInt(5), "5", kindNumber, true},
		{"named float", nvFloat(1.5), "1.5", kindNumber, true},

		// uintptr: Val used to be the outlier and errored; it renders.
		{"uintptr", uintptr(5), "5", kindNumber, true},

		// []string joins — the token-list reading class/style always had.
		{"string slice", []string{"a", "b"}, "a b", kindString, true},
		{"string slice empty", []string{}, "", kindString, true},
		// A named SLICE joins: its element is still exactly `string`, so codegen's
		// strings.Join lowering compiles for it.
		{"named slice of string", nvTags{"a", "b"}, "a b", kindString, true},
		// A slice of a NAMED string does NOT: strings.Join takes []string, and
		// []nvString is not assignable to it, so classify rejects it too. Joining
		// here would render what the static path cannot compile.
		{"slice of named string", []nvString{"a", "b"}, "", kindInvalid, false},

		{"struct", struct{ X int }{1}, "", kindInvalid, false},
		{"map", map[string]int{"a": 1}, "", kindInvalid, false},
		{"nil", nil, "", kindInvalid, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, k, ok := anyRenderVal(c.v)
			if got != c.want || k != c.kind || ok != c.ok {
				t.Errorf("anyRenderVal(%#v) = (%q, %v, %v); want (%q, %v, %v)",
					c.v, got, k, ok, c.want, c.kind, c.ok)
			}
		})
	}
}

// kindNumber and kindBool promise an escape-free charset; PR #122's gw.S win
// depends on that promise holding for every possible value, not just typical ones.
func TestAnyRenderValEscapeFreeKinds(t *testing.T) {
	vals := []any{
		true, false, 0, -1, 1 << 62, uint64(1) << 63, uintptr(0),
		1.5, -1.5, float32(1e-45), 1e308, -1e308,
	}
	for _, v := range vals {
		s, k, ok := anyRenderVal(v)
		if !ok || (k != kindBool && k != kindNumber) {
			t.Fatalf("anyRenderVal(%#v) = (%q, %v, %v); want an escape-free kind", v, s, k, ok)
		}
		if strings.ContainsAny(s, "\x00&<>\"'") {
			t.Errorf("anyRenderVal(%#v) = %q — kind %v promises escape-free but it contains an escapable byte", v, s, k)
		}
	}
}

// TestDispatchAgreement is the gate this change exists to install: every runtime
// consumer must answer identically for the same value, because they now share one
// classifier. It failed on 9 of these values before unification — named scalars
// and uintptr errored in Val/anyRenderString while toStr rendered them, and
// []string rendered only in toStr.
func TestDispatchAgreement(t *testing.T) {
	cases := []any{
		"a", []byte("a"), true, false, 5, -5, uint(5), 1.5, float32(1.5),
		nvStringer{}, time.Second,
		nvString("y"), nvBool(true), nvInt(5), nvFloat(1.5),
		uintptr(5), []string{"a", "b"},
	}
	for _, v := range cases {
		t.Run(fmt.Sprintf("%T", v), func(t *testing.T) {
			want, _, ok := anyRenderVal(v)
			if !ok {
				t.Fatalf("anyRenderVal(%#v) not ok — every case here must render", v)
			}

			var b strings.Builder
			if err := Val(v).Render(context.Background(), &b); err != nil {
				t.Fatalf("Val(%#v).Render = %v; want it to render %q", v, err, want)
			}
			if got := b.String(); got != want {
				t.Errorf("Val(%#v) = %q; anyRenderVal = %q", v, got, want)
			}

			if got, err := AttrString(v); err != nil || got != want {
				t.Errorf("AttrString(%#v) = (%q, %v); anyRenderVal = %q", v, got, err, want)
			}

			if got := toStr(v); got != want {
				t.Errorf("toStr(%#v) = %q; anyRenderVal = %q", v, got, want)
			}
		})
	}
}
