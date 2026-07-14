package modpath

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDirForImportPathRejectsFiles(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	file := filepath.Join(root, "renderer-file")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	for _, importPath := range []string{
		"example.com/app/renderer-file",
		"example.com/app/renderer-file/child",
	} {
		if got, ok := DirForImportPath(root, "example.com/app", importPath); ok || got != "" {
			t.Errorf("DirForImportPath(%q) = (%q, %v), want (\"\", false)", importPath, got, ok)
		}
	}
}

func TestDirForImportPathSymlinkTargets(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	insideDir := filepath.Join(root, "actual-renderers")
	insideFile := filepath.Join(root, "renderer-file")
	outsideDir := t.TempDir()
	if err := os.MkdirAll(insideDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(insideFile, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	insideAlias := filepath.Join(root, "inside-alias")
	outsideAlias := filepath.Join(root, "outside-alias")
	fileAlias := filepath.Join(root, "file-alias")
	brokenAlias := filepath.Join(root, "broken-alias")
	for alias, target := range map[string]string{
		insideAlias:  insideDir,
		outsideAlias: outsideDir,
		fileAlias:    insideFile,
		brokenAlias:  filepath.Join(root, "missing-target"),
	} {
		if err := os.Symlink(target, alias); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
	}

	insideMissing := filepath.Join(insideAlias, "future", "nested", "renderers")
	tests := []struct {
		name       string
		importPath string
		want       string
		wantOK     bool
	}{
		{name: "directory alias", importPath: "example.com/app/inside-alias", want: insideAlias, wantOK: true},
		{name: "multi-component missing suffix", importPath: "example.com/app/inside-alias/future/nested/renderers", want: insideMissing, wantOK: true},
		{name: "outside alias", importPath: "example.com/app/outside-alias"},
		{name: "missing suffix below outside alias", importPath: "example.com/app/outside-alias/future"},
		{name: "file alias", importPath: "example.com/app/file-alias"},
		{name: "child below file alias", importPath: "example.com/app/file-alias/child"},
		{name: "broken alias", importPath: "example.com/app/broken-alias"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := DirForImportPath(root, "example.com/app", tt.importPath)
			if got != tt.want || ok != tt.wantOK {
				t.Fatalf("DirForImportPath(%q) = (%q, %v), want (%q, %v)", tt.importPath, got, ok, tt.want, tt.wantOK)
			}
		})
	}
}

func TestDirForImportPathAliasedModuleRoot(t *testing.T) {
	t.Parallel()
	actualRoot := t.TempDir()
	aliasParent := t.TempDir()
	rootAlias := filepath.Join(aliasParent, "module-root")
	if err := os.Symlink(actualRoot, rootAlias); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	tests := []struct {
		name       string
		importPath string
		want       string
	}{
		{name: "root", importPath: "example.com/app", want: rootAlias},
		{name: "missing nested path", importPath: "example.com/app/future/renderers", want: filepath.Join(rootAlias, "future", "renderers")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := DirForImportPath(rootAlias, "example.com/app", tt.importPath)
			if !ok || got != tt.want {
				t.Fatalf("DirForImportPath(%q) = (%q, %v), want (%q, true)", tt.importPath, got, ok, tt.want)
			}
		})
	}
}
