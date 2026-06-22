package gen

import (
	"os"
	"path/filepath"
	"testing"
)

func TestModuleRoot(t *testing.T) {
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
