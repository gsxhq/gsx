package gen

import (
	"os"
	"path/filepath"
	"strings"
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

func TestModuleRootFailsClosedAtUnreadableNearestGoMod(t *testing.T) {
	for _, tt := range []struct {
		name  string
		setup func(*testing.T, string)
	}{
		{
			name: "directory",
			setup: func(t *testing.T, path string) {
				if err := os.MkdirAll(path, 0o755); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "broken symlink",
			setup: func(t *testing.T, path string) {
				if err := os.Symlink(filepath.Join(filepath.Dir(path), "missing.mod"), path); err != nil {
					t.Skipf("symlink unavailable: %v", err)
				}
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			parent := t.TempDir()
			if err := os.WriteFile(filepath.Join(parent, "go.mod"), []byte("module example.com/parent\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			child := filepath.Join(parent, "child")
			if err := os.MkdirAll(child, 0o755); err != nil {
				t.Fatal(err)
			}
			tt.setup(t, filepath.Join(child, "go.mod"))
			dir := filepath.Join(child, "views")
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}

			root, _, err := moduleRoot(dir)
			if err == nil || !strings.Contains(err.Error(), filepath.Join(child, "go.mod")) {
				t.Fatalf("moduleRoot = (%q, %v), want nearest go.mod read error", root, err)
			}
		})
	}
}

func TestModuleRootRejectsNearestGoModWithoutModuleDirective(t *testing.T) {
	parent := t.TempDir()
	if err := os.WriteFile(filepath.Join(parent, "go.mod"), []byte("module example.com/parent\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	child := filepath.Join(parent, "child")
	if err := os.MkdirAll(child, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(child, "go.mod"), []byte("go 1.26.1\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	root, modulePath, err := moduleRoot(child)
	if err == nil || !strings.Contains(err.Error(), "module directive") {
		t.Fatalf("moduleRoot = (%q, %q, %v), want missing module directive error", root, modulePath, err)
	}
}

func TestModuleRootRejectsMalformedNearestGoMod(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/app\n\nrequire (\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gotRoot, modulePath, err := moduleRoot(root)
	if err == nil || !strings.Contains(err.Error(), "parse") {
		t.Fatalf("moduleRoot = (%q, %q, %v), want malformed go.mod rejection", gotRoot, modulePath, err)
	}
}

func TestModuleDirForImportPath(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	nested := filepath.Join(root, "ui", "renderers")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{"bad name", "trailing.", "a..b", "CON", "bad~1"} {
		if err := os.Mkdir(filepath.Join(root, rel), 0o755); err != nil {
			t.Fatal(err)
		}
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
		{name: "invalid module grammar", modulePath: "example.com/bad module", importPath: "example.com/bad module"},
		{name: "space", modulePath: "example.com/app", importPath: "example.com/app/bad name"},
		{name: "trailing dot", modulePath: "example.com/app", importPath: "example.com/app/trailing."},
		{name: "windows reserved", modulePath: "example.com/app", importPath: "example.com/app/CON"},
		{name: "windows short name", modulePath: "example.com/app", importPath: "example.com/app/bad~1"},
		{name: "consecutive dots accepted by xmod", modulePath: "example.com/app", importPath: "example.com/app/a..b", want: filepath.Join(root, "a..b"), wantOK: true},
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
