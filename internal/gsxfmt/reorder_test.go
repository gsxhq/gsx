package gsxfmt

import (
	"strings"
	"testing"
)

// reorder formats src through the goimports path (reorder on, no removal).
func reorder(t *testing.T, src string) string {
	t.Helper()
	out, err := FormatWith("x.gsx", []byte(src), FormatOptions{Width: 80, Reorder: true})
	if err != nil {
		t.Fatalf("FormatWith: %v", err)
	}
	return string(out)
}

// TestReorderMergesAndDedups: a single-line import plus a grouped one that
// repeats it collapse into one block with the duplicate gone. This is the
// motivating case: gofmt leaves both declarations and the duplicate alone.
func TestReorderMergesAndDedups(t *testing.T) {
	src := "package x\n\n" +
		"import \"strings\"\n\n" +
		"import (\n\t\"fmt\"\n\n\t\"strings\"\n)\n\n" +
		"component C() {\n\t<p>{ fmt.Sprint(strings.ToUpper(\"x\")) }</p>\n}\n"
	got := reorder(t, src)
	if n := strings.Count(got, "import"); n != 1 {
		t.Fatalf("want exactly 1 import keyword, got %d:\n%s", n, got)
	}
	if n := strings.Count(got, "\"strings\""); n != 1 {
		t.Fatalf("duplicate strings import not deduped (%d occurrences):\n%s", n, got)
	}
	if !strings.Contains(got, "\"fmt\"") {
		t.Fatalf("fmt import lost:\n%s", got)
	}
}

// TestReorderGroupsStdAndThirdParty: std and non-std land in separate
// blank-line-separated groups, goimports' default two-group split.
func TestReorderGroupsStdAndThirdParty(t *testing.T) {
	src := "package x\n\n" +
		"import (\n\t\"github.com/gsxhq/gsx\"\n\t\"fmt\"\n)\n\n" +
		"component C() {\n\t<p>hi</p>\n}\n"
	got := reorder(t, src)
	fmtAt := strings.Index(got, "\"fmt\"")
	gsxAt := strings.Index(got, "\"github.com/gsxhq/gsx\"")
	if fmtAt < 0 || gsxAt < 0 {
		t.Fatalf("imports missing:\n%s", got)
	}
	if fmtAt > gsxAt {
		t.Fatalf("std must sort before third-party:\n%s", got)
	}
	between := got[fmtAt:gsxAt]
	if !strings.Contains(between, "\n\n") {
		t.Fatalf("want blank line between std and third-party groups:\n%s", got)
	}
}

// TestReorderIdempotent: reorder output is stable, including after the printer
// re-gofmt's every chunk (gofmt sorts within a group but never regroups, so the
// std/third-party split survives).
func TestReorderIdempotent(t *testing.T) {
	src := "package x\n\n" +
		"import \"strings\"\n\nimport (\n\t\"fmt\"\n\n\t\"strings\"\n)\n\n" +
		"component C() {\n\t<p>{ fmt.Sprint(strings.ToUpper(\"x\")) }</p>\n}\n"
	once := reorder(t, src)
	twice := reorder(t, once)
	if once != twice {
		t.Fatalf("reorder not idempotent:\nonce:\n%s\ntwice:\n%s", once, twice)
	}
}

// TestReorderOffLeavesImportsAlone: with Reorder:false the duplicate and the two
// separate declarations survive — that is gofmt mode.
func TestReorderOffLeavesImportsAlone(t *testing.T) {
	src := "package x\n\n" +
		"import \"strings\"\n\nimport (\n\t\"fmt\"\n\n\t\"strings\"\n)\n\n" +
		"component C() {\n\t<p>{ fmt.Sprint(strings.ToUpper(\"x\")) }</p>\n}\n"
	out, err := FormatWith("x.gsx", []byte(src), FormatOptions{Width: 80, Reorder: false})
	if err != nil {
		t.Fatal(err)
	}
	got := string(out)
	if n := strings.Count(got, "\"strings\""); n != 2 {
		t.Fatalf("gofmt mode must keep the duplicate (got %d):\n%s", n, got)
	}
	if n := strings.Count(got, "import"); n != 2 {
		t.Fatalf("gofmt mode must keep both import declarations (got %d):\n%s", n, got)
	}
}

// TestReorderPreservesBlankAndAliasedImports: `_` and aliased specs survive a
// reorder (FormatOnly never drops a spec).
func TestReorderPreservesBlankAndAliasedImports(t *testing.T) {
	src := "package x\n\n" +
		"import (\n\t_ \"embed\"\n\tsx \"strings\"\n)\n\n" +
		"component C() {\n\t<p>{ sx.ToUpper(\"x\") }</p>\n}\n"
	got := reorder(t, src)
	if !strings.Contains(got, "_ \"embed\"") {
		t.Fatalf("blank import lost:\n%s", got)
	}
	if !strings.Contains(got, "sx \"strings\"") {
		t.Fatalf("aliased import lost:\n%s", got)
	}
}

