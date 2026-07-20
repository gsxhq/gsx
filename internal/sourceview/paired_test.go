package sourceview

import (
	"path/filepath"
	"testing"
)

func TestPairedGSXPathUsesExactSiblingIdentity(t *testing.T) {
	dir := t.TempDir()
	for _, test := range []struct {
		name string
		path string
		want string
		ok   bool
	}{
		{name: "paired output", path: filepath.Join(dir, "page.x.go"), want: filepath.Join(dir, "page.gsx"), ok: true},
		{name: "ordinary Go", path: filepath.Join(dir, "page.go")},
		{name: "xgo prefix", path: filepath.Join(dir, "page.x.go.extra")},
		{name: "nested suffix", path: filepath.Join(dir, "page.x.x.go"), want: filepath.Join(dir, "page.x.gsx"), ok: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, ok := PairedGSXPath(test.path)
			if got != test.want || ok != test.ok {
				t.Fatalf("PairedGSXPath(%q) = (%q, %t), want (%q, %t)", test.path, got, ok, test.want, test.ok)
			}
		})
	}
}

func TestPairedGeneratedOutputPathUsesExactSiblingIdentity(t *testing.T) {
	dir := t.TempDir()
	gsx := filepath.Join(dir, "page.gsx")
	if got, ok := PairedGeneratedOutputPath(gsx); !ok || got != filepath.Join(dir, "page.x.go") {
		t.Fatalf("PairedGeneratedOutputPath(%q) = (%q, %t)", gsx, got, ok)
	}
	if got, ok := PairedGeneratedOutputPath(filepath.Join(dir, "page.go")); ok || got != "" {
		t.Fatalf("ordinary Go paired output = (%q, %t), want none", got, ok)
	}
}
