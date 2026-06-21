package wsnorm

import (
	"go/token"
	"reflect"
	"testing"

	"github.com/gsxhq/gsx/ast"
	"github.com/gsxhq/gsx/parser"
)

// --- normalizeText table (the load-bearing per-text rule) ---

func TestNormalizeText(t *testing.T) {
	tests := []struct {
		name string
		in   string
		out  string
		keep bool
	}{
		// All-whitespace with newline → DROP (cosmetic indentation).
		{"all-ws newline", "\n  ", "", false},
		{"all-ws CR", "\r\n\t", "", false},
		{"all-ws just newline", "\n", "", false},
		// All-whitespace without newline → single inline space.
		{"all-ws space", " ", " ", true},
		{"all-ws spaces", "   ", " ", true},
		{"all-ws tabs", "\t\t", " ", true},
		// Leading inline run (no newline) → one leading space.
		{"lead inline space", " world", " world", true},
		{"lead inline tab", "\tworld", " world", true},
		// Leading newline edge → no space.
		{"lead newline", "\nworld", "world", true},
		{"lead newline+indent", "\n  world", "world", true},
		// Trailing inline run (no newline) → one trailing space.
		{"trail inline space", "Hello,   ", "Hello, ", true},
		{"trail inline tab", "Hello\t", "Hello ", true},
		// Trailing newline edge → no space.
		{"trail newline", "world\n", "world", true},
		{"trail newline+indent", "world\n  ", "world", true},
		// Internal run collapse.
		{"internal collapse", "foo   bar", "foo bar", true},
		{"internal tabs", "foo\t\tbar", "foo bar", true},
		{"internal newline", "foo\nbar", "foo bar", true},
		// Multi-line join (lines trimmed, joined by one space, edges dropped).
		{"multi-line join", "\n  a\n  b\n", "a b", true},
		// Both edges inline.
		{"both inline edges", "  x  ", " x ", true},
		// Content-only unchanged.
		{"content only", "hello", "hello", true},
		{"content with single internal space", "a b", "a b", true},
		// Empty string: not all-whitespace by our rule? Empty has no newline and is
		// all-whitespace vacuously; treat as the no-newline all-ws → " ".
		// (Parser never emits empty Text; documented behavior.)
		{"empty", "", " ", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out, keep := normalizeText(tc.in)
			if out != tc.out || keep != tc.keep {
				t.Fatalf("normalizeText(%q) = (%q, %v), want (%q, %v)", tc.in, out, keep, tc.out, tc.keep)
			}
		})
	}
}

// normalizeText must be idempotent on its own output (when kept).
func TestNormalizeTextIdempotent(t *testing.T) {
	inputs := []string{
		"\n  ", " ", "   ", "\t", " world", "\nworld", "Hello,   ",
		"world\n", "foo   bar", "\n  a\n  b\n", "  x  ", "hello",
	}
	for _, in := range inputs {
		out, keep := normalizeText(in)
		if !keep {
			continue
		}
		out2, keep2 := normalizeText(out)
		if !keep2 || out2 != out {
			t.Fatalf("normalizeText not idempotent: %q → %q → (%q, keep=%v)", in, out, out2, keep2)
		}
	}
}

// --- helpers for AST-level tests ---

func parse(t *testing.T, body string) *ast.File {
	t.Helper()
	src := "package p\n\ncomponent C() {\n" + body + "\n}\n"
	f, err := parser.ParseFile(token.NewFileSet(), "t.gsx", src, 0)
	if err != nil {
		t.Fatalf("parse error: %v\nsrc:\n%s", err, src)
	}
	return f
}

// collectText returns every Text node's Value, in traversal order.
func collectText(f *ast.File) []string {
	var out []string
	ast.Inspect(f, func(n ast.Node) bool {
		if n == nil {
			return false
		}
		if t, ok := n.(*ast.Text); ok {
			out = append(out, t.Value)
		}
		return true
	})
	return out
}

func TestNormalizeBlockIndentationRemoved(t *testing.T) {
	f := parse(t, "<div>\n  <p>a</p>\n  <span>b</span>\n</div>")
	Normalize(f)
	got := collectText(f)
	// "a" and "b" survive; all indentation Text dropped.
	want := []string{"a", "b"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("text nodes = %#v, want %#v", got, want)
	}
}

func TestNormalizeInlineSpaceKept(t *testing.T) {
	// The parser emits the inline trailing space after "a" as Text "a "; wsnorm
	// must preserve that single significant space (no newline at the edge).
	f := parse(t, "a <b>y</b>")
	Normalize(f)
	got := collectText(f)
	want := []string{"a ", "y"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("text nodes = %#v, want %#v", got, want)
	}
}