// TestReorderSamePathDifferentAliasBothKept: same path under two aliases is two
// distinct imports; dedup must not collapse them.
func TestReorderSamePathDifferentAliasBothKept(t *testing.T) {
	src := "package x\n\n" +
		"import (\n\tsx \"strings\"\n\tst \"strings\"\n)\n\n" +
		"component C() {\n\t<p>{ sx.ToUpper(st.ToLower(\"x\")) }</p>\n}\n"
	got := reorder(t, src)
	if !strings.Contains(got, "sx \"strings\"") || !strings.Contains(got, "st \"strings\"") {
		t.Fatalf("distinct aliases wrongly collapsed:\n%s", got)
	}
}

// TestChunkHasImportsIgnoresWordInStringsAndComments: the gate is AST-based, so
// the word "import" inside a string or comment must not trigger a reorder.
func TestChunkHasImportsIgnoresWordInStringsAndComments(t *testing.T) {
	if chunkHasImports("// import \"strings\"\nvar x = 1\n") {
		t.Fatal("comment mentioning import must not count as an import decl")
	}
	if chunkHasImports("var s = \"import \\\"strings\\\"\"\n") {
		t.Fatal("string containing import must not count as an import decl")
	}
	if !chunkHasImports("import \"strings\"\n") {
		t.Fatal("a real import decl must be detected")
	}
}

// TestReorderChunkImportsLeavesInvalidGoUntouched: a chunk that is not
// standalone-valid Go is returned unchanged rather than mangled.
func TestReorderChunkImportsLeavesInvalidGoUntouched(t *testing.T) {
	const bad = "import \"strings\"\n\nfunc ( {\n"
	got, changed := reorderChunkImports(bad)
	if changed || got != bad {
		t.Fatalf("invalid Go must be left untouched, got changed=%v:\n%s", changed, got)
	}
}

// TestDeleteChunkImportsPreservesGoBuild: go/printer relocates a //go:build
// comment ABOVE the synthetic package clause used internally to make a
// GoChunk parse standalone. deleteChunkImports must locate that clause by
// parsing, not by stripping "the first line" — otherwise the build constraint
// is deleted and "package _gsxp" leaks into the user's source.
func TestDeleteChunkImportsPreservesGoBuild(t *testing.T) {
	const src = "//go:build linux\n\nimport (\n\t\"bytes\"\n\t\"fmt\"\n)\n"
	got, changed := deleteChunkImports(src, []ImportRef{{Path: "bytes"}})
	if !changed {
		t.Fatalf("expected a change (bytes import removed)")
	}
	if !strings.Contains(got, "//go:build linux") {
		t.Fatalf("build constraint lost:\n%s", got)
	}
	if strings.Contains(got, "_gsxp") {
		t.Fatalf("synthetic package clause leaked:\n%s", got)
	}
	if strings.Contains(got, "\"bytes\"") {
		t.Fatalf("bytes import not removed:\n%s", got)
	}
}

// TestReorderChunkImportsPreservesGoBuild: the same relocation hazard as
// TestDeleteChunkImportsPreservesGoBuild, but through the goimports
// (imports.Process) path.
func TestReorderChunkImportsPreservesGoBuild(t *testing.T) {
	const src = "//go:build linux\n\nimport (\n\t\"bytes\"\n\n\t\"fmt\"\n)\n"
	got, _ := reorderChunkImports(src)
	if !strings.Contains(got, "//go:build linux") {
		t.Fatalf("build constraint lost:\n%s", got)
	}
	if strings.Contains(got, "_gsxp") {
		t.Fatalf("synthetic package clause leaked:\n%s", got)
	}
	if !strings.Contains(got, "\"bytes\"") || !strings.Contains(got, "\"fmt\"") {
		t.Fatalf("imports lost:\n%s", got)
	}
}

// TestReorderPreservesBlankLineBeforeNextDecl: a GoChunk's trailing blank line
// is a layout fact (internal/printer's endsWithBlankLine), not slack. A reorder
// that actually rewrites the chunk (here: the motivating dup-import case) must
// not collapse the blank line the author left before the next declaration.
func TestReorderPreservesBlankLineBeforeNextDecl(t *testing.T) {
	src := "package x\n\n" +
		"import \"strings\"\n\nimport (\n\t\"fmt\"\n\n\t\"strings\"\n)\n\n" +
		"component C() {\n\t<p>{ fmt.Sprint(strings.ToUpper(\"x\")) }</p>\n}\n"
	got := reorder(t, src)
	if !strings.Contains(got, ")\n\ncomponent C()") {
		t.Fatalf("blank line before component C() lost:\n%s", got)
	}
}

