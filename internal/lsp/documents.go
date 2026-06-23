package lsp

import (
	"net/url"
	"path/filepath"
	"strings"
	"sync"
)

type document struct {
	text    string
	version int
}

// docStore holds open document buffers keyed by URI. It is safe for concurrent
// use; slice 1 drives it from a single goroutine but the mutex keeps it honest.
type docStore struct {
	mu   sync.Mutex
	docs map[string]*document
}

func newDocStore() *docStore { return &docStore{docs: map[string]*document{}} }

func (s *docStore) open(uri, text string, version int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.docs[uri] = &document{text: text, version: version}
}

func (s *docStore) update(uri, text string, version int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.docs[uri] = &document{text: text, version: version}
}

func (s *docStore) close(uri string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.docs, uri)
}

func (s *docStore) text(uri string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.docs[uri]
	if !ok {
		return "", false
	}
	return d.text, true
}

// openInDir returns abs-file-path -> text for every open document whose
// containing directory equals dir.
func (s *docStore) openInDir(dir string) map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]string{}
	for uri, d := range s.docs {
		p := uriToPath(uri)
		if filepath.Dir(p) == dir {
			out[p] = d.text
		}
	}
	return out
}

// uriToPath converts a file:// URI to a local filesystem path, decoding percent
// escapes. Non-file URIs are returned unchanged.
func uriToPath(uri string) string {
	rest, ok := strings.CutPrefix(uri, "file://")
	if !ok {
		return uri
	}
	if decoded, err := url.PathUnescape(rest); err == nil {
		return decoded
	}
	return rest
}

// pathToURI converts an absolute filesystem path to a file:// URI, percent-
// escaping path segments.
func pathToURI(path string) string {
	u := &url.URL{Scheme: "file", Path: path}
	return u.String()
}
