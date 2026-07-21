package lsp

import (
	"net/url"
	"path/filepath"
	"strings"
	"testing"
)

func TestURIPathRoundTrip(t *testing.T) {
	cases := []string{"/x/page.gsx", "/a b/c.gsx", "/p/ünïcode.gsx"}
	for _, p := range cases {
		if got := uriToPath(pathToURI(p)); got != p {
			t.Fatalf("round trip %q -> %q", p, got)
		}
	}
	if got := pathToURI("/x/page.gsx"); got != "file:///x/page.gsx" {
		t.Fatalf("pathToURI = %q", got)
	}
}

func TestLocalFileURIPathAuthorityMatrix(t *testing.T) {
	path := filepath.Join(t.TempDir(), "space ünicode", "pä ge.gsx")
	canonical := (&url.URL{Scheme: "file", Path: path}).String()
	localhost := (&url.URL{Scheme: "FILE", Host: "LOCALHOST", Path: path}).String()
	for _, raw := range []string{canonical, localhost} {
		got, err := localFileURIPath(raw)
		if err != nil {
			t.Fatalf("localFileURIPath(%q): %v", raw, err)
		}
		if got != filepath.Clean(path) {
			t.Fatalf("localFileURIPath(%q) = %q, want %q", raw, got, filepath.Clean(path))
		}
		if normalized := pathToURI(got); normalized != canonical {
			t.Fatalf("normalized URI = %q, want %q", normalized, canonical)
		}
	}

	root := filepath.Clean(t.TempDir())
	invalid := []string{
		"file://user@localhost" + root,
		"file://localhost:80" + root,
		"file://remote.example" + root,
		"https://localhost" + root,
		"file://localhost",
		"file:relative.gsx",
		"file://localhost/%zz",
		"file://localhost" + root + "?query=1",
		"file://localhost" + root + "#fragment",
		root,
	}
	for _, raw := range invalid {
		t.Run(strings.ReplaceAll(raw, "/", "_"), func(t *testing.T) {
			if path, err := localFileURIPath(raw); err == nil || path != "" {
				t.Fatalf("localFileURIPath(%q) = (%q, %v), want rejection", raw, path, err)
			}
			if path := uriToPath(raw); path != "" {
				t.Fatalf("uriToPath(%q) = %q, want fail-closed empty path", raw, path)
			}
		})
	}
}

func TestDocStoreCanonicalizesEquivalentLocalFileURIs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "space ünicode", "pä ge.gsx")
	canonical := pathToURI(path)
	localhost := (&url.URL{Scheme: "file", Host: "LOCALHOST", Path: path}).String()
	store := newDocStore()

	store.open(localhost, "open", 1)
	if got, ok := store.text(canonical); !ok || got != "open" {
		t.Fatalf("canonical lookup after localhost open = (%q, %t)", got, ok)
	}
	store.update(canonical, "changed", 2)
	if got, ok := store.text(localhost); !ok || got != "changed" {
		t.Fatalf("localhost lookup after canonical change = (%q, %t)", got, ok)
	}
	store.close("FILE://localhost" + (&url.URL{Path: path}).EscapedPath())
	if _, ok := store.text(canonical); ok {
		t.Fatal("equivalent localhost close left canonical document open")
	}

	store.open("file://remote.example/tmp/escape.gsx", "invalid", 1)
	if len(store.docs) != 0 || len(store.byDirSnapshot()) != 0 {
		t.Fatalf("invalid URI entered document store: docs=%v dirs=%v", store.docs, store.byDirSnapshot())
	}
}

func TestDocStoreOpenInDir(t *testing.T) {
	s := newDocStore()
	s.open(pathToURI("/proj/page.gsx"), "A", 1)
	s.open(pathToURI("/proj/card.gsx"), "B", 1)
	s.open(pathToURI("/other/x.gsx"), "C", 1)
	got := s.openInDir("/proj")
	if len(got) != 2 || got["/proj/page.gsx"] != "A" || got["/proj/card.gsx"] != "B" {
		t.Fatalf("openInDir = %v", got)
	}
	s.update(pathToURI("/proj/page.gsx"), "A2", 2)
	if txt, _ := s.text(pathToURI("/proj/page.gsx")); txt != "A2" {
		t.Fatalf("text after update = %q", txt)
	}
	s.close(pathToURI("/proj/page.gsx"))
	if _, ok := s.text(pathToURI("/proj/page.gsx")); ok {
		t.Fatal("doc still present after close")
	}
}
