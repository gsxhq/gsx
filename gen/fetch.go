package gen

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/mod/module"
)

// fetchTimeout bounds the whole fetchModuleFS operation (both the @latest
// lookup and the zip download, when a lookup is needed).
const fetchTimeout = 30 * time.Second

// latestInfo is the JSON body of a module proxy @latest response (only the
// field we need).
type latestInfo struct {
	Version string
}

// fetchModuleFS fetches modulePath at version (or its latest published
// version, when version is "" or "latest") from the module proxy at
// proxyBase, and returns an fs.FS rooted inside the "<module>@<version>/"
// prefix used by module zips — so callers see go.mod at the FS root — plus
// the resolved version string. Every error is wrapped with the URL that was
// tried and a hint to set GOPROXY or use --from <dir> instead, since a failed
// fetch is the one operational error a `gsx new` user is likely to hit
// offline or behind a firewall.
func fetchModuleFS(ctx context.Context, proxyBase, modulePath, version string) (fs.FS, string, error) {
	ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()

	escapedPath, err := module.EscapePath(modulePath)
	if err != nil {
		return nil, "", fmt.Errorf("invalid module path %q: %w", modulePath, err)
	}
	base := strings.TrimRight(proxyBase, "/")

	resolved := version
	if resolved == "" || resolved == "latest" {
		latestURL := base + "/" + escapedPath + "/@latest"
		body, err := fetchBytes(ctx, latestURL)
		if err != nil {
			return nil, "", fmt.Errorf("resolving latest version of %s: %w (set GOPROXY or use --from <dir>)", modulePath, err)
		}
		var info latestInfo
		if err := json.Unmarshal(body, &info); err != nil {
			return nil, "", fmt.Errorf("%s: parsing @latest response: %w (set GOPROXY or use --from <dir>)", latestURL, err)
		}
		resolved = info.Version
	}

	escapedVersion, err := module.EscapeVersion(resolved)
	if err != nil {
		return nil, "", fmt.Errorf("invalid version %q for %s: %w", resolved, modulePath, err)
	}

	zipURL := base + "/" + escapedPath + "/@v/" + escapedVersion + ".zip"
	data, err := fetchBytes(ctx, zipURL)
	if err != nil {
		return nil, "", fmt.Errorf("fetching %s: %w (set GOPROXY or use --from <dir>)", zipURL, err)
	}

	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, "", fmt.Errorf("%s: reading module zip: %w", zipURL, err)
	}

	// Module zip entries are rooted at "<module>@<version>/..." using the
	// module path and version exactly as written (not the proxy-escaped
	// forms) — see golang.org/x/mod/zip's format documentation.
	prefix := modulePath + "@" + resolved
	sub, err := fs.Sub(zr, prefix)
	if err != nil {
		return nil, "", fmt.Errorf("%s: module zip missing expected %s/ prefix: %w", zipURL, prefix, err)
	}
	return sub, resolved, nil
}

// fetchBytes issues one GET request and returns the response body, wrapping
// any failure (including a non-200 status) with the URL that was tried.
func fetchBytes(ctx context.Context, rawURL string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", rawURL, err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", rawURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: proxy returned %s", rawURL, resp.Status)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("%s: reading response body: %w", rawURL, err)
	}
	return body, nil
}

// localTemplateFS returns dir as an fs.FS for use as a `--from <dir>`
// template source, validating that it looks like a Go module template (a
// go.mod file at its root) before handing it to the scaffold/personalize
// pipeline.
func localTemplateFS(dir string) (fs.FS, error) {
	info, err := os.Stat(dir)
	if err != nil {
		return nil, fmt.Errorf("--from %s: %w", dir, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("--from %s: not a directory", dir)
	}
	if _, err := os.Stat(filepath.Join(dir, "go.mod")); err != nil {
		return nil, fmt.Errorf("--from %s: no go.mod found (not a Go module template)", dir)
	}
	return os.DirFS(dir), nil
}

// proxyBaseFromEnv resolves the module-proxy base URL to fetch templates
// from: the first https:// entry of the comma/pipe-separated GOPROXY
// environment variable, defaulting to https://proxy.golang.org when GOPROXY
// is unset or empty. GOPROXY=off (or a GOPROXY with no usable https://
// entry, e.g. "direct" alone) returns an error directing the caller to
// --from, since there is then no proxy this package can fetch a zip from.
func proxyBaseFromEnv() (string, error) {
	v := os.Getenv("GOPROXY")
	if v == "" {
		return "https://proxy.golang.org", nil
	}
	if v == "off" {
		return "", fmt.Errorf("GOPROXY=off: use --from <dir> to fetch a template from a local checkout instead")
	}
	for _, f := range strings.FieldsFunc(v, func(r rune) bool { return r == ',' || r == '|' }) {
		if f = strings.TrimSpace(f); strings.HasPrefix(f, "https://") {
			return f, nil
		}
	}
	return "", fmt.Errorf("GOPROXY=%q has no https:// entry: use --from <dir> to fetch a template from a local checkout instead", v)
}
