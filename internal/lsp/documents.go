package lsp

import (
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

func (d *docStore) byDirSnapshot() map[string]bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	dirs := make(map[string]bool)
	for uri := range d.docs {
		dirs[filepath.Dir(uriToPath(uri))] = true
	}
	return dirs
}

func newDocStore() *docStore { return &docStore{docs: map[string]*document{}} }

func (s *docStore) open(uri, text string, version int) {
	uri, ok := canonicalDocumentURI(uri)
	if !ok {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.docs[uri] = &document{text: text, version: version}
}

func (s *docStore) update(uri, text string, version int) {
	uri, ok := canonicalDocumentURI(uri)
	if !ok {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.docs[uri] = &document{text: text, version: version}
}

func (s *docStore) close(uri string) {
	uri, ok := canonicalDocumentURI(uri)
	if !ok {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.docs, uri)
}

func (s *docStore) text(uri string) (string, bool) {
	uri, ok := canonicalDocumentURI(uri)
	if !ok {
		return "", false
	}
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

// docSnap is the retained content and version of one open document.
type docSnap struct {
	text    string
	version int
}

// snapshotDir returns abs-file-path -> {text, version} for every open document
// whose containing directory equals dir. Unlike openInDir it carries the version
// so a publish can be version-tagged (the editor drops stale-version publishes).
func (s *docStore) snapshotDir(dir string) map[string]docSnap {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]docSnap{}
	for uri, d := range s.docs {
		p := uriToPath(uri)
		if filepath.Dir(p) == dir {
			out[p] = docSnap{text: d.text, version: d.version}
		}
	}
	return out
}

// allOpenGSX returns abs-file-path -> bytes for every open .gsx document, for
// whole-module analysis overrides (unsaved buffers across the module).
func (s *docStore) allOpenGSX() map[string][]byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string][]byte{}
	for uri, d := range s.docs {
		p := uriToPath(uri)
		if strings.HasSuffix(p, ".gsx") {
			out[p] = []byte(d.text)
		}
	}
	return out
}
