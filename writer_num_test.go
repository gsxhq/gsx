package gsx

import (
	"io"
	"strings"
	"testing"
)

func TestWriterNumbers(t *testing.T) {
	var buf [32]byte
	cases := []struct {
		name string
		fn   func(*Writer)
		want string
	}{
		{"int", func(w *Writer) { w.IntInto(buf[:], 1234) }, "1234"},
		{"int-neg", func(w *Writer) { w.IntInto(buf[:], -42) }, "-42"},
		{"int-zero", func(w *Writer) { w.IntInto(buf[:], 0) }, "0"},
		{"uint", func(w *Writer) { w.UintInto(buf[:], 18446744073709551615) }, "18446744073709551615"},
		{"float", func(w *Writer) { w.FloatInto(buf[:], 3.14159) }, "3.14159"},
		{"float-neg", func(w *Writer) { w.FloatInto(buf[:], -0.5) }, "-0.5"},
		{"float-big", func(w *Writer) { w.FloatInto(buf[:], 1e21) }, "1e+21"},
	}
	for _, c := range cases {
		var b strings.Builder
		gw := W(&b)
		c.fn(gw)
		if gw.Err() != nil {
			t.Fatalf("%s: err %v", c.name, gw.Err())
		}
		if b.String() != c.want {
			t.Fatalf("%s: got %q want %q", c.name, b.String(), c.want)
		}
	}
}

// A per-render shared buffer keeps numeric interpolation to at most one
// allocation per render regardless of how many numbers are written.
func TestWriterNumbersShared(t *testing.T) {
	gw := W(io.Discard)
	allocs := testing.AllocsPerRun(100, func() {
		var buf [32]byte // one per "render"
		for n := int64(1000); n < 1020; n++ {
			gw.IntInto(buf[:], n)
		}
	})
	if allocs > 1 {
		t.Errorf("20 ints allocated %v times, want <= 1", allocs)
	}
}
