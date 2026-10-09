package sourceview

import (
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestVanishedDuringWalk(t *testing.T) {
	root := "/m"
	for _, tc := range []struct {
		path string
		err  error
		want bool
	}{
		{"/m/a/node_modules/x", fs.ErrNotExist, true},
		{"/m/a", &fs.PathError{Op: "open", Path: "/m/a", Err: syscall.ENOENT}, true},
		{"/m", fs.ErrNotExist, false},     // the root itself missing is an error
		{"/m/a", fs.ErrPermission, false}, // unreadable is an error
	} {
		if got := vanishedDuringWalk(root, tc.path, tc.err); got != tc.want {
			t.Errorf("vanishedDuringWalk(%s, %v) = %v, want %v", tc.path, tc.err, got, tc.want)
		}
	}
}

func TestDirFilter(t *testing.T) {
	root := t.TempDir()
	write := func(rel, content string) {
		t.Helper()
		path := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module example.com/app\n\ngo 1.25\n\nignore (\n\t./frontend/node_cache\n\tgenerated/out\n\t./examples\n)\n")
	write("nested/go.mod", "module example.com/nested\n\ngo 1.25\n\nignore ./private\n")
	write("broken/go.mod", "module example.com/broken\n\nthis is not go.mod syntax\n")
	for _, dir := range []string{
		"views", ".claude/worktrees/w", "_tools", "frontend/node_cache/pkg", "frontend/src",
		"a/generated/out/x", "generated", "examples/demo", "nested/private", "nested/views",
		"broken/views", "frontend/node_cachex",
	} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	f := NewDirFilter()
	for _, tc := range []struct {
		dir  string
		want bool
	}{
		{"views", false},
		{".claude", true},      // dot-prefixed name (the walk stops there)
		{"node_modules", true}, // excluded names
		{"vendor", true},
		{"testdata", true},
		{"_tools", false},                 // underscore dirs stay: importable
		{"frontend/node_cache", true},     // ./x at the module root
		{"frontend/node_cache/pkg", true}, // and its subtree
		{"frontend/node_cachex", false},   // a whole element, not a prefix
		{"frontend/src", false},
		{"a/generated/out", true}, // x at any depth
		{"a/generated/out/x", true},
		{"generated", false},
		{"examples/demo", true}, // parent ignores a nested module dir
		{"nested", false},
		{"nested/private", true}, // a nested module's own ignore
		{"nested/views", false},
		{"broken/views", false}, // unparsable go.mod ignores nothing
	} {
		if got := f.Excluded(filepath.Join(root, tc.dir)); got != tc.want {
			t.Errorf("Excluded(%s) = %v, want %v", tc.dir, got, tc.want)
		}
	}
}
