package printer

import (
	"go/format"
	"strings"
	"testing"
)

func TestBreakWideLiterals(t *testing.T) {
	tests := []struct {
		name      string
		src, want string
	}{{
		name: "narrow literal untouched",
		src:  "package p\n\nvar x = []T{\n\t{a: 1, b: 2},\n}\n",
		want: "package p\n\nvar x = []T{\n\t{a: 1, b: 2},\n}\n",
	}, {
		// The outer break ALONE brings the inner under budget, so the inner stays
		// inline. An innermost-first implementation would explode the inner literal
		// here; this is the case that tells the two apart. Inner literal text is 77
		// columns; "var x = []T{" + 77 + "}" = 90 (over budget, so the outer
		// breaks); after the outer break, one tab + 77 + a trailing comma = 79
		// (under budget, so the inner is left alone).
		name: "outermost first: outer break alone suffices, inner stays inline",
		src:  "package p\n\nvar x = []T{{alpha: \"aaaaaaaaaaaaaaa\", beta: \"bbbbbbbbbbbbbbb\", gamma: \"ccccccccccccccc\"}}\n",
		want: "package p\n\nvar x = []T{\n\t{alpha: \"aaaaaaaaaaaaaaa\", beta: \"bbbbbbbbbbbbbbb\", gamma: \"ccccccccccccccc\"},\n}\n",
	}, {
		// The inner literal on its own line is 82 columns at tabWidth 1 (still
		// over an 80 budget), so the outer break alone is not enough: the inner
		// literal's fields get broken too, on the next round. This does NOT
		// distinguish outermost-first from innermost-first (both would arrive
		// here) -- see the case above for that.
		name: "nested: inner still over budget after the outer break, so it breaks too",
		src:  "package p\n\nvar x = []T{{alpha: \"aaaaaaaaaaaaaaaa\", beta: \"bbbbbbbbbbbbbbbb\", gamma: \"cccccccccccccccc\"}}\n",
		want: "package p\n\nvar x = []T{\n\t{\n\t\talpha: \"aaaaaaaaaaaaaaaa\",\n\t\tbeta:  \"bbbbbbbbbbbbbbbb\",\n\t\tgamma: \"cccccccccccccccc\",\n\t},\n}\n",
	}, {
		// A single field wider than the budget: no break can bring its line under
		// width, but go/printer's grouping breaks unconditionally once the flat
		// form doesn't fit — mirroring prettier, which breaks a single-property
		// object the same way even when the property's own line still overflows.
		// Breaking here is correct, not something to guard against — the
		// invariant this pins is termination, not "leave the unsplittable field
		// alone": round two can't relocate the (now split) literal onto the bad
		// line again, so it stops after one round. See
		// TestBreakWideLiteralsTerminates for the direct fixed-point check.
		name: "unsplittable field: breaks once, then stops",
		src:  "package p\n\nvar x = T{a: \"" + strings.Repeat("z", 100) + "\"}\n",
		want: "package p\n\nvar x = T{\n\ta: \"" + strings.Repeat("z", 100) + "\",\n}\n",
	}, {
		// A literal the author broke PARTIALLY: alpha, beta, and gamma still share
		// the `{` line (101 columns -- over the 80 budget), delta sits alone on
		// the next. Comparing only the first (alpha) and last (delta) element's
		// lines mistakes this for "already one per line" -- last (4) > first (3)
		// -- and leaves the 101-column line alone forever.
		//
		// NOTE: the spec's suggested src/want for this case (alpha+beta sharing
		// the line, gamma alone) gofmts to a 70-column first line -- under the
		// 80-column budget, so firstWideLine never fires and the case would pass
		// even against the unfixed code. Widened here to alpha+beta+gamma sharing
		// the line (the exact repro from the bug report) so the case actually
		// exercises, and fails against, the buggy guard.
		name: "partially broken literal is finished, not skipped",
		src:  "package p\n\nvar x = T{alpha: \"aaaaaaaaaaaaaaaaaaaa\", beta: \"bbbbbbbbbbbbbbbbbbbb\", gamma: \"cccccccccccccccccccc\",\n\tdelta: \"d\"}\n",
		want: "package p\n\nvar x = T{\n\talpha: \"aaaaaaaaaaaaaaaaaaaa\",\n\tbeta:  \"bbbbbbbbbbbbbbbbbbbb\",\n\tgamma: \"cccccccccccccccccccc\",\n\tdelta: \"d\",\n}\n",
	}, {
		// A literal already one element per line, with the first element already
		// off the `{` line: fully broken, so there is no progress to make. Must be
		// left byte-identical -- no blank lines inserted.
		name: "fully broken literal left as-is",
		src:  "package p\n\nvar x = T{\n\talpha: \"aaaaaaaaaaaaaaaaaaaa\",\n\tbeta:  \"bbbbbbbbbbbbbbbbbbbb\",\n\tgamma: \"cccccccccccccccccccc\",\n}\n",
		want: "package p\n\nvar x = T{\n\talpha: \"aaaaaaaaaaaaaaaaaaaa\",\n\tbeta:  \"bbbbbbbbbbbbbbbbbbbb\",\n\tgamma: \"cccccccccccccccccccc\",\n}\n",
	}, {
		// The case above never actually reaches the fully-broken guard: every line
		// is under budget, so firstWideLine returns 0 and breakFirstWideLiteral
		// exits before Inspect ever runs. Here the `var` line is independently
		// wide (a long identifier, nothing to do with the literal), so its line
		// number IS badLine on every round, and the literal's Lbrace sits on it
		// too -- the guard is actually exercised, every round, on an already
		// fully-broken multi-field literal. A guard that answered "not fully
		// broken" here would either splice in a blank line or -- for the
		// single-field variant -- report changed=true despite writing no bytes,
		// spinning forever.
		name: "fully broken literal with independently wide brace line stays put",
		src:  "package p\n\nvar " + strings.Repeat("x", 80) + " = T{\n\talpha: \"a\",\n\tbeta:  \"b\",\n}\n",
		want: "package p\n\nvar " + strings.Repeat("x", 80) + " = T{\n\talpha: \"a\",\n\tbeta:  \"b\",\n}\n",
	}, {
		name: "unparseable source passes through",
		src:  "package p\n\nvar x = T{{{\n",
		want: "package p\n\nvar x = T{{{\n",
	}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := breakWideLiterals(tt.src, 80, 1); got != tt.want {
				t.Errorf("breakWideLiterals:\n got %q\nwant %q", got, tt.want)
			}
		})
	}
}

