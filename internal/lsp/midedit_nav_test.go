package lsp

import (
	"go/token"
	"go/types"
	"strings"
	"testing"

	gsxast "github.com/gsxhq/gsx/ast"
	"github.com/gsxhq/gsx/internal/sourceintel"
	gsxparser "github.com/gsxhq/gsx/parser"
)

// TestAdjustRepairedSpan pins the repaired→live byte-span mapping used by the
// mid-edit go-to-definition / hover fallback. The repair inserted patchLen bytes
// at off; a returned span in repaired coordinates must map back to the live
// buffer before it is converted to an LSP range.
func TestAdjustRepairedSpan(t *testing.T) {
	const off, patchLen = 10, 3 // patch "_/>" inserted at offset 10
	cases := []struct {
		name               string
		start, end         int
		wantStart, wantEnd int
	}{
		// A span entirely before the insertion is untouched (identifier ending at
		// the cursor — the common tag-name hover range).
		{"before off", 4, 10, 4, 10},
		// A span entirely at/after the inserted patch shifts left by patchLen (a
		// control-clause identifier after the cursor).
		{"after patch", 13, 18, 10, 15},
		// A span starting before off but ending past the patch keeps its start and
		// pulls its end back (the identifier the patch landed inside).
		{"straddling the patch", 6, 15, 6, 12},
		// A degenerate span wholly inside the inserted patch clamps to the
		// insertion point (names no live byte).
		{"inside the patch", 11, 12, 10, 10},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			gotStart, gotEnd := adjustRepairedSpan(c.start, c.end, off, patchLen)
			if gotStart != c.wantStart || gotEnd != c.wantEnd {
				t.Fatalf("adjustRepairedSpan(%d,%d,%d,%d) = (%d,%d), want (%d,%d)",
					c.start, c.end, off, patchLen, gotStart, gotEnd, c.wantStart, c.wantEnd)
			}
		})
	}
}

// TestAdjustRepairedSpanOtherFileUntouched documents that the mapping is gated
// on the current (repaired) file at the snapshot boundary: a span belonging to
// another file is never passed to adjustRepairedSpan, so a target Location in a
// dependency .gsx keeps its coordinates. rangeForSpan applies the mapping only
// when span.Path == repairPath.
func TestAdjustRepairedSpanOtherFileUntouched(t *testing.T) {
	snap := &requestSourceSnapshot{
		enc:     encUTF16,
		sources: map[string]*capturedSource{},
	}
	// Two files with distinct content; the repair is armed for /m/a.gsx only.
	snap.sources["/m/a.gsx"] = &capturedSource{stringValue: "0123456789ABCDEF", hasString: true, ok: true}
	snap.sources["/m/dep.gsx"] = &capturedSource{stringValue: "0123456789ABCDEF", hasString: true, ok: true}
	snap.setRepair("/m/a.gsx", 4, 3)

	// A span in the OTHER file must not be shifted.
	depRange, ok := snap.rangeForSpan(sourceintel.Span{Path: "/m/dep.gsx", Start: 10, End: 12})
	if !ok {
		t.Fatal("dep range not ok")
	}
	want := rangeForSpan("0123456789ABCDEF", 10, 12, encUTF16)
	if depRange != want {
		t.Fatalf("dep-file span was adjusted: got %+v want %+v", depRange, want)
	}
	// A current-file span at/after the patch IS shifted (10 -> 7, 12 -> 9).
	curRange, ok := snap.rangeForSpan(sourceintel.Span{Path: "/m/a.gsx", Start: 10, End: 12})
	if !ok {
		t.Fatal("cur range not ok")
	}
	wantCur := rangeForSpan("0123456789ABCDEF", 7, 9, encUTF16)
	if curRange != wantCur {
		t.Fatalf("current-file span not adjusted: got %+v want %+v", curRange, wantCur)
	}
}

// ephemeralCountingAnalyzer records how many times AnalyzeEphemeral is invoked
// and returns a scripted Analyze result to populate s.pkgs via didOpen.
type ephemeralCountingAnalyzer struct {
	nilAnalyzer
	analyzed *Package
	ephCount *int
}

func (a ephemeralCountingAnalyzer) Analyze(string, map[string][]byte) (*Package, error) {
	return a.analyzed, nil
}

func (a ephemeralCountingAnalyzer) AnalyzeEphemeral(string, string, []byte) (*Package, error) {
	*a.ephCount++
	return nil, nil
}

// AnalyzeEphemeralNonBlocking is the variant the nav handlers now call. It
// always acquires (uncontended) and delegates to AnalyzeEphemeral, so the
// ephCount assertions in the fallback-gate tests are unchanged.
func (a ephemeralCountingAnalyzer) AnalyzeEphemeralNonBlocking(dir, path string, content []byte) (*Package, bool, error) {
	pkg, err := a.AnalyzeEphemeral(dir, path, content)
	return pkg, true, err
}

// tagAnsweringPackage hand-builds a Package whose retained facts answer
// go-to-definition on a same-package component tag (`<Row/>`) via CrossIndex —
// no ephemeral analysis required.
func tagAnsweringPackage(t *testing.T, path, text string) *Package {
	t.Helper()
	fset := token.NewFileSet()
	f, err := gsxparser.ParseFile(fset, path, []byte(text), 0)
	if err != nil {
		t.Fatal(err)
	}
	stampSyntacticComponents(f)
	rowNameOff := strings.Index(text, "component Row") + len("component ")
	decl := token.Position{Filename: path, Offset: rowNameOff}
	return &Package{
		GSXFset: fset,
		Fset:    fset,
		Info:    &types.Info{},
		Files:   map[string]*gsxast.File{path: f},
		CrossIndex: map[string]CrossRef{
			".Row": {Name: "Row", Decl: decl, Decls: []token.Position{decl}},
		},
	}
}

