package std

import (
	"encoding/base64"
	"testing"
)

func TestUpper(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", ""},
		{"ascii", "hello", "HELLO"},
		{"mixed", "Hello World", "HELLO WORLD"},
		{"already upper", "ABC", "ABC"},
		{"unicode", "héllo", "HÉLLO"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Upper(tt.in); got != tt.want {
				t.Errorf("Upper(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestLower(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", ""},
		{"ascii", "HELLO", "hello"},
		{"mixed", "Hello World", "hello world"},
		{"already lower", "abc", "abc"},
		{"unicode", "HÉLLO", "héllo"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Lower(tt.in); got != tt.want {
				t.Errorf("Lower(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestTrim(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", ""},
		{"no whitespace", "abc", "abc"},
		{"leading", "  abc", "abc"},
		{"trailing", "abc  ", "abc"},
		{"both", "  abc  ", "abc"},
		{"tabs and newlines", "\t\nabc\n\t", "abc"},
		{"internal preserved", "  a b c  ", "a b c"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Trim(tt.in); got != tt.want {
				t.Errorf("Trim(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestTruncate(t *testing.T) {
	tests := []struct {
		name string
		n    int
		in   string
		want string
	}{
		{"n negative", -1, "hello", ""},
		{"n zero", 0, "hello", ""},
		{"n equals len", 5, "hello", "hello"},
		{"n greater than len", 10, "hello", "hello"},
		{"ascii cut", 3, "hello", "hel"},
		{"empty input", 3, "", ""},
		// rune-safety: "héllo" — é is multi-byte (2 bytes), 5 runes total.
		{"multibyte cut keeps rune intact", 3, "héllo", "hél"},
		{"multibyte at boundary", 5, "héllo", "héllo"},
		// CJK: each character is 3 bytes in UTF-8, 4 runes total.
		{"cjk cut", 2, "你好世界", "你好"},
		{"cjk full", 4, "你好世界", "你好世界"},
		// emoji: each is a 4-byte rune.
		{"emoji cut", 2, "😀😁😂", "😀😁"},
		{"emoji full", 3, "😀😁😂", "😀😁😂"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Truncate(tt.in, tt.n); got != tt.want {
				t.Errorf("Truncate(%q, %d) = %q, want %q", tt.in, tt.n, got, tt.want)
			}
		})
	}
}

func TestJoin(t *testing.T) {
	tests := []struct {
		name string
		sep  string
		in   []string
		want string
	}{
		{"empty slice", ",", []string{}, ""},
		{"nil slice", ",", nil, ""},
		{"single element", ",", []string{"a"}, "a"},
		{"comma sep", ",", []string{"a", "b", "c"}, "a,b,c"},
		{"empty sep", "", []string{"a", "b", "c"}, "abc"},
		{"multichar sep", " - ", []string{"a", "b"}, "a - b"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Join(tt.in, tt.sep); got != tt.want {
				t.Errorf("Join(%v, %q) = %q, want %q", tt.in, tt.sep, got, tt.want)
			}
		})
	}
}

func TestPrintf(t *testing.T) {
	tests := []struct {
		name string
		v    any
		spec string
		rest []any
		want string
	}{
		{"float precision", 3.14159, "%.2f", nil, "3.14"},
		{"int with text", 5, "%d comments", nil, "5 comments"},
		{"string verb", "hi", "[%s]", nil, "[hi]"},
		{"multi-arg subject first", 1, "%d/%d", []any{3}, "1/3"},
		{"currency", 9.5, "$%.2f", nil, "$9.50"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Printf(tt.v, tt.spec, tt.rest...); got != tt.want {
				t.Errorf("Printf(%v, %q, %v) = %q, want %q", tt.v, tt.spec, tt.rest, got, tt.want)
			}
		})
	}
}

func TestUrlquery(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", ""},
		{"plain", "cats", "cats"},
		{"ampersand and equals", "a&b=c", "a%26b%3Dc"},
		{"space becomes plus", "50% off", "50%25+off"},
		{"fragment and question", "x#y?z", "x%23y%3Fz"},
		{"plus is encoded", "a+b", "a%2Bb"},
		{"unicode", "héllo", "h%C3%A9llo"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Urlquery(tt.in); got != tt.want {
				t.Errorf("Urlquery(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestDefault(t *testing.T) {
	tests := []struct {
		name     string
		fallback string
		in       string
		want     string
	}{
		{"empty uses fallback", "N/A", "", "N/A"},
		{"nonempty unchanged", "N/A", "value", "value"},
		{"empty fallback for empty input", "", "", ""},
		{"whitespace is not empty", "N/A", " ", " "},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Default(tt.in, tt.fallback); got != tt.want {
				t.Errorf("Default(%q, %q) = %q, want %q", tt.in, tt.fallback, got, tt.want)
			}
		})
	}
}

func TestDataURL(t *testing.T) {
	got := DataURL([]byte("PNGDATA"), "image/png")
	want := "data:image/png;base64," + base64.StdEncoding.EncodeToString([]byte("PNGDATA"))
	if got != want {
		t.Fatalf("DataURL = %q, want %q", got, want)
	}
	if empty := DataURL(nil, "image/gif"); empty != "data:image/gif;base64," {
		t.Fatalf("DataURL(nil) = %q, want %q", empty, "data:image/gif;base64,")
	}
}
