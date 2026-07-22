package lsp

import (
	"encoding/json"
	"go/token"
	"path/filepath"
	"strings"
)

// handleCompletion answers textDocument/completion for a .gsx file. .go files
// are gopls's to complete (null). Source-state problems (mid-edit breakage,
// package-clause mismatch) yield an empty list, never an error: completion is
// advisory and must fail soft.
func (s *Server) handleCompletion(f frame) error {
	var p completionParams
	if err := json.Unmarshal(f.Params, &p); err != nil {
		return s.reply(f.ID, nil)
	}
	if !s.diskViewValid {
		return s.reply(f.ID, emptyCompletion())
	}
	path := uriToPath(p.TextDocument.URI)
	if strings.HasSuffix(path, ".go") {
		return s.reply(f.ID, nil) // gopls owns .go completion
	}
	sources := s.sourceSnapshot()
	text, ok := sources.sourceString(path)
	if !ok {
		return s.reply(f.ID, emptyCompletion())
	}

	off := byteOffsetForPosition(text, p.Position.Line, p.Position.Character, s.enc)

	// Repair the (possibly mid-edit) buffer at the cursor, then classify the
	// cursor context over the repaired parse. Both use a fresh FileSet per request.
	r := repairAtCursor(token.NewFileSet(), path, text, off)
	cc := classifyCompletionContext(r, path, off)

	switch cc.kind {
	case ctxGoExpr, ctxSigType:
		return s.reply(f.ID, s.goContextCompletion(cc, path, text, off, r))
	case ctxPipeStage:
		return s.reply(f.ID, s.pipeStageCompletion(cc, path, text, off, r))
	case ctxTag:
		return s.reply(f.ID, s.tagCompletion(cc, path, text, off, r))
	case ctxAttrName:
		return s.reply(f.ID, s.attrNameCompletion(cc, path, text, off, r))
	case ctxAttrValue:
		return s.reply(f.ID, s.attrValueCompletion(cc, text, off))
	default:
		// Remaining contexts (ctxNone) offer nothing.
		return s.reply(f.ID, emptyCompletion())
	}
}

// goContextCompletion answers a Go identifier-position cursor: it runs one
// ephemeral warm analysis over the repaired buffer, bridges the cursor into the
// type-checked skeleton, and enumerates the visible scope-chain objects (plus
// statement keywords in GoBlock/GoChunk positions). Every failure mode — the
// analyzer erroring, a diagnostics-only package with no type info, or an
// unbridgeable cursor — yields an empty list, never an error: completion is
// advisory and must fail soft.
func (s *Server) goContextCompletion(cc completionContext, path, text string, off int, r repairResult) CompletionList {
	// PHANTOM SKELETON REPAIR — the SECOND completion patch site, distinct from
	// Task 6's gsx-parse chooser in repairAtCursor (completion_repair.go). A
	// trailing-dot member cursor like `{ user.▮ }` PARSES cleanly as gsx (the
	// chooser picks the empty patch), but the generated SKELETON carries a broken
	// selector `user.` that yields no member type info. Insert `_` at the cursor so
	// the skeleton carries a valid `user._` selector whose `_` Sel is an
	// empty-prefix member cursor. CROSS-TASK INVARIANT: this patches AT the cursor
	// only — bytes before off (all import lines included) never move — so the
	// bridge offsets computed against cc (over the original buffer) stay valid.
	// Guarded by r.patch != "_" so a chooser `_` repair is never doubled.
	src := r.src
	if cc.kind == ctxGoExpr && off > 0 && off <= len(text) && text[off-1] == '.' && r.patch != "_" && off <= len(src) {
		patched := make([]byte, 0, len(src)+1)
		patched = append(patched, src[:off]...)
		patched = append(patched, '_')
		patched = append(patched, src[off:]...)
		src = patched
	}
	// Non-blocking: this runs inline on the dispatch goroutine, so it must never
	// stall behind an in-flight background analysis. On not-acquired (contention)
	// a Go identifier cursor has no retained fallback worth serving — a stale
	// scope-chain would list objects the current buffer may no longer have — so
	// fail soft to an empty list, exactly as a shell/error result already does.
	eph, acquired, err := s.analyzer.AnalyzeEphemeralNonBlocking(filepath.Dir(path), path, src)
	if !acquired || err != nil || eph == nil || eph.Info == nil {
		return emptyCompletion()
	}
	// The classifier's fragment start in buffer-byte coordinates; the cursor's
	// in-fragment offset (off - exprStartOff) bridges into the ephemeral skeleton.
	exprStartOff := 0
	if r.fset != nil && cc.exprPos.IsValid() {
		exprStartOff = r.fset.Position(cc.exprPos).Offset
	}
	scope, skel, skelPos, statementCtx, ok := goCompletionBridge(eph, cc, exprStartOff, off, path)
	if !ok {
		return emptyCompletion()
	}
	start, end := completionTokenSpan(text, off, false)
	// Expected-type ranking (never filtering): a candidate whose type matches the
	// type expected at the cursor sorts ahead of the rest within its locality
	// tier. Fails silent (nil, no boost) outside the derivable subset.
	expected := expectedTypeAt(eph, cc, skel, skelPos, exprStartOff, path)
	items := goCompletionItems(eph, scope, skel, skelPos, statementCtx, expected, text, start, end, s.enc, path, src)
	if len(items) == 0 {
		return emptyCompletion()
	}
	return CompletionList{IsIncomplete: false, Items: items}
}

func emptyCompletion() CompletionList {
	return CompletionList{IsIncomplete: false, Items: []CompletionItem{}}
}
