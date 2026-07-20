package lsp

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func writeWorkspaceFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeTestModule(t *testing.T, dir, modulePath string) string {
	t.Helper()
	writeWorkspaceFile(t, filepath.Join(dir, "go.mod"), "module "+modulePath+"\n\ngo 1.26.1\n")
	abs, err := filepath.Abs(dir)
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Clean(abs)
}

func TestDiscoverWorkspaceModules(t *testing.T) {
	t.Run("module root", func(t *testing.T) {
		root := writeTestModule(t, filepath.Join(t.TempDir(), "module"), "example.test/module")
		got, err := discoverWorkspaceModules([]string{root})
		if err != nil {
			t.Fatal(err)
		}
		if want := []string{root}; !slices.Equal(got, want) {
			t.Fatalf("modules = %v, want %v", got, want)
		}
	})

	t.Run("subdirectory belongs to nearest module", func(t *testing.T) {
		root := writeTestModule(t, filepath.Join(t.TempDir(), "module"), "example.test/module")
		subdir := filepath.Join(root, "internal", "page")
		if err := os.MkdirAll(subdir, 0o755); err != nil {
			t.Fatal(err)
		}
		got, err := discoverWorkspaceModules([]string{subdir})
		if err != nil {
			t.Fatal(err)
		}
		if want := []string{root}; !slices.Equal(got, want) {
			t.Fatalf("modules = %v, want %v", got, want)
		}
	})

	t.Run("go work contributes only explicit use directories", func(t *testing.T) {
		root := t.TempDir()
		first := writeTestModule(t, filepath.Join(root, "first"), "example.test/first")
		second := writeTestModule(t, filepath.Join(root, "second"), "example.test/second")
		_ = writeTestModule(t, filepath.Join(root, "nested-unlisted"), "example.test/unlisted")
		writeWorkspaceFile(t, filepath.Join(root, "go.work"), "go 1.26.1\n\nuse (\n\t./second\n\t./first\n)\n")

		got, err := discoverWorkspaceModules([]string{root})
		if err != nil {
			t.Fatal(err)
		}
		want := []string{first, second}
		slices.Sort(want)
		if !slices.Equal(got, want) {
			t.Fatalf("modules = %v, want explicit go.work uses %v", got, want)
		}
	})

	t.Run("duplicate canonical roots collapse", func(t *testing.T) {
		root := writeTestModule(t, filepath.Join(t.TempDir(), "module"), "example.test/module")
		subdir := filepath.Join(root, "child")
		if err := os.MkdirAll(subdir, 0o755); err != nil {
			t.Fatal(err)
		}
		got, err := discoverWorkspaceModules([]string{root, filepath.Join(root, "."), subdir})
		if err != nil {
			t.Fatal(err)
		}
		if want := []string{root}; !slices.Equal(got, want) {
			t.Fatalf("modules = %v, want %v", got, want)
		}
	})

	t.Run("nested module is not guessed", func(t *testing.T) {
		root := t.TempDir()
		_ = writeTestModule(t, filepath.Join(root, "nested"), "example.test/nested")
		got, err := discoverWorkspaceModules([]string{root})
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 0 {
			t.Fatalf("modules = %v, want no recursively guessed module", got)
		}
	})

	t.Run("nonexistent root is actionable", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "missing")
		_, err := discoverWorkspaceModules([]string{root})
		if err == nil || !strings.Contains(err.Error(), root) || !strings.Contains(err.Error(), "workspace root") {
			t.Fatalf("error = %v, want workspace root path", err)
		}
	})

	t.Run("malformed go work is actionable", func(t *testing.T) {
		root := t.TempDir()
		writeWorkspaceFile(t, filepath.Join(root, "go.work"), "go not-a-version\nuse (\n")
		_, err := discoverWorkspaceModules([]string{root})
		if err == nil || !strings.Contains(err.Error(), filepath.Join(root, "go.work")) {
			t.Fatalf("error = %v, want malformed go.work path", err)
		}
	})
}

func TestSetWorkspaceFoldersDecodesPercentEscapedLocalURI(t *testing.T) {
	root := writeTestModule(t, filepath.Join(t.TempDir(), "module with space"), "example.test/escaped")
	uri := pathToURI(root)
	if !strings.Contains(uri, "%20") {
		t.Fatalf("test URI %q does not contain a percent escape", uri)
	}
	server := NewServer(strings.NewReader(""), os.Stderr, nilAnalyzer{})
	if err := server.setWorkspaceFolders([]workspaceFolder{{URI: uri, Name: "escaped"}}); err != nil {
		t.Fatal(err)
	}
	if want := []string{root}; !slices.Equal(server.workspaceRoots, want) || !slices.Equal(server.workspaceModules, want) {
		t.Fatalf("roots/modules = %v/%v, want decoded %v", server.workspaceRoots, server.workspaceModules, want)
	}
	if want := []workspaceFolder{{URI: uri, Name: "escaped"}}; !slices.Equal(server.workspaceFolders, want) {
		t.Fatalf("folders = %+v, want normalized %+v", server.workspaceFolders, want)
	}
}

func TestNormalizeWorkspaceFolderAuthority(t *testing.T) {
	rootWithSpace := filepath.Join(t.TempDir(), "module with space")
	canonicalURI := pathToURI(rootWithSpace)
	for _, tt := range []struct {
		name string
		uri  string
	}{
		{name: "localhost is case insensitive", uri: strings.Replace(canonicalURI, "file://", "file://LOCALHOST", 1)},
		{name: "file scheme is case insensitive", uri: strings.Replace(canonicalURI, "file://", "FILE://LOCALHOST", 1)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			folder, path, err := normalizeWorkspaceFolder(workspaceFolder{URI: tt.uri, Name: "local"})
			if err != nil {
				t.Fatal(err)
			}
			if path != rootWithSpace {
				t.Fatalf("path = %q, want decoded %q", path, rootWithSpace)
			}
			if want := (workspaceFolder{URI: canonicalURI, Name: "local"}); folder != want {
				t.Fatalf("folder = %+v, want canonical %+v", folder, want)
			}
		})
	}

	root := filepath.Join(t.TempDir(), "module")
	tests := []struct {
		name string
		uri  string
	}{
		{name: "userinfo", uri: "file://user@localhost" + root},
		{name: "port", uri: "file://localhost:80" + root},
		{name: "remote host", uri: "file://remote.example" + root},
		{name: "non file scheme", uri: "https://localhost" + root},
		{name: "missing path", uri: "file://localhost"},
	}
	for _, tt := range tests {
		t.Run("rejects "+tt.name, func(t *testing.T) {
			_, _, err := normalizeWorkspaceFolder(workspaceFolder{URI: tt.uri, Name: tt.name})
			if err == nil || !strings.Contains(err.Error(), tt.uri) || !strings.Contains(err.Error(), "local file URI") {
				t.Fatalf("error = %v, want path-bearing local file URI rejection", err)
			}
		})
	}
}
