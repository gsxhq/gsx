package lsp

import (
	goast "go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"strconv"
	"strings"
	"sync"

	gsxast "github.com/gsxhq/gsx/ast"
)

// This file implements T9+T10 of the completion batch: Go doc comments on
// completion candidates. Documentation is doc-comment text ONLY (rendered as
// markdown via markdownDoc) — Detail already carries the rendered signature
// (goObjectPresentation), so Documentation never repeats it.
//
// Two independent mechanisms, split by where the declaring source lives:
//
//   - EAGER (authoredDoc, below): an object declared in the CURRENT package's
//     own analyzed .gsx source. The declaring GoChunk/GoWithElements is
//     already in memory (pkg.Files), so its doc is extracted synchronously and
//     placed directly on the CompletionItem. Reuses the EXACT byte-for-byte
//     recovery reconstruction textDocument/documentSymbol already proved
//     (reconstructGoChunk/reconstructGoWithElements in symbols.go) rather than
//     a second hand-rolled fragment parser — GoWithElements decls (a var
//     initializer with an embedded element) need that reconstruction's
//     newline-preserving placeholders to parse at all.
//
//   - LAZY (see completion_resolve.go): everything else — imported dependency
//     symbols, pipeline filters. Attaching CompletionItem.Data{file,line} at
//     list-build time and deferring the file read + parse to
//     completionItem/resolve keeps a full completion list (hundreds of
//     stdlib symbols) from paying a per-candidate file read+parse; only the
//     one item the user actually selects pays that cost.
//
// chunkDocCache/depDocCache below are content-addressed, so identical source
// text always yields the identical cached doc — safe to share across
// concurrent requests and even across process-wide test runs without any
// invalidation logic (a changed chunk simply misses the cache and
// overwrites the stale entry).

// declDocsByLine walks a *recovery-parsed* Go file's top-level declarations —
// funcs (including methods, via FuncDecl.Recv), types, vars, consts, and
// struct fields — and returns EVERY declaration's doc-comment group, keyed by
// its defining identifier's line (via lineOf). lineOf abstracts the
// position→line mapping so the same walk serves both the eager path (mapping
// back through a partialGoSource reconstruction to REAL .gsx lines) and a
// plain full-file parse (mapping directly via the parse's own FileSet, no
// reconstruction).
//
// Building the FULL map in one pass — rather than searching for one target
// line — matters beyond convenience: a single GoChunk/GoWithElements
// (equally, a single real .go file) commonly holds MULTIPLE declarations
// (e.g. a struct type immediately followed by a method on it, merged into one
// GoChunk with no Component decl between them to split it). Caching a single
// (chunk, targetLine) -> doc answer, keyed only by chunk identity/content,
// would let an unrelated declaration's doc leak onto a DIFFERENT line's
// lookup the moment both live in the same reconstructed text and the cache
// is consulted a second time for the other line — an actual bug caught while
// writing this: a struct field's lookup returned its sibling method's doc,
// because both hashed to the same cache entry. Computing the whole line->doc
// map once and caching THAT (chunkDocCache) removes the collision entirely.
//
// Doc-comment attachment mirrors go/ast + gofmt precedence: a GenDecl with
// exactly one Spec attributes ITS OWN leading Doc to that spec when the spec
// has no more specific Doc of its own (`// Foo is ...\nvar Foo = 1` records
// the comment as the GenDecl's Doc, not the ValueSpec's).
func declDocsByLine(decls []goast.Decl, lineOf func(*goast.Ident) (int, bool)) map[int]string {
	out := map[int]string{}
	set := func(id *goast.Ident, cg *goast.CommentGroup) {
		line, ok := lineOf(id)
		if !ok {
			return
		}
		if text := docText(cg); text != "" {
			out[line] = text
		}
	}
	for _, d := range decls {
		switch decl := d.(type) {
		case *goast.FuncDecl:
			if decl.Name != nil {
				set(decl.Name, decl.Doc)
			}
		case *goast.GenDecl:
			single := len(decl.Specs) == 1
			for _, sp := range decl.Specs {
				switch spec := sp.(type) {
				case *goast.ValueSpec:
					doc := spec.Doc
					if doc == nil && single {
						doc = decl.Doc
					}
					for _, name := range spec.Names {
						set(name, doc)
					}
				case *goast.TypeSpec:
					doc := spec.Doc
					if doc == nil && single {
						doc = decl.Doc
					}
					if spec.Name != nil {
						set(spec.Name, doc)
					}
					st, ok := spec.Type.(*goast.StructType)
					if !ok || st.Fields == nil {
						continue
					}
					for _, field := range st.Fields.List {
						for _, name := range field.Names {
							set(name, field.Doc)
						}
					}
				}
			}
		}
	}
	return out
}

// docText renders a possibly-nil comment group as trimmed markdown text (""
// for nil — CommentGroup.Text() already strips comment markers).
func docText(cg *goast.CommentGroup) string {
	if cg == nil {
		return ""
	}
	return strings.TrimSpace(cg.Text())
}

