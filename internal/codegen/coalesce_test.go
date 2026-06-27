package codegen

import (
	"strings"
	"testing"
)

// wrap puts body statements inside a minimal valid Go file so coalesceStaticWrites
// can parse it (it operates on generated-file source).
func wrap(body string) string {
	return "package p\nfunc f() {\n" + body + "\n}\n"
}

func TestCoalesceMergesAdjacentStaticWrites(t *testing.T) {
	t.Parallel()
	in := wrap("\t_gsxgw.S(\"<div\")\n\t_gsxgw.S(\">\")\n\t_gsxgw.S(\"x\")\n\t_gsxgw.S(\"</div>\")")
	got := string(coalesceStaticWrites([]byte(in)))
	if !strings.Contains(got, `_gsxgw.S("<div>x</div>")`) {
		t.Fatalf("adjacent static writes should merge into one S call; got:\n%s", got)
	}
	if strings.Count(got, "_gsxgw.S(") != 1 {
		t.Fatalf("expected exactly one S call after merge; got:\n%s", got)
	}
}

func TestCoalesceDynamicCallBreaksRun(t *testing.T) {
	t.Parallel()
	// A non-S call (gw.Node) between two static writes must NOT be swallowed.
	in := wrap("\t_gsxgw.S(\"a\")\n\t_gsxgw.Node(ctx, X())\n\t_gsxgw.S(\"b\")")
	got := string(coalesceStaticWrites([]byte(in)))
	if !strings.Contains(got, "_gsxgw.Node(ctx, X())") {
		t.Fatalf("dynamic call must survive; got:\n%s", got)
	}
	if strings.Count(got, "_gsxgw.S(") != 2 {
		t.Fatalf("the two S calls are not adjacent and must stay separate; got:\n%s", got)
	}
}

func TestCoalesceNonLiteralArgNotMerged(t *testing.T) {
	t.Parallel()
	// _gsxgw.S(string(raw)) and _gsxgw.S(strconv...) take non-literal args (RawCSS,
	// numeric formatting) and must never be merged into a string literal.
	in := wrap("\t_gsxgw.S(string(raw))\n\t_gsxgw.S(\"b\")")
	got := string(coalesceStaticWrites([]byte(in)))
	if !strings.Contains(got, "_gsxgw.S(string(raw))") {
		t.Fatalf("non-literal S arg must be preserved; got:\n%s", got)
	}
	if strings.Count(got, "_gsxgw.S(") != 2 {
		t.Fatalf("a non-literal S call must not merge with a literal one; got:\n%s", got)
	}
}

func TestCoalescePreservesLineDirective(t *testing.T) {
	t.Parallel()
	// A //line directive between two static writes must NOT be displaced/swallowed:
	// the run breaks at the comment, so both S calls remain (each keeps its mapping).
	in := wrap("\t_gsxgw.S(\"a\")\n//line input.gsx:4:7\n\t_gsxgw.S(\"b\")")
	got := string(coalesceStaticWrites([]byte(in)))
	if !strings.Contains(got, "//line input.gsx:4:7") {
		t.Fatalf("//line directive must be preserved; got:\n%s", got)
	}
	if strings.Count(got, "_gsxgw.S(") != 2 {
		t.Fatalf("a run must not merge across a //line directive; got:\n%s", got)
	}
}

func TestCoalesceMergesInsideSwitchCase(t *testing.T) {
	t.Parallel()
	// switch/select clause bodies are []Stmt, not *BlockStmt; their static writes
	// must coalesce too (the generator emits case bodies flat, not brace-wrapped).
	in := "package p\nfunc f() {\n\tswitch k {\n\tcase \"a\":\n\t\t_gsxgw.S(\"<b\")\n\t\t_gsxgw.S(\">x</b>\")\n\tdefault:\n\t\t_gsxgw.S(\"<i\")\n\t\t_gsxgw.S(\">y</i>\")\n\t}\n}\n"
	got := string(coalesceStaticWrites([]byte(in)))
	if !strings.Contains(got, `_gsxgw.S("<b>x</b>")`) {
		t.Fatalf("static writes in a switch case body should merge; got:\n%s", got)
	}
	if !strings.Contains(got, `_gsxgw.S("<i>y</i>")`) {
		t.Fatalf("static writes in a default clause body should merge; got:\n%s", got)
	}
}

func TestCoalesceRequotesEscapes(t *testing.T) {
	t.Parallel()
	// Merging must unquote/requote correctly so embedded quotes survive.
	in := wrap("\t_gsxgw.S(\"<a href=\\\"\")\n\t_gsxgw.S(\"x\")\n\t_gsxgw.S(\"\\\"\")")
	got := string(coalesceStaticWrites([]byte(in)))
	if !strings.Contains(got, `_gsxgw.S("<a href=\"x\"")`) {
		t.Fatalf("merged literal must requote embedded quotes; got:\n%s", got)
	}
}