// TestRemovePreservesBlankLineBeforeNextDecl: same layout-fact requirement as
// TestReorderPreservesBlankLineBeforeNextDecl, but through the unused-import
// removal path (deleteChunkImports / FormatRemovingImports).
func TestRemovePreservesBlankLineBeforeNextDecl(t *testing.T) {
	src := "package x\n\nimport (\n\t\"strings\"\n\t\"fmt\"\n)\n\ncomponent C() {\n\t<p>{ fmt.Sprint(1) }</p>\n}\n"
	got := mustFormat(t, src, []ImportRef{{Path: "strings"}})
	if !strings.Contains(got, ")\n\ncomponent C()") {
		t.Fatalf("blank line before component C() lost:\n%s", got)
	}
}

// TestRemoveNoBlankLineStaysNoBlankLine: a chunk with NO trailing blank line
// (the import block runs straight into the next declaration) must not gain one
// after a rewrite — preserveTrailing must not manufacture layout that wasn't
// there.
func TestRemoveNoBlankLineStaysNoBlankLine(t *testing.T) {
	src := "package x\n\nimport (\n\t\"strings\"\n\t\"fmt\"\n)\ncomponent C() {\n\t<p>{ fmt.Sprint(1) }</p>\n}\n"
	got := mustFormat(t, src, []ImportRef{{Path: "strings"}})
	if strings.Contains(got, ")\n\ncomponent C()") {
		t.Fatalf("blank line wrongly introduced before component C():\n%s", got)
	}
	if !strings.Contains(got, ")\ncomponent C()") {
		t.Fatalf("expected no blank line between import block and component C():\n%s", got)
	}
}

// TestDeleteChunkImportsGoBuildIdempotent: re-running the go:build-preserving
// removal on its own output is a no-op.
func TestDeleteChunkImportsGoBuildIdempotent(t *testing.T) {
	const src = "//go:build linux\n\nimport (\n\t\"bytes\"\n\t\"fmt\"\n)\n"
	once, _ := deleteChunkImports(src, []ImportRef{{Path: "bytes"}})
	twice, changed := deleteChunkImports(once, []ImportRef{{Path: "bytes"}})
	if changed {
		t.Fatalf("second pass should find nothing left to remove")
	}
	if once != twice {
		t.Fatalf("not idempotent:\nonce:\n%s\ntwice:\n%s", once, twice)
	}
}

// TestReorderChunkImportsGoBuildIdempotent: re-running the go:build-preserving
// reorder on its own output is a no-op (goimports never regroups what it just
// grouped).
func TestReorderChunkImportsGoBuildIdempotent(t *testing.T) {
	const src = "//go:build linux\n\nimport (\n\t\"bytes\"\n\n\t\"fmt\"\n)\n"
	once, _ := reorderChunkImports(src)
	twice, changed := reorderChunkImports(once)
	if changed {
		t.Fatalf("second pass should find nothing left to reorder:\nonce:\n%s\ntwice:\n%s", once, twice)
	}
	if once != twice {
		t.Fatalf("not idempotent:\nonce:\n%s\ntwice:\n%s", once, twice)
	}
}

// TestFormatWithPreservesGoBuildThroughPrinter is the end-to-end regression
// test for the printer corruption bug: deleteChunkImports/reorderChunkImports
// preserve a //go:build comment when they rewrite a chunk's imports, but the
// GoChunk is then printed by internal/printer's Fprint — which independently
// runs every chunk through go/format via fmtGoChunk. Before fmtGoChunk wrapped
// the chunk in a synthetic package clause (making format.Source see a valid
// file, so it never enters its byte-count-stripping fragment mode), that
// second pass re-corrupted the comment even though gsxfmt's own import
// rewrite had left it intact. This must hold both with Reorder off (gofmt
// mode) and on (goimports mode).
func TestFormatWithPreservesGoBuildThroughPrinter(t *testing.T) {
	// The //go:build comment must lead a GoChunk that is NOT the file's own
	// package-clause decl: that first chunk already carries a real package
	// clause of its own, so format.Source parses it directly and never enters
	// the byte-stripping fragment path this test guards against. Attaching
	// the comment to the (import-bearing) second chunk instead reproduces the
	// shape go/format actually mishandles.
	src := "package x\n\n" +
		"//go:build linux\n\n" +
		"import \"strings\"\n\nimport (\n\t\"fmt\"\n\n\t\"strings\"\n)\n\n" +
		"component C() {\n\t<p>{ fmt.Sprint(strings.ToUpper(\"x\")) }</p>\n}\n"

	for _, reorder := range []bool{false, true} {
		out, err := FormatWith("x.gsx", []byte(src), FormatOptions{Width: 80, Reorder: reorder})
		if err != nil {
			t.Fatalf("Reorder=%v: FormatWith: %v", reorder, err)
		}
		got := string(out)
		if !strings.Contains(got, "//go:build linux") {
			t.Fatalf("Reorder=%v: build constraint lost or corrupted:\n%s", reorder, got)
		}
		for _, bad := range []string{"_gsxp", "_gsxfmt", "package p"} {
			if strings.Contains(got, bad) {
				t.Fatalf("Reorder=%v: synthetic package clause %q leaked:\n%s", reorder, bad, got)
			}
		}
	}
}