// coveringDeclReconstruction finds the .gsx File decl (GoChunk or
// GoWithElements) whose span covers 1-based REAL .gsx source line
// targetLine and reconstructs its verbatim Go text via the same proven
// byte-exact recovery reconstruction textDocument/documentSymbol uses.
// Returns ok=false when no covering decl is found or the reconstruction is
// rejected (a structural mismatch symbols.go would also reject).
// declLine is the covering decl's own span-START line — the chunkDocCache
// key, so edits inside a large chunk still hit the SAME cache slot.
func coveringDeclReconstruction(file *gsxast.File, fset *token.FileSet, source []byte, targetLine int) (reconstructed partialGoSource, tokenFile *token.File, declLine int, ok bool) {
	for _, d := range file.Decls {
		switch decl := d.(type) {
		case *gsxast.GoChunk:
			if !spanCoversLine(fset, decl.Pos(), decl.End(), targetLine) {
				continue
			}
			reconstructed, tokenFile, ok = reconstructGoChunk(fset, source, decl)
			return reconstructed, tokenFile, fset.Position(decl.Pos()).Line, ok
		case *gsxast.GoWithElements:
			if !spanCoversLine(fset, decl.Pos(), decl.End(), targetLine) {
				continue
			}
			reconstructed, tokenFile, ok = reconstructGoWithElements(fset, source, decl)
			return reconstructed, tokenFile, fset.Position(decl.Pos()).Line, ok
		}
	}
	return partialGoSource{}, nil, 0, false
}

// spanCoversLine reports whether 1-based line is actually spanned by
// [pos, end) — end treated as EXCLUSIVE, so the line of end itself is only
// counted when end owns at least one byte on it (end's column > 1).
// Naively using fset.Position(end).Line as the inclusive upper bound is
// wrong whenever a decl's exclusive end lands exactly at column 1 of the
// FOLLOWING line (the common case: a GoChunk immediately followed by a
// `component` decl, with no gap — end sits at the first byte of "component",
// which token.Position still reports as being ON that line): a real bug this
// caught, where a package-scope component's line was wrongly credited to the
// PRECEDING GoChunk, and its doc lookup was satisfied by the chunk's cache
// entry it happened to also collide with, returning the WRONG declaration's
// doc.
func spanCoversLine(fset *token.FileSet, pos, end token.Pos, line int) bool {
	lo := fset.Position(pos).Line
	if line < lo {
		return false
	}
	endPos := fset.Position(end)
	hi := endPos.Line
	if endPos.Column == 1 && hi > lo {
		hi-- // end owns no bytes on its own line; the decl's real last line is hi-1.
	}
	return line <= hi
}

// docsFromReconstruction parses partialGoWrapPrefix+reconstructed.text WITH
// comments and returns EVERY declaration's doc text, keyed by the REAL .gsx
// line its name identifier maps back to (via reconstructed.sourceOffset, the
// SAME position-mapping partialGoSymbols trusts for LSP navigation). A
// recovery parse can synthesize identifiers on malformed input, so —
// mirroring partialGoSymbols' mapIdent — every identifier position is proven
// to originate from one verbatim authored segment before it can match.
func docsFromReconstruction(sourceFset *token.FileSet, sourceTokenFile *token.File, reconstructed partialGoSource) map[int]string {
	gfset := token.NewFileSet()
	gf, _ := parser.ParseFile(gfset, "recovery.go", partialGoWrapPrefix+reconstructed.text.String(), parser.ParseComments|parser.AllErrors|parser.SkipObjectResolution)
	if gf == nil {
		return nil
	}
	parsedTokenFile := gfset.File(gf.Pos())
	if parsedTokenFile == nil {
		return nil
	}
	lineOf := func(ident *goast.Ident) (int, bool) {
		if ident == nil || gfset.File(ident.Pos()) != parsedTokenFile || gfset.File(ident.End()) != parsedTokenFile {
			return 0, false
		}
		parsedStart := parsedTokenFile.Offset(ident.Pos()) - len(partialGoWrapPrefix)
		parsedEnd := parsedTokenFile.Offset(ident.End()) - len(partialGoWrapPrefix)
		sourceStart, _, ok := reconstructed.linearSourceSpan(parsedStart, parsedEnd)
		if !ok || sourceStart < 0 || sourceStart > sourceTokenFile.Size() {
			return 0, false
		}
		return sourceFset.Position(sourceTokenFile.Pos(sourceStart)).Line, true
	}
	return declDocsByLine(gf.Decls, lineOf)
}

