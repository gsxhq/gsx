package gen

import (
	"os"
	"path/filepath"
	"testing"
)

func TestModuleRoot(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	os.WriteFile(filepath.Join(tmp, "go.mod"), []byte("module example.com/app\n\ngo 1.26\n"), 0o644)
	sub := filepath.Join(tmp, "a", "b")
	os.MkdirAll(sub, 0o755)
	root, modPath, err := moduleRoot(sub)
	if err != nil {
		t.Fatal(err)
	}
	if root != tmp {
		t.Errorf("root = %q, want %q", root, tmp)
	}
	if modPath != "example.com/app" {
		t.Errorf("modPath = %q, want example.com/app", modPath)
	}
}

func TestModuleDirForImportPath(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	nested := filepath.Join(root, "ui", "renderers")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(root, "not-a-dir")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name       string
		modulePath string
		importPath string
		want       string
		wantOK     bool
	}{
		{name: "module root", modulePath: "example.com/app", importPath: "example.com/app", want: root, wantOK: true},
		{name: "nested", modulePath: "example.com/app", importPath: "example.com/app/ui/renderers", want: nested, wantOK: true},
		{name: "external", modulePath: "example.com/app", importPath: "example.net/renderers"},
		{name: "empty module", importPath: "example.com/app"},
		{name: "empty import", modulePath: "example.com/app"},
		{name: "absolute import", modulePath: "example.com/app", importPath: "/example.com/app"},
		{name: "dot segment", modulePath: "example.com/app", importPath: "example.com/app/./renderers"},
		{name: "dotdot segment", modulePath: "example.com/app", importPath: "example.com/app/../sibling"},
		{name: "empty segment", modulePath: "example.com/app", importPath: "example.com/app//renderers"},
		{name: "backslash traversal", modulePath: "example.com/app", importPath: `example.com/app/..\sibling`},
		{name: "missing directory", modulePath: "example.com/app", importPath: "example.com/app/missing"},
		{name: "file", modulePath: "example.com/app", importPath: "example.com/app/not-a-dir"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := moduleDirForImportPath(root, tt.modulePath, tt.importPath)
			if ok != tt.wantOK || got != tt.want {
				t.Fatalf("moduleDirForImportPath(%q, %q, %q) = (%q, %v), want (%q, %v)", root, tt.modulePath, tt.importPath, got, ok, tt.want, tt.wantOK)
			}
		})
	}
}

func TestModuleDirForImportPathSymlinkContainment(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	inside := filepath.Join(root, "actual-renderers")
	outside := filepath.Join(filepath.Dir(root), "outside-renderers")
	for _, dir := range []string{inside, outside} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	insideLink := filepath.Join(root, "inside-link")
	outsideLink := filepath.Join(root, "outside-link")
	if err := os.Symlink(inside, insideLink); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if err := os.Symlink(outside, outsideLink); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	got, ok := moduleDirForImportPath(root, "example.com/app", "example.com/app/inside-link")
	if !ok || got != insideLink {
		t.Fatalf("in-module symlink = (%q, %v), want (%q, true)", got, ok, insideLink)
	}
	if got, ok := moduleDirForImportPath(root, "example.com/app", "example.com/app/outside-link"); ok || got != "" {
		t.Fatalf("outside symlink escape = (%q, %v), want (\"\", false)", got, ok)
	}
}