const midEditNavPage = "package x\n\ncomponent Page() {\n\t<Row/>\n}\n\ncomponent Row() {\n\t<div>hi</div>\n}\n"

// TestDefinitionFallbackNotConsultedWhenPrimaryAnswers is the scope guard: when
// the retained package answers go-to-definition (here, a component tag), the
// completion-repair + ephemeral-analysis fallback must never run — proven by a
// zero AnalyzeEphemeral count. It also confirms the primary answer is unchanged
// (a real Location).
func TestDefinitionFallbackNotConsultedWhenPrimaryAnswers(t *testing.T) {
	uri := "file:///m/a.gsx"
	path := "/m/a.gsx"
	count := 0
	a := ephemeralCountingAnalyzer{analyzed: tagAnsweringPackage(t, path, midEditNavPage), ephCount: &count}

	// Cursor on the "Row" of "<Row/>" (line 3).
	rowTagOff := strings.Index(midEditNavPage, "<Row/>") + 2 // on 'o'
	pos := positionForByteOffset(midEditNavPage, rowTagOff, encUTF16)
	out := drive(t, a, initFrame()+didOpenFrame(uri, midEditNavPage)+
		definitionFrame(2, uri, pos)+exitFrame())

	if count != 0 {
		t.Fatalf("AnalyzeEphemeral called %d times on an answered request; want 0", count)
	}
	result := responseByID(t, out, 2)["result"]
	if strings.TrimSpace(string(result)) == "null" {
		t.Fatalf("primary go-to-definition returned null; want the Row declaration Location")
	}
}

// contendedNavAnalyzer models the P4 contention case: the retained package
// answers primary nav, but the non-blocking ephemeral reports acquired=false.
// ephBlocking is what a *successful* ephemeral would return (must never be
// consulted); nbCalls/blkCalls count the two entry points so a test can prove
// the blocking body was skipped.
type contendedNavAnalyzer struct {
	nilAnalyzer
	analyzed    *Package
	ephBlocking *Package
	nbCalls     *int
	blkCalls    *int
}

func (a contendedNavAnalyzer) Analyze(string, map[string][]byte) (*Package, error) {
	return a.analyzed, nil
}

func (a contendedNavAnalyzer) AnalyzeEphemeral(string, string, []byte) (*Package, error) {
	*a.blkCalls++
	return a.ephBlocking, nil
}

func (a contendedNavAnalyzer) AnalyzeEphemeralNonBlocking(string, string, []byte) (*Package, bool, error) {
	*a.nbCalls++
	return nil, false, nil
}

// TestNavFallbackSkippedWhenContended pins the P4 nav policy: on a primary miss
// the fallback IS consulted (nbCalls >= 1), but under contention it skips the
// ephemeral pass entirely (blkCalls == 0 — the resolving ephBlocking package is
// never used) and replies null, exactly the pre-mid-edit-nav behavior. This is
// what turns the worst-case ~1.5 s dispatch-loop stall into an instant null.
func TestNavFallbackSkippedWhenContended(t *testing.T) {
	uri := "file:///m/a.gsx"
	path := "/m/a.gsx"
	nb, blk := 0, 0
	a := contendedNavAnalyzer{
		analyzed:    tagAnsweringPackage(t, path, midEditNavPage),
		ephBlocking: tagAnsweringPackage(t, path, midEditNavPage), // would resolve if consulted
		nbCalls:     &nb,
		blkCalls:    &blk,
	}

	// Cursor on the "hi" plain text (line 7) — primary misses, fallback runs.
	textOff := strings.Index(midEditNavPage, ">hi<") + 1
	pos := positionForByteOffset(midEditNavPage, textOff, encUTF16)
	out := drive(t, a, initFrame()+didOpenFrame(uri, midEditNavPage)+
		definitionFrame(2, uri, pos)+exitFrame())

	if nb == 0 {
		t.Fatal("non-blocking ephemeral never called; the fallback was not consulted")
	}
	if blk != 0 {
		t.Fatalf("blocking AnalyzeEphemeral called %d times under contention; the ephemeral body must be skipped", blk)
	}
	if r := strings.TrimSpace(string(responseByID(t, out, 2)["result"])); r != "null" {
		t.Fatalf("contended nav fallback result = %s, want null", r)
	}
}

// TestDefinitionFallbackConsultedOnPrimaryMiss is the complement: when the
// retained package matches nothing (cursor on plain text), the fallback IS
// consulted (AnalyzeEphemeral count >= 1), so the gate is not merely always-off.
func TestDefinitionFallbackConsultedOnPrimaryMiss(t *testing.T) {
	uri := "file:///m/a.gsx"
	path := "/m/a.gsx"
	count := 0
	a := ephemeralCountingAnalyzer{analyzed: tagAnsweringPackage(t, path, midEditNavPage), ephCount: &count}

	// Cursor on the "hi" plain text inside Row's <div> (line 7) — nothing to resolve.
	textOff := strings.Index(midEditNavPage, ">hi<") + 1 // on 'h'
	pos := positionForByteOffset(midEditNavPage, textOff, encUTF16)
	out := drive(t, a, initFrame()+didOpenFrame(uri, midEditNavPage)+
		definitionFrame(2, uri, pos)+exitFrame())

	if count == 0 {
		t.Fatalf("AnalyzeEphemeral never called on a primary miss; the fallback was not consulted")
	}
	if r := strings.TrimSpace(string(responseByID(t, out, 2)["result"])); r != "null" {
		t.Fatalf("expected null result on an unresolved cursor, got %s", r)
	}
}
