// internal/reindent/reindent_test.go
package reindent

import (
	"strings"
	"testing"
)

// fake is a trivial adapter: it tokenizes a tiny toy language where '{'/'}' are
// Open/Close, '\n' is Newline, runs of ' '/'\t' are Space, and every other run
// of non-space, non-brace, non-newline bytes is one Other token. A leading "ERR"
// makes Tokenize fail (ok=false).
type fake struct{}

func (fake) Tokenize(src []byte) ([]Token, bool) {
	s := string(src)
	if strings.HasPrefix(s, "ERR") {
		return nil, false
	}
	var toks []Token
	i := 0
	for i < len(s) {
		switch c := s[i]; {
		case c == '\n':
			toks = append(toks, Token{Newline, "\n"})
			i++
		case c == ' ' || c == '\t':
			j := i
			for j < len(s) && (s[j] == ' ' || s[j] == '\t') {
				j++
			}
			toks = append(toks, Token{Space, s[i:j]})
			i = j
		case c == '{':
			toks = append(toks, Token{Open, "{"})
			i++
		case c == '}':
			toks = append(toks, Token{Close, "}"})
			i++
		default:
			j := i
			for j < len(s) && s[j] != '\n' && s[j] != ' ' && s[j] != '\t' && s[j] != '{' && s[j] != '}' {
				j++
			}
			toks = append(toks, Token{Other, s[i:j]})
			i = j
		}
	}
	return toks, true
}

func reindent(t *testing.T, in string) string {
	t.Helper()
	out, ok := Reindent([]byte(in), fake{})
	if !ok {
		t.Fatalf("Reindent reported failure on %q", in)
	}
	return out
}

func TestReindentFixesDepth(t *testing.T) {
	// Messy leading whitespace, correct structure.
	in := "a {\n      b\n   c\n}"
	want := "a {\n\tb\n\tc\n}"
	if got := reindent(t, in); got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestReindentCloseDedents(t *testing.T) {
	in := "a {\nb {\nc\n}\n}"
	want := "a {\n\tb {\n\t\tc\n\t}\n}"
	if got := reindent(t, in); got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestReindentPreservesBlankLines(t *testing.T) {
	in := "a {\nb\n\nc\n}"
	want := "a {\n\tb\n\n\tc\n}"
	got := reindent(t, in)
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
	// The blank line must carry NO trailing whitespace.
	for _, ln := range strings.Split(got, "\n") {
		if ln != strings.TrimRight(ln, " \t") {
			t.Fatalf("line %q has trailing whitespace", ln)
		}
	}
}

func TestReindentPreservesIntraLineSpacing(t *testing.T) {
	// Mid-line spacing is the author's; only LEADING indentation is normalized.
	in := "  x   plus   y"
	want := "x   plus   y"
	if got := reindent(t, in); got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestReindentOneLinerStaysOneLine(t *testing.T) {
	in := "a{b;c}"
	if got := reindent(t, in); got != "a{b;c}" {
		t.Fatalf("a one-liner must not be reflowed: got %q", got)
	}
}

func TestReindentLexFailureReportsFalse(t *testing.T) {
	if _, ok := Reindent([]byte("ERR whatever"), fake{}); ok {
		t.Fatal("expected ok=false on adapter lex failure")
	}
}
