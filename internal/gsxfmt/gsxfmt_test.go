package gsxfmt

import "testing"

const messy = `package views



component   Hi(name string) {
    <p>{name}</p>
}
`

// TestFormatCanonicalizes: a messy file is rewritten to canonical form (collapsed
// blank lines, single space after `component`).
func TestFormatCanonicalizes(t *testing.T) {
	out, err := Format("hi.gsx", []byte(messy))
	if err != nil {
		t.Fatal(err)
	}
	got := string(out)
	if got == messy {
		t.Fatalf("Format did not change a non-canonical file:\n%s", got)
	}
	if want := "component Hi(name string)"; !contains(got, want) {
		t.Fatalf("formatted output missing %q:\n%s", want, got)
	}
}

// TestFormatIdempotent: formatting an already-canonical file is a no-op.
func TestFormatIdempotent(t *testing.T) {
	once, err := Format("hi.gsx", []byte(messy))
	if err != nil {
		t.Fatal(err)
	}
	twice, err := Format("hi.gsx", once)
	if err != nil {
		t.Fatal(err)
	}
	if string(once) != string(twice) {
		t.Fatalf("Format is not idempotent:\nonce:\n%s\ntwice:\n%s", once, twice)
	}
}

// TestFormatParseErrorReturnsError: invalid gsx yields an error, not silent
// truncation — callers decide whether to surface or ignore it.
func TestFormatParseErrorReturnsError(t *testing.T) {
	if _, err := Format("bad.gsx", []byte("package x\n\ncomponent Hi( {\n")); err == nil {
		t.Fatal("expected a parse error for malformed gsx, got nil")
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
