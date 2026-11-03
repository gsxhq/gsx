package lsp

import (
	"fmt"
	"net/url"
	"strings"
)

func normalizeWindowsDriveURIPath(path string) (string, bool) {
	if len(path) < 4 || path[0] != '/' || path[2] != ':' || path[3] != '/' || !isASCIILetter(path[1]) {
		return "", false
	}
	return string(toUpperASCII(path[1])) + path[2:], true
}

func normalizeWindowsDrivePathForURI(path string) (string, bool) {
	if len(path) < 3 || path[1] != ':' || path[2] != '/' || !isASCIILetter(path[0]) {
		return "", false
	}
	return "/" + string(toUpperASCII(path[0])) + path[1:], true
}

func isASCIILetter(b byte) bool {
	return b >= 'A' && b <= 'Z' || b >= 'a' && b <= 'z'
}

func toUpperASCII(b byte) byte {
	if b >= 'a' && b <= 'z' {
		return b - ('a' - 'A')
	}
	return b
}

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
	path, ok := fileURIPathToPath(parsed.Path)
	if !ok {
		return "", fmt.Errorf("not an absolute local file URI")
	}
	return path, nil
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
	uriPath, ok := filePathToURIPath(path)
	if !ok {
		return ""
	}
	return (&url.URL{Scheme: "file", Path: uriPath}).String()
}

func canonicalDocumentURI(raw string) (string, bool) {
	path, err := localFileURIPath(raw)
	if err != nil {
		return "", false
	}
	return pathToURI(path), true
}
