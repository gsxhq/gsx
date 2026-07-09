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
