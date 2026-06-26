// internal/cssfmt/token_test.go
package cssfmt

import "testing"

func kinds(toks []token) []tokKind {
	ks := make([]tokKind, len(toks))
	for i, t := range toks {
		ks[i] = t.kind
	}
	return ks
}

func TestTokenizeRule(t *testing.T) {
	toks, err := tokenize([]byte(".a{color:red}"))
	if err != nil {
		t.Fatal(err)
	}
	want := []tokKind{tDelim, tWord, tLBrace, tWord, tColon, tWord, tRBrace}
	got := kinds(toks)
	if len(got) != len(want) {
		t.Fatalf("kinds = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("token %d = %v, want %v (%q)", i, got[i], want[i], toks[i].text)
		}
	}
}

func TestTokenizeStringWithBraces(t *testing.T) {
	toks, err := tokenize([]byte(`content:"a{b}c"`))
	if err != nil {
		t.Fatal(err)
	}
	// The braces/semicolons inside the string must stay inside ONE tString.
	var found bool
	for _, tk := range toks {
		if tk.kind == tString && tk.text == `"a{b}c"` {
			found = true
		}
	}
	if !found {
		t.Fatalf("string not tokenized atomically: %#v", toks)
	}
}

func TestTokenizeComment(t *testing.T) {
	toks, err := tokenize([]byte("/* hi {} ; */ .a"))
	if err != nil {
		t.Fatal(err)
	}
	if toks[0].kind != tComment || toks[0].text != "/* hi {} ; */" {
		t.Fatalf("comment not tokenized atomically: %#v", toks[0])
	}
}

func TestTokenizeSentinelIsWord(t *testing.T) {
	toks, err := tokenize([]byte("color:__gsxhole_0_"))
	if err != nil {
		t.Fatal(err)
	}
	last := toks[len(toks)-1]
	if last.kind != tWord || last.text != "__gsxhole_0_" {
		t.Fatalf("sentinel must be one word token, got %#v", last)
	}
}

func TestTokenizeUnterminatedString(t *testing.T) {
	if _, err := tokenize([]byte(`content:"oops`)); err == nil {
		t.Fatal("expected error for unterminated string")
	}
}

func TestTokenizeUnterminatedComment(t *testing.T) {
	if _, err := tokenize([]byte(`/* oops`)); err == nil {
		t.Fatal("expected error for unterminated comment")
	}
}

func TestTokenizeUnterminatedBareQuote(t *testing.T) {
	if _, err := tokenize([]byte(`"`)); err == nil {
		t.Fatal("expected error for a bare opening quote")
	}
}

func TestTokenizeUnterminatedTrailingEscape(t *testing.T) {
	// bytes: " t e x t \ "  (the final \" is an escape, not a terminator)
	if _, err := tokenize([]byte("\"text\\\"")); err == nil {
		t.Fatal("expected error for a string whose only closing quote is escaped")
	}
}
