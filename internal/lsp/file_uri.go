package lsp

import (
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
)

// localFileURIPath decodes one absolute local file URI. Empty authority and
// localhost are the only local identities; every other URI fails closed.
func localFileURIPath(raw string) (string, error) {
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("parse URI: %w", err)
	}
	if !strings.EqualFold(parsed.Scheme, "file") || parsed.Opaque != "" || parsed.User != nil ||
		parsed.Port() != "" || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" {
		return "", fmt.Errorf("not an absolute local file URI")
	}
	if parsed.Host != "" {
		hostname := parsed.Hostname()
		if !strings.EqualFold(hostname, "localhost") || !strings.EqualFold(parsed.Host, hostname) {
			return "", fmt.Errorf("not an absolute local file URI")
		}
	}
	path := filepath.FromSlash(parsed.Path)
	if path == "" || !filepath.IsAbs(path) {
		return "", fmt.Errorf("not an absolute local file URI")
	}
	return filepath.Clean(path), nil
}

// uriToPath is the fail-closed compatibility form used by request handlers.
// Invalid or non-local URIs never become relative paths such as ".".
func uriToPath(raw string) string {
	path, err := localFileURIPath(raw)
	if err != nil {
		return ""
	}
	return path
}

// pathToURI converts an absolute filesystem path to one canonical file URI.
func pathToURI(path string) string {
	if path == "" || !filepath.IsAbs(path) {
		return ""
	}
	return (&url.URL{Scheme: "file", Path: filepath.Clean(path)}).String()
}

func canonicalDocumentURI(raw string) (string, bool) {
	path, err := localFileURIPath(raw)
	if err != nil {
		return "", false
	}
	return pathToURI(path), true
}