// The invariant that makes this pass an extension of gofmt rather than a fork:
// gofmt accepts the output, is stable on it, and the pass is a no-op on gofmt's
// own output. Written BEFORE the pass, per the spec's risk note.
func TestBreakWideLiteralsOutputIsGofmtFixedPoint(t *testing.T) {
	srcs := []string{
		"package p\n\nvar x = []T{{alpha: \"aaaaaaaaaaaaaaaa\", beta: \"bbbbbbbbbbbbbbbb\", gamma: \"cccccccccccccccc\"}}\n",
		"package p\n\nvar x = map[string]string{\"alpha\": \"one\", \"beta\": \"two\", \"gamma\": \"three\", \"delta\": \"four\", \"epsilon\": \"five\"}\n",
		"package p\n\nvar x = T{a: \"" + strings.Repeat("z", 100) + "\"}\n",
	}
	for _, src := range srcs {
		rewritten := breakWideLiterals(src, 80, 1)
		out, err := format.Source([]byte(rewritten))
		if err != nil {
			t.Errorf("gofmt rejected breakWideLiterals output for %q: %v\n%s", src, err, rewritten)
			continue
		}
		again, err := format.Source(out)
		if err != nil || string(again) != string(out) {
			t.Errorf("gofmt not stable on breakWideLiterals output for %q", src)
			continue
		}
		if got := breakWideLiterals(string(out), 80, 1); got != string(out) {
			t.Errorf("breakWideLiterals re-fires on gofmt's output for %q:\n got %q\nwant %q", src, got, out)
		}
	}
}

// The loop ends on no progress, never on a round count. A field wider than the
// budget cannot be fixed by breaking, and must not loop forever. Assert both
// that the pass returns and that its output is a fixed point of itself — a pass
// that oscillated between two layouts would hang the formatter.
func TestBreakWideLiteralsTerminates(t *testing.T) {
	cases := map[string]string{
		"single over-long field": "package p\n\nvar x = T{a: \"" + strings.Repeat("z", 100) + "\"}\n",
		"nested unfixable":       "package p\n\nvar x = O{i: I{a: \"" + strings.Repeat("z", 100) + "\"}}\n",
		"deep nest":              "package p\n\nvar x = A{b: B{c: C{d: D{e: \"" + strings.Repeat("z", 90) + "\"}}}}\n",
	}
	for name, src := range cases {
		out := breakWideLiterals(src, 80, 1)
		if again := breakWideLiterals(out, 80, 1); again != out {
			t.Errorf("%s: not a fixed point of itself\n once %q\ntwice %q", name, out, again)
		}
	}
}
