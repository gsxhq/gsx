package std

import "testing"

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
			f := Truncate(tt.n)
			if got := f(tt.in); got != tt.want {
				t.Errorf("Truncate(%d)(%q) = %q, want %q", tt.n, tt.in, got, tt.want)
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
			f := Join(tt.sep)
			if got := f(tt.in); got != tt.want {
				t.Errorf("Join(%q)(%v) = %q, want %q", tt.sep, tt.in, got, tt.want)
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
			f := Default(tt.fallback)
			if got := f(tt.in); got != tt.want {
				t.Errorf("Default(%q)(%q) = %q, want %q", tt.fallback, tt.in, got, tt.want)
			}
		})
	}
}
