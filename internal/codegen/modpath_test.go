package codegen

import "testing"

func TestModulePathFromGoMod(t *testing.T) {
	t.Parallel()
	cases := []struct{ name, src, want string }{
		{"plain", "module example.com/foo\n\ngo 1.26.1\n", "example.com/foo"},
		{"inline comment", "module example.com/foo // vanity import\n", "example.com/foo"},
		{"quoted path", "module \"example.com/foo\"\n", "example.com/foo"},
		{"with require block", "module example.com/foo\n\ngo 1.26.1\n\nrequire golang.org/x/mod v0.37.0\n", "example.com/foo"},
		{"no module directive", "go 1.26.1\n", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ModulePathFromGoMod([]byte(tc.src)); got != tc.want {
				t.Errorf("ModulePathFromGoMod(%q) = %q, want %q", tc.src, got, tc.want)
			}
		})
	}
}