func TestNormalizeNewlineEdgeDropped(t *testing.T) {
	// The parser emits Text "y\n" after <b>x</b>; wsnorm drops the trailing
	// newline edge (cosmetic indentation before the closing brace) → "y".
	f := parse(t, "<b>x</b>\ny")
	Normalize(f)
	got := collectText(f)
	want := []string{"x", "y"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("text nodes = %#v, want %#v", got, want)
	}
}

func TestNormalizeTrailingInlineBeforeInterp(t *testing.T) {
	f := parse(t, "Hello,   {name}")
	Normalize(f)
	got := collectText(f)
	want := []string{"Hello, "}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("text nodes = %#v, want %#v", got, want)
	}
}

// --- Preserve contexts ---

func TestPreservePre(t *testing.T) {
	f := parse(t, "<pre>  a\n  b</pre>")
	Normalize(f)
	got := collectText(f)
	want := []string{"  a\n  b"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("text nodes = %#v, want %#v", got, want)
	}
}

func TestPreserveTextarea(t *testing.T) {
	f := parse(t, "<textarea>\n x \n</textarea>")
	Normalize(f)
	got := collectText(f)
	want := []string{"\n x \n"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("text nodes = %#v, want %#v", got, want)
	}
}

func TestPreserveScript(t *testing.T) {
	f := parse(t, "<script>\n let x=1;\n</script>")
	Normalize(f)
	got := collectText(f)
	want := []string{"\n let x=1;\n"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("text nodes = %#v, want %#v", got, want)
	}
}

func TestPreserveStyle(t *testing.T) {
	f := parse(t, "<style>\n a{}\n</style>")
	Normalize(f)
	got := collectText(f)
	want := []string{"\n a{}\n"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("text nodes = %#v, want %#v", got, want)
	}
}

// A <pre> wrapping nested elements + indentation → all inner whitespace preserved
// (nested-preserve flag stays on through descendants).
func TestPreserveNested(t *testing.T) {
	f := parse(t, "<pre>\n  <code>\n    x\n  </code>\n</pre>")
	Normalize(f)
	got := collectText(f)
	want := []string{"\n  ", "\n    x\n  ", "\n"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("text nodes = %#v, want %#v", got, want)
	}
}

// --- MarkupAttr slot ---

func TestMarkupAttrSlotNormalized(t *testing.T) {
	f := parse(t, "<Panel header={ <h1>\n  Hi \n</h1> }/>")
	Normalize(f)
	got := collectText(f)
	// "\n  Hi \n" → "Hi" (newline edges dropped).
	want := []string{"Hi"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("text nodes = %#v, want %#v", got, want)
	}
}

// A <pre> inside a MarkupAttr slot is preserved (slot is fresh context, but the
// pre tag turns preserve back on within the slot).
func TestMarkupAttrSlotPreservesPre(t *testing.T) {
	f := parse(t, "<Panel header={ <pre>  a\n  b</pre> }/>")
	Normalize(f)
	got := collectText(f)
	want := []string{"  a\n  b"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("text nodes = %#v, want %#v", got, want)
	}
}

// --- Control flow ---

func TestControlFlowForBodyNormalized(t *testing.T) {
	// Inside <li>: the parser yields Text "\n " before {x} and " \n" after it;
	// both are all-whitespace-with-newline → dropped (cosmetic indentation).
	f := parse(t, "{ for _, x := range xs {\n  <li>\n {x} \n</li>\n} }")
	Normalize(f)
	got := collectText(f)
	want := []string(nil)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("text nodes = %#v, want %#v", got, want)
	}
}

// A control-flow body whose markup carries real content + indentation: the
// indentation collapses but content survives, proving the for body is walked.
func TestControlFlowForBodyContentSurvives(t *testing.T) {
	f := parse(t, "{ for _, x := range xs {\n  <li>item   {x}</li>\n} }")
	Normalize(f)
	got := collectText(f)
	// "item   " → "item " (internal/trailing inline run collapsed to one space).
	want := []string{"item "}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("text nodes = %#v, want %#v", got, want)
	}
}

// --- Idempotence at the AST level: twice == once ---

func TestNormalizeIdempotentAST(t *testing.T) {
	bodies := []string{
		"<div>\n  <p>a</p>\n  <span>b</span>\n</div>",
		"<b>x</b> y",
		"Hello,   {name}",
		"<pre>  a\n  b</pre>",
		"<Panel header={ <h1>\n  Hi \n</h1> }/>",
		"{ for _, x := range xs {\n  <li>\n {x} \n</li>\n} }",
		"<div>foo   bar\n  baz</div>",
	}
	for _, body := range bodies {
		t.Run(body, func(t *testing.T) {
			f := parse(t, body)
			Normalize(f)
			once := collectText(f)
			Normalize(f)
			twice := collectText(f)
			if !reflect.DeepEqual(once, twice) {
				t.Fatalf("not idempotent:\n once=%#v\ntwice=%#v", once, twice)
			}
		})
	}
}
