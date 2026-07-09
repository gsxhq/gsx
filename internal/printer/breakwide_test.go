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
		// The inner literal on its own line is 82 columns at tabWidth 1 (still
		// over an 80 budget), so the outer break alone is not enough: the inner
		// literal's fields get broken too, on the next round.
		name: "outermost first: inner still over budget, so it breaks too",
		src:  "package p\n\nvar x = []T{{alpha: \"aaaaaaaaaaaaaaaa\", beta: \"bbbbbbbbbbbbbbbb\", gamma: \"cccccccccccccccc\"}}\n",
		want: "package p\n\nvar x = []T{\n\t{\n\t\talpha: \"aaaaaaaaaaaaaaaa\",\n\t\tbeta:  \"bbbbbbbbbbbbbbbb\",\n\t\tgamma: \"cccccccccccccccc\",\n\t},\n}\n",
	}, {
		// A single field wider than the budget: no break can bring its line under
		// width, but go/printer's grouping breaks unconditionally once the flat
		// form doesn't fit — mirroring prettier, which breaks a single-property
		// object the same way even when the property's own line still overflows.
		// The pass still must not loop: round two can't relocate the (now split)
		// literal onto the bad line again, so it stops after one round.
		name: "single over-long field breaks once, then stops (no infinite loop)",
		src:  "package p\n\nvar x = T{a: \"" + strings.Repeat("z", 100) + "\"}\n",
		want: "package p\n\nvar x = T{\n\ta: \"" + strings.Repeat("z", 100) + "\",\n}\n",
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