// completionDocFor computes the Documentation/Data pair for one completion
// candidate object — the single decision point for the design's uniform
// eager-vs-lazy rule: an object whose OWN declaring package is the package
// under analysis (obj.Pkg() == pkg.Types) is "authored" and gets eager
// Documentation (authoredDoc, extracted from the already-in-memory .gsx
// source); every other object with a resolvable position — an imported
// dependency's func/type/var/const, a pipeline filter's target func — gets a
// lazy Data{file,line} payload instead, deferring the file read+parse to
// completionItem/resolve. An object with no resolvable position (a builtin,
// `nil`, a synthesized object) gets neither.
//
// Applying the SAME object-identity check to every candidate class (package
// scope, imported package members, same-package struct/receiver members) is
// what makes "same-package MEMBER items follow the same rule" (decision #2)
// automatic: a member's authored-ness is a property of the member object
// itself, not of which completion path found it.
func completionDocFor(pkg *Package, obj types.Object, source []byte) (*MarkupContent, *completionResolveData) {
	if pkg == nil || pkg.Fset == nil || obj == nil || !obj.Pos().IsValid() {
		return nil, nil
	}
	if pkg.Types != nil && obj.Pkg() == pkg.Types {
		// Authored in THIS package: eager-only. A miss (the //line position
		// didn't map into a decl this pass could reconstruct — e.g. source
		// wasn't supplied) fails soft to no documentation: the resolved
		// position here is a .gsx path, never a valid completionItem/resolve
		// target (resolvablePath requires a real .go file), so there is no
		// useful Data to fall back to.
		if doc, ok := authoredDoc(pkg, obj, source); ok {
			return markdownDoc(doc), nil
		}
		return nil, nil
	}
	pos := pkg.Fset.Position(obj.Pos())
	if pos.Filename == "" || pos.Line <= 0 {
		return nil, nil
	}
	return nil, &completionResolveData{File: pos.Filename, Line: pos.Line}
}

// authoredDoc extracts obj's doc comment from THIS package's own analyzed
// .gsx source (caller has already checked obj.Pkg() == pkg.Types). This
// object-identity check matters beyond a cheap filter: pkg.Fset.Position can
// //line-map a DIFFERENT package's component-value object back to THAT
// package's .gsx (see objectSourceLocation's docs) — chasing that here would
// be wrong, because that source lives in the OTHER package's Files map, and
// pkg.Files only holds THIS package's own parsed files.
//
// source must be the exact bytes pkg.Files were parsed from (the analysis
// that produced pkg): reconstruction matches gsxast byte spans against it,
// so any divergence (a stale or different buffer) fails the reconstruction's
// own invariant checks and returns ok=false — never wrong output, just no doc.
func authoredDoc(pkg *Package, obj types.Object, source []byte) (string, bool) {
	if pkg == nil || pkg.GSXFset == nil || obj == nil || len(source) == 0 {
		return "", false
	}
	pos := pkg.Fset.Position(obj.Pos())
	if pos.Line <= 0 || !strings.HasSuffix(pos.Filename, ".gsx") {
		return "", false
	}
	file := pkg.Files[pos.Filename]
	if file == nil {
		return "", false
	}
	return globalChunkDocCache.doc(file, pkg.GSXFset, source, pos.Filename, pos.Line)
}

// chunkDocCache memoizes eager current-package doc extraction, content-keyed
// so a repeated completion request over an UNCHANGED declaration skips the
// recovery parse entirely. Keyed by (path, declaration start line); the
// cached reconstructed source text is compared on every lookup, so an edited
// declaration simply misses and overwrites — no version/generation tracking
// needed, and no possibility of serving stale text. Safe for concurrent use;
// shared process-wide (not per-Server), which is sound for the same reason:
// entries are self-validating by content, so cross-request/cross-test
// sharing only ever saves work, never returns a wrong answer.
type chunkDocCache struct {
	mu      sync.Mutex
	entries map[string]chunkDocEntry
	// parses counts actual recovery-parse calls (cache misses only) — test-only
	// observability for the "second completion doesn't re-parse" cache
	// contract (see chunkDocCache.doc).
	parses int
}

type chunkDocEntry struct {
	src  string         // the exact reconstructed source text this entry was parsed from
	docs map[int]string // every declaration's doc in this chunk, keyed by REAL .gsx line
}

var globalChunkDocCache = &chunkDocCache{entries: map[string]chunkDocEntry{}}

// doc returns the doc text for the declaration covering targetLine in file
// (an authored .gsx AST already resolved to belong to path), using fset+source
// for reconstruction. path+declLine key the cache; declLine is the SPAN START
// line of the covering decl (found once here, then reused as the cache key),
// and the cached VALUE is the whole chunk's line->doc map (not a single
// line's answer) — see declDocsByLine's doc comment for why caching a
// single-line answer is unsound whenever a chunk holds more than one
// declaration. An edit inside the chunk changes its reconstructed text, so
// the content check below simply misses and overwrites; no version tracking
// needed.
func (c *chunkDocCache) doc(file *gsxast.File, fset *token.FileSet, source []byte, path string, targetLine int) (string, bool) {
	reconstructed, tokenFile, declLine, ok := coveringDeclReconstruction(file, fset, source, targetLine)
	if !ok {
		return "", false
	}
	reconstructedSrc := reconstructed.text.String()
	key := path + "#" + strconv.Itoa(declLine)

	c.mu.Lock()
	entry, hit := c.entries[key]
	c.mu.Unlock()
	if hit && entry.src == reconstructedSrc {
		text, found := entry.docs[targetLine]
		return text, found
	}

	docs := docsFromReconstruction(fset, tokenFile, reconstructed)
	c.mu.Lock()
	c.entries[key] = chunkDocEntry{src: reconstructedSrc, docs: docs}
	c.parses++
	c.mu.Unlock()
	text, found := docs[targetLine]
	return text, found
}
