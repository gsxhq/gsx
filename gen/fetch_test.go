package gen

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// buildTestZip builds an in-memory module zip with the given
// "<module>@<version>/<rel>" -> content entries.
func buildTestZip(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range files {
		fw, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fw.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestFetchModuleFS(t *testing.T) {
	t.Parallel()
	zipData := buildTestZip(t, map[string]string{
		"example.com/tmpl@v0.1.0/go.mod":    "module example.com/tmpl\n\ngo 1.26\n",
		"example.com/tmpl@v0.1.0/main.go":   "package main\n\nfunc main() {}\n",
		"example.com/tmpl@v0.1.0/sub/f.txt": "hello\n",
	})

	mux := http.NewServeMux()
	mux.HandleFunc("/example.com/tmpl/@latest", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"Version": "v0.1.0"})
	})
	mux.HandleFunc("/example.com/tmpl/@v/v0.1.0.zip", func(w http.ResponseWriter, r *http.Request) {
		w.Write(zipData)
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	fsys, version, err := fetchModuleFS(context.Background(), server.URL, "example.com/tmpl", "latest")
	if err != nil {
		t.Fatalf("fetchModuleFS: %v", err)
	}
	if version != "v0.1.0" {
		t.Fatalf("resolved version = %q, want %q", version, "v0.1.0")
	}
	gomod, err := fs.ReadFile(fsys, "go.mod")
	if err != nil {
		t.Fatalf("reading go.mod at FS root: %v", err)
	}
	if !strings.Contains(string(gomod), "module example.com/tmpl") {
		t.Fatalf("go.mod = %s", gomod)
	}
	if _, err := fs.ReadFile(fsys, "main.go"); err != nil {
		t.Fatalf("reading main.go: %v", err)
	}
	sub, err := fs.ReadFile(fsys, "sub/f.txt")
	if err != nil {
		t.Fatalf("reading sub/f.txt: %v", err)
	}
	if string(sub) != "hello\n" {
		t.Fatalf("sub/f.txt = %q", sub)
	}
}

func TestFetchModuleFSExplicitVersion(t *testing.T) {
	t.Parallel()
	zipData := buildTestZip(t, map[string]string{
		"example.com/tmpl@v0.2.0/go.mod": "module example.com/tmpl\n",
	})
	mux := http.NewServeMux()
	mux.HandleFunc("/example.com/tmpl/@v/v0.2.0.zip", func(w http.ResponseWriter, r *http.Request) {
		w.Write(zipData)
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	fsys, version, err := fetchModuleFS(context.Background(), server.URL, "example.com/tmpl", "v0.2.0")
	if err != nil {
		t.Fatalf("fetchModuleFS: %v", err)
	}
	if version != "v0.2.0" {
		t.Fatalf("resolved version = %q, want %q", version, "v0.2.0")
	}
	if _, err := fs.ReadFile(fsys, "go.mod"); err != nil {
		t.Fatalf("reading go.mod: %v", err)
	}
}

// TestFetchModuleFSProxyOff exercises fetchModuleFS with an unusable
// (non-URL) proxyBase — the shape a caller would produce if it forwarded a
// raw "off"-ish GOPROXY value without resolving it via proxyBaseFromEnv
// first. This must fail fast (client-side, no real network attempt) with a
// wrapped, actionable error rather than hang or panic.
func TestFetchModuleFSProxyOff(t *testing.T) {
	t.Parallel()
	_, _, err := fetchModuleFS(context.Background(), "", "example.com/tmpl", "latest")
	if err == nil {
		t.Fatal("expected an error for an empty/unusable proxy base")
	}
	if !strings.Contains(err.Error(), "--from") {
		t.Fatalf("expected a --from hint in the error, got %q", err.Error())
	}
}

func TestFetchModuleFS404(t *testing.T) {
	t.Parallel()
	mux := http.NewServeMux()
	mux.HandleFunc("/example.com/missing/@latest", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	_, _, err := fetchModuleFS(context.Background(), server.URL, "example.com/missing", "latest")
	if err == nil {
		t.Fatal("expected an error for a 404 response")
	}
	if !strings.Contains(err.Error(), "example.com/missing/@latest") {
		t.Fatalf("expected the wrapped error to mention the URL tried, got %q", err.Error())
	}
}

func TestLocalTemplateFSValidation(t *testing.T) {
	t.Parallel()

	t.Run("missing dir", func(t *testing.T) {
		t.Parallel()
		_, err := localTemplateFS(filepath.Join(t.TempDir(), "does-not-exist"))
		if err == nil {
			t.Fatal("expected an error for a missing directory")
		}
	})

	t.Run("no go.mod", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		_, err := localTemplateFS(dir)
		if err == nil {
			t.Fatal("expected an error for a directory with no go.mod")
		}
		if !strings.Contains(err.Error(), "go.mod") {
			t.Fatalf("expected the error to mention go.mod, got %q", err.Error())
		}
	})

	t.Run("file, not a directory", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		f := filepath.Join(dir, "notadir")
		if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := localTemplateFS(f); err == nil {
			t.Fatal("expected an error when the path is a file, not a directory")
		}
	})

	t.Run("valid template dir", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		fsys, err := localTemplateFS(dir)
		if err != nil {
			t.Fatalf("localTemplateFS: %v", err)
		}
		if _, err := fs.ReadFile(fsys, "go.mod"); err != nil {
			t.Fatalf("reading go.mod through the returned FS: %v", err)
		}
		if _, err := fs.ReadFile(fsys, "main.go"); err != nil {
			t.Fatalf("reading main.go through the returned FS: %v", err)
		}
	})
}

func TestProxyBaseFromEnv(t *testing.T) {
	cases := []struct {
		name    string
		goproxy string
		unset   bool
		want    string
		wantErr bool
	}{
		{name: "unset defaults to proxy.golang.org", unset: true, want: "https://proxy.golang.org"},
		{name: "empty defaults to proxy.golang.org", goproxy: "", want: "https://proxy.golang.org"},
		{name: "off is an error", goproxy: "off", wantErr: true},
		{name: "single https entry", goproxy: "https://example.com", want: "https://example.com"},
		{name: "single http entry", goproxy: "http://127.0.0.1:12345", want: "http://127.0.0.1:12345"},
		{name: "comma-separated picks first https", goproxy: "direct,https://example.com,https://other.com", want: "https://example.com"},
		{name: "comma-separated picks first http", goproxy: "direct,http://127.0.0.1:12345,https://other.com", want: "http://127.0.0.1:12345"},
		{name: "pipe-separated picks first https", goproxy: "https://a.example|https://b.example", want: "https://a.example"},
		{name: "direct only is an error", goproxy: "direct", wantErr: true},
		{name: "mixed separators, https not first", goproxy: "direct|off,https://last.example", want: "https://last.example"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if c.unset {
				orig, had := os.LookupEnv("GOPROXY")
				os.Unsetenv("GOPROXY")
				t.Cleanup(func() {
					if had {
						os.Setenv("GOPROXY", orig)
					}
				})
			} else {
				t.Setenv("GOPROXY", c.goproxy)
			}
			got, err := proxyBaseFromEnv()
			if c.wantErr {
				if err == nil {
					t.Fatalf("expected an error for GOPROXY=%q", c.goproxy)
				}
				return
			}
			if err != nil {
				t.Fatalf("proxyBaseFromEnv: %v", err)
			}
			if got != c.want {
				t.Fatalf("proxyBaseFromEnv() = %q, want %q", got, c.want)
			}
		})
	}
}
