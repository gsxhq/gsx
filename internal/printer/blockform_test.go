package printer

import (
	"go/format"
	"testing"
)

func TestBlockFormBraces(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want string
	}{{
		name: "brace opens a line, so it closes one",
		src:  "package p\n\nvar x = T{\n\ta: 1}\n",
		want: "package p\n\nvar x = T{\n\ta: 1,\n}\n",
	}, {
		name: "inline literal is left alone",
		src:  "package p\n\nvar x = T{a: 1}\n",
		want: "package p\n\nvar x = T{a: 1}\n",
	}, {
		name: "already block-form is a no-op (idempotence)",
		src:  "package p\n\nvar x = T{\n\ta: 1,\n}\n",
		want: "package p\n\nvar x = T{\n\ta: 1,\n}\n",
	}, {
		name: "existing terminating comma gets a newline, not a second comma",
		src:  "package p\n\nvar x = T{\n\ta: 1,}\n",
		want: "package p\n\nvar x = T{\n\ta: 1,\n}\n",
	}, {
		// A comma inside a comment is not a terminating comma. Treating it as one
		// would emit `1 /*,*/\n}` — ASI puts a `;` after 1 and the literal dies.
		// The comma is inserted at the brace, i.e. after the comment; gofmt then
		// reflows it to `1, /* , */`.
		name: "comma inside a block comment is not a terminating comma",
		src:  "package p\n\nvar x = []int{\n\t1 /* , */}\n",
		want: "package p\n\nvar x = []int{\n\t1 /* , */,\n}\n",
	}, {
		// The last element ends a line but the `}` does not open one — the break
		// must still be inserted, since the `{` opened a line.
		name: "multi-line last element still gets the closing break",
		src:  "package p\n\nvar x = T{\n\tf: func() {\n\t\tg()\n\t}}\n",
		want: "package p\n\nvar x = T{\n\tf: func() {\n\t\tg()\n\t},\n}\n",
	}, {
		name: "empty literal is untouched",
		src:  "package p\n\nvar x = T{\n}\n",
		want: "package p\n\nvar x = T{\n}\n",
	}, {
		// Never a reason for gsx fmt to fail: unparseable Go passes through.
		name: "unparseable source passes through unchanged",
		src:  "package p\n\nvar x = T{{{\n",
		want: "package p\n\nvar x = T{{{\n",
	}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := blockFormBraces(tt.src); got != tt.want {
				t.Errorf("blockFormBraces:\n got %q\nwant %q", got, tt.want)
			}
		})
	}
}

// Everything blockFormBraces emits must still be valid Go — the inserted comma
// exists precisely to keep automatic semicolon insertion from breaking the
// literal — and must be a gofmt FIXED POINT, so the pass adds a rule on top of
// gofmt without ever fighting it.
func TestBlockFormBracesOutputIsGofmtFixedPoint(t *testing.T) {
	srcs := []string{
		"package p\n\nvar x = T{\n\ta: 1}\n",
		"package p\n\nvar x = []int{\n\t1 /* , */}\n",
		"package p\n\nvar x = Config{\n\touter: Outer{\n\t\tinner: Inner{\n\t\t\ta: 1}},\n\tinline: I{c: 3},\n}\n",
		"package p\n\nvar x = T{\n\tf: func() {\n\t\tg()\n\t}}\n",
	}
	for _, src := range srcs {
		rewritten := blockFormBraces(src)
		out, err := format.Source([]byte(rewritten))
		if err != nil {
			t.Errorf("gofmt rejected blockFormBraces output for %q: %v\n%s", src, err, rewritten)
			continue
		}
		again, err := format.Source(out)
		if err != nil {
			t.Errorf("gofmt rejected its own output for %q: %v", src, err)
			continue
		}
		if string(again) != string(out) {
			t.Errorf("gofmt is not stable on blockFormBraces output for %q", src)
		}
		// And the pass itself is a no-op on gofmt's output: no oscillation.
		if got := blockFormBraces(string(out)); got != string(out) {
			t.Errorf("blockFormBraces re-fires on gofmt's output for %q:\n got %q\nwant %q", src, got, out)
		}
	}
}
