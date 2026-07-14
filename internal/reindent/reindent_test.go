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
		switch c := s[i]; c {
		case '\n':
			toks = append(toks, Token{Newline, "\n"})
			i++
		case ' ', '\t':
			j := i
			for j < len(s) && (s[j] == ' ' || s[j] == '\t') {
				j++
			}
			toks = append(toks, Token{Space, s[i:j]})
			i = j
		case '{':
			toks = append(toks, Token{Open, "{"})
			i++
		case '}':
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

func TestReindentRebasesPreservingRelative(t *testing.T) {
	// A block indented at some base dedents to zero, keeping its relative
	// structure exactly (the caller re-adds the target base).
	in := "\t\ta {\n\t\t\tb\n\t\t\tc\n\t\t}"
	want := "a {\n\tb\n\tc\n}"
	if got := reindent(t, in); got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestReindentPreservesAuthorRelativeIndent(t *testing.T) {
	// The author's relative indentation is preserved, NOT recomputed from
	// structure — a continuation / hanging indent survives (this is the whole
	// point: no brace-depth normalization, so nothing gets flattened). The
	// continuation is not the first line, so the base is 0 (foo/bar recur it).
	in := "foo();\nx = a\n\t|| b;\nbar();"
	want := "foo();\nx = a\n\t|| b;\nbar();"
	if got := reindent(t, in); got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestReindentDoesNotNormalizeFlatInput(t *testing.T) {
	// Flat (unindented) input stays flat — re-basing never ADDS structure.
	in := "a {\nb\nc\n}"
	if got := reindent(t, in); got != in {
		t.Fatalf("got %q want %q (flat input must stay flat)", got, in)
	}
}

func TestReindentPreservesBlankLines(t *testing.T) {
	in := "\ta {\n\tb\n\n\tc\n\t}"
	want := "a {\nb\n\nc\n}"
	got := reindent(t, in)
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
	// The blank line must carry NO trailing whitespace.
	for ln := range strings.SplitSeq(got, "\n") {
		if ln != strings.TrimRight(ln, " \t") {
			t.Fatalf("line %q has trailing whitespace", ln)
		}
	}
}

func TestReindentExcludesAttachedFirstLine(t *testing.T) {
	// An inline attribute value's `{` sits at column 0 (attached to the delimiter)
	// while the body carries the source base indentation. The first line is
	// excluded from the base so the body dedents correctly and `{` stays attached.
	in := "{\n\t\t\topen: false,\n\t\t}"
	want := "{\n\topen: false,\n}"
	if got := reindent(t, in); got != want {
		t.Fatalf("got %q want %q", got, want)
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

// opaqueFake tokenizes like fake but treats a backtick-delimited run (which may
// span newlines) as a single Opaque token whose internal newlines are content.
type opaqueFake struct{}

func (opaqueFake) Tokenize(src []byte) ([]Token, bool) {
	s := string(src)
	var toks []Token
	i := 0
	for i < len(s) {
		switch c := s[i]; c {
		case '`':
			j := i + 1
			for j < len(s) && s[j] != '`' {
				j++
			}
			if j < len(s) {
				j++ // include closing backtick
			}
			toks = append(toks, Token{Opaque, s[i:j]})
			i = j
		case '\n':
			toks = append(toks, Token{Newline, "\n"})
			i++
		case ' ', '\t':
			j := i
			for j < len(s) && (s[j] == ' ' || s[j] == '\t') {
				j++
			}
			toks = append(toks, Token{Space, s[i:j]})
			i = j
		case '{':
			toks = append(toks, Token{Open, "{"})
			i++
		case '}':
			toks = append(toks, Token{Close, "}"})
			i++
		default:
			j := i
			for j < len(s) && s[j] != '\n' && s[j] != ' ' && s[j] != '\t' && s[j] != '{' && s[j] != '}' && s[j] != '`' {
				j++
			}
			toks = append(toks, Token{Other, s[i:j]})
			i = j
		}
	}
	return toks, true
}

func TestReindentLinesOpaqueInteriorStaysOneLine(t *testing.T) {
	// A backtick literal spanning 3 physical lines inside a braced block.
	in := "{\nx `a\nb\nc`\n}\n"
	lines, ok := ReindentLines([]byte(in), opaqueFake{})
	if !ok {
		t.Fatal("ok=false")
	}
	// Logical lines: "{", "x `a\nb\nc`", "}", "" — the opaque token keeps its
	// internal newlines within ONE logical line. Indentation is preserved as
	// written (the author put these at column 0), not recomputed from braces.
	want := []string{"{", "x `a\nb\nc`", "}", ""}
	if len(lines) != len(want) {
		t.Fatalf("got %d lines %q, want %d %q", len(lines), lines, len(want), want)
	}
	for i := range want {
		if lines[i] != want[i] {
			t.Fatalf("line %d: got %q want %q (all: %q)", i, lines[i], want[i], lines)
		}
	}
}

func TestReindentLinesJoinEqualsReindent(t *testing.T) {
	for _, in := range []string{"{\nx `a\nb`\n}\n", "a\n{\nb\n}\n", "{\n}\n"} {
		lines, ok := ReindentLines([]byte(in), opaqueFake{})
		if !ok {
			t.Fatalf("%q: ok=false", in)
		}
		flat, _ := Reindent([]byte(in), opaqueFake{})
		if strings.Join(lines, "\n") != flat {
			t.Fatalf("%q: join(lines)=%q != Reindent=%q", in, strings.Join(lines, "\n"), flat)
		}
	}
}

func TestReindentCommentInteriorRebasesStringStaysVerbatim(t *testing.T) {
	// opaqueFake treats backticks as verbatim strings; extend it isn't needed —
	// use SplitComment directly for the comment half.
	// A multi-line comment's interior re-bases (aligns); a backtick string does not.
	toks := SplitComment("/* a\n\t   b\n\t   c */")
	// first line is Opaque; interiors are Newline + Space + Opaque (re-basable).
	if toks[0].Class != Opaque || toks[0].Text != "/* a" {
		t.Fatalf("first line: %+v", toks[0])
	}
	sawSpace := false
	for _, tk := range toks {
		if tk.Class == Space {
			sawSpace = true
		}
	}
	if !sawSpace {
		t.Fatalf("comment interior leading not a Space token (won't re-base): %+v", toks)
	}
	// single-line comment stays one Opaque token.
	if s := SplitComment("// x"); len(s) != 1 || s[0].Class != Opaque {
		t.Fatalf("single-line comment must be one Opaque token: %+v", s)
	}
}
