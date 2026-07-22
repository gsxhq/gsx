package lsp

import (
	"encoding/json"
	"go/ast"
	"go/build"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// handleCompletionResolve answers completionItem/resolve (T10): the client
// sends back one CompletionItem — verbatim, Data included — that it received
// from an earlier textDocument/completion reply, and expects the same item
// back with Documentation filled in. Every failure mode replies the item
// UNCHANGED (fail soft: resolve is advisory, never an error) — no Data (the
// item's doc was already eager, or it carries none), a Data.File that fails
// the resolvablePath security gate, or a malformed Data payload (decoded
// separately as json.RawMessage below specifically so a malformed "data"
// value degrades to "item unchanged, Data dropped" rather than losing the
// whole item — see the field-shadowing comment on the anonymous struct).
//
// resolve is deliberately ANALYZER-FREE: Data{file,line} is a location the
// server itself already resolved via go/packages or //line-mapped skeleton
// coordinates at list-build time (see completion_go.go/completion_gsx.go), so
// answering it is pure file I/O — read the file (cached; dependency files are
// immutable within a session), parse it with comments, and extract the doc
// comment at that line. No Package/Analyzer lookup, no re-analysis.
func (s *Server) handleCompletionResolve(f frame) error {
	// raw.Data (json.RawMessage, depth 0) shadows the embedded
	// CompletionItem.Data (*completionResolveData, depth 1, same "data" json
	// tag) per encoding/json's shallowest-field-wins rule, so "data" always
	// decodes into raw.Data here regardless of its shape — a malformed Data
	// value can never fail the unmarshal of the REST of the item.
	var raw struct {
		CompletionItem
		Data json.RawMessage `json:"data,omitempty"`
	}
	if err := json.Unmarshal(f.Params, &raw); err != nil {
		return s.reply(f.ID, CompletionItem{})
	}
	item := raw.CompletionItem
	item.Data = nil
	if len(raw.Data) == 0 || string(raw.Data) == "null" {
		return s.reply(f.ID, item)
	}
	var data completionResolveData
	if err := json.Unmarshal(raw.Data, &data); err != nil {
		return s.reply(f.ID, item) // malformed Data: rest of the item unchanged
	}
	if !s.resolvablePath(data.File) {
		return s.reply(f.ID, item)
	}
	if doc, ok := s.depDocs.doc(data.File, data.Line); ok {
		item.Documentation = markdownDoc(doc)
	}
	return s.reply(f.ID, item)
}

// resolvablePath is the SECURITY gate for completionItem/resolve.
//
// THREAT MODEL: CompletionItem.Data round-trips through the CLIENT — that is
// the LSP contract (textDocument/completion emits it, completionItem/resolve
// receives it back unchanged). A hostile or merely buggy client is therefore
// free to send completionItem/resolve with an ARBITRARY {file,line} pair the
// server never emitted. If the handler blindly read whatever path it was
// given and returned the text found there as "documentation", it would be a
// generic file-exfiltration primitive: any file readable by the gsx-lsp
// process — source code, credentials, anything — could be read back through
// an otherwise read-only, advisory completion reply.
//
// THE GATE: only serve resolve for a path that is (a) an absolute path
// ending in ".go", AND (b) located under one of a small set of roots the
// server itself could ever have legitimately emitted a position under:
//   - GOMODCACHE (third-party dependency source, and — when the analyzed
//     module pins a toolchain via `go`/`toolchain` directives — the
//     downloaded toolchain's own stdlib source, which Go also places under
//     GOMODCACHE as golang.org/toolchain@..., not GOROOT)
//   - GOROOT (the running gsx binary's own toolchain stdlib, for the common
//     case where no separate toolchain download applies)
//   - every negotiated workspace's Go module roots (s.workspaceModules) — the
//     user's own source, already fully readable by every other LSP feature
//     (hover, go-to-definition, etc.)
//
// Anything else — an absolute path elsewhere on disk, a relative path, a
// path that Cleans to escape one of the roots via "..", or a non-.go suffix —
// is rejected outright, with NO file read attempted.
func (s *Server) resolvablePath(path string) bool {
	if path == "" || !strings.HasSuffix(path, ".go") || !filepath.IsAbs(path) {
		return false
	}
	clean := filepath.Clean(path)
	for _, root := range s.resolveRoots() {
		if root != "" && underRoot(clean, root) {
			return true
		}
	}
	return false
}

// underRoot reports whether clean path p is root itself or a descendant of
// root, comparing Cleaned paths so a "root/../elsewhere" escape is rejected
// (filepath.Rel below would report a leading ".." for it, which is excluded).
func underRoot(p, root string) bool {
	root = filepath.Clean(root)
	if p == root {
		return true
	}
	rel, err := filepath.Rel(root, p)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// resolveRoots returns the full allow-list for resolvablePath: GOMODCACHE,
// GOROOT, and this server's current negotiated workspace module roots.
// Recomputed on every call rather than memoized: goModCacheDir/build.Default
// are plain env/in-memory lookups (no filesystem access, no subprocess), so
// there is no measurable cost to paying it fresh per resolve request — and
// staying fresh is what lets GOMODCACHE/GOPATH be overridden per-test (or, in
// principle, mid-session) without a stale first-call value sticking for the
// rest of the process. GOROOT comes from go/build.Default.GOROOT (honors a
// GOROOT env override, else the toolchain's compiled-in default) rather than
// runtime.GOROOT() — deprecated since Go 1.24 precisely because it reflects
// the BUILDING toolchain's root, not necessarily the one that type-checked
// the analyzed module if the binary was copied elsewhere.
func (s *Server) resolveRoots() []string {
	roots := make([]string, 0, 2+len(s.workspaceModules))
	roots = append(roots, goModCacheDir(), build.Default.GOROOT)
	roots = append(roots, s.workspaceModules...)
	return roots
}

// goModCacheDir returns the effective GOMODCACHE directory without invoking
// `go env` (a subprocess per lookup is not warranted for a value that only
// changes with the environment): GOMODCACHE if explicitly set, otherwise Go's
// documented default of the first GOPATH entry's pkg/mod (GOPATH itself
// defaulting to ~/go when unset) — see `go help environment`. Returns "" only
// when even the home directory cannot be determined, in which case that empty
// root is simply never matched (resolvablePath skips empty roots).
func goModCacheDir() string {
	if v := os.Getenv("GOMODCACHE"); v != "" {
		return filepath.Clean(v)
	}
	gopath := os.Getenv("GOPATH")
	if gopath == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		gopath = filepath.Join(home, "go")
	}
	if i := strings.IndexRune(gopath, os.PathListSeparator); i >= 0 {
		gopath = gopath[:i]
	}
	if gopath == "" {
		return ""
	}
	return filepath.Join(gopath, "pkg", "mod")
}

// depDocCache caches completionItem/resolve's dependency-file doc lookups by
// absolute file path. Files under GOMODCACHE/GOROOT are immutable within a
// session (a module-cache entry is content-addressed; the running toolchain's
// stdlib does not change under a live server), so a hit never needs
// invalidation — this mirrors gopls's own module-cache caching assumption.
type depDocCache struct {
	mu      sync.Mutex
	byPath  map[string]*ast.File
	fileSet map[string]*token.FileSet // paired 1:1 with byPath, same key
}

func newDepDocCache() *depDocCache {
	return &depDocCache{byPath: map[string]*ast.File{}, fileSet: map[string]*token.FileSet{}}
}

// doc returns the doc-comment text of the declaration whose name identifier
// sits on 1-based line in the real Go file at absPath, parsing (and caching)
// the file on first use. ok=false when the file cannot be read/parsed, or no
// declaration's identifier lands on line, or that declaration has no doc
// comment.
func (c *depDocCache) doc(absPath string, line int) (string, bool) {
	f, fset, ok := c.parsed(absPath)
	if !ok {
		return "", false
	}
	lineOf := func(id *ast.Ident) (int, bool) {
		if id == nil {
			return 0, false
		}
		return fset.Position(id.Pos()).Line, true
	}
	text, found := declDocsByLine(f.Decls, lineOf)[line]
	return text, found
}

func (c *depDocCache) parsed(absPath string) (*ast.File, *token.FileSet, bool) {
	c.mu.Lock()
	f, hit := c.byPath[absPath]
	fset := c.fileSet[absPath]
	c.mu.Unlock()
	if hit {
		return f, fset, f != nil
	}

	fset = token.NewFileSet()
	f, err := parser.ParseFile(fset, absPath, nil, parser.ParseComments|parser.SkipObjectResolution)
	if err != nil {
		f = nil // cache the miss too — an unreadable/unparseable dep file stays that way
	}

	c.mu.Lock()
	c.byPath[absPath] = f
	c.fileSet[absPath] = fset
	c.mu.Unlock()
	return f, fset, f != nil
}
