package sourceview

import (
	"bytes"
	"testing"
)

func TestGoFileHeader(t *testing.T) {
	const base = "//go:build linux\n\npackage p\n\nimport (\n\t\"a\"\n\t\"b\"\n)\n\nfunc F() int { return 1 }\n"
	const cgo = "package p\n\n/*\n#include <x.h>\n*/\nimport \"C\"\n\nfunc F() {}\n"
	const noImports = "package p\n\nfunc F() int { return 1 }\n"
	tests := []struct {
		name  string
		a, b  string
		equal bool
	}{
		{"body edit", base, "//go:build linux\n\npackage p\n\nimport (\n\t\"a\"\n\t\"b\"\n)\n\nfunc F() int { return 2 }\n", true},
		{"added import", base, "//go:build linux\n\npackage p\n\nimport (\n\t\"a\"\n\t\"b\"\n\t\"c\"\n)\n\nfunc F() int { return 1 }\n", false},
		{"reordered imports", base, "//go:build linux\n\npackage p\n\nimport (\n\t\"b\"\n\t\"a\"\n)\n\nfunc F() int { return 1 }\n", false},
		{"build line changed", base, "//go:build darwin\n\npackage p\n\nimport (\n\t\"a\"\n\t\"b\"\n)\n\nfunc F() int { return 1 }\n", false},
		{"package renamed", base, "//go:build linux\n\npackage q\n\nimport (\n\t\"a\"\n\t\"b\"\n)\n\nfunc F() int { return 1 }\n", false},
		{"cgo preamble changed", cgo, "package p\n\n/*\n#include <y.h>\n*/\nimport \"C\"\n\nfunc F() {}\n", false},
		{"cgo body edit", cgo, "package p\n\n/*\n#include <x.h>\n*/\nimport \"C\"\n\nfunc F() { _ = 1 }\n", true},
		{"no imports body edit", noImports, "package p\n\nfunc F() int { return 2 }\n", true},
		{"no imports package renamed", noImports, "package q\n\nfunc F() int { return 1 }\n", false},
		{"body syntax error ignored", noImports, "package p\n\nfunc F( {{{\n", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := bytes.Equal(goFileHeader([]byte(tt.a)), goFileHeader([]byte(tt.b)))
			if got != tt.equal {
				t.Errorf("headers equal = %v, want %v", got, tt.equal)
			}
		})
	}

	t.Run("unparseable header is whole file", func(t *testing.T) {
		src := []byte("package p\n\nimport (\n\t\"a\"\n\nfunc F() {}\n")
		if got := goFileHeader(src); !bytes.Equal(got, src) {
			t.Errorf("goFileHeader = %q, want whole src", got)
		}
	})
	t.Run("header is a prefix through last import", func(t *testing.T) {
		got := string(goFileHeader([]byte(base)))
		want := "//go:build linux\n\npackage p\n\nimport (\n\t\"a\"\n\t\"b\"\n)"
		if got != want {
			t.Errorf("goFileHeader = %q, want %q", got, want)
		}
	})
}

func TestGoEmbedLines(t *testing.T) {
	src := []byte("package p\n\nimport _ \"embed\"\n\n//go:embed a.txt\nvar a string\n\nfunc F() {}\n\n\t//go:embed b/*\nvar b string\n")
	got := goEmbedLines(src)
	want := []string{"//go:embed a.txt", "\t//go:embed b/*"}
	if len(got) != len(want) {
		t.Fatalf("got %d lines %q, want %d", len(got), got, len(want))
	}
	for i := range want {
		if string(got[i]) != want[i] {
			t.Errorf("line %d = %q, want %q", i, got[i], want[i])
		}
	}
	if n := len(goEmbedLines([]byte("package p\n"))); n != 0 {
		t.Errorf("no directives: got %d lines", n)
	}
}
