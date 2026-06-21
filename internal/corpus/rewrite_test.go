package corpus

import "testing"

func TestRewriteImportPath(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{
			name: "exact match",
			in:   `import "example.com/app"`,
			want: `import "corpustest/cases/x"`,
		},
		{
			name: "subpackage",
			in:   `import "example.com/app/ui"`,
			want: `import "corpustest/cases/x/ui"`,
		},
		{
			name: "leaves stdlib and gsx untouched",
			in:   `import ("context"; _ "github.com/gsxhq/gsx")`,
			want: `import ("context"; _ "github.com/gsxhq/gsx")`,
		},
		{
			name: "no false prefix match",
			in:   `import "example.com/application"`,
			want: `import "example.com/application"`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := string(rewriteImportPath([]byte(tc.in), "example.com/app", "corpustest/cases/x"))
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestRewriteImportPathEmptyOld(t *testing.T) {
	in := `import "anything"`
	if got := string(rewriteImportPath([]byte(in), "", "corpustest/cases/x")); got != in {
		t.Errorf("got %q, want unchanged", got)
	}
}
