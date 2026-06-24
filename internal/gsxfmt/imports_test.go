package gsxfmt

import (
	"strings"
	"testing"
)

func mustFormat(t *testing.T, src string, unused []ImportRef) string {
	t.Helper()
	out, err := FormatRemovingImports("x.gsx", []byte(src), unused)
	if err != nil {
		t.Fatalf("FormatRemovingImports: %v", err)
	}
	return string(out)
}

// TestRemoveSingleImport: a lone `import "strings"` is removed entirely.
func TestRemoveSingleImport(t *testing.T) {
	src := "package x\n\nimport \"strings\"\n\ncomponent C() {\n\t<p>hi</p>\n}\n"
	got := mustFormat(t, src, []ImportRef{{Path: "strings"}})
	if strings.Contains(got, "strings") {
		t.Fatalf("strings import not removed:\n%s", got)
	}
}

// TestRemoveOneOfBlock: one unused spec drops from an import block; the used one
// and the block survive.
func TestRemoveOneOfBlock(t *testing.T) {
	src := "package x\n\nimport (\n\t\"strings\"\n\t\"fmt\"\n)\n\ncomponent C() {\n\t<p>{ fmt.Sprint(1) }</p>\n}\n"
	got := mustFormat(t, src, []ImportRef{{Path: "strings"}})
	if strings.Contains(got, "\"strings\"") {
		t.Fatalf("strings not removed:\n%s", got)
	}
	if !strings.Contains(got, "\"fmt\"") {
		t.Fatalf("fmt wrongly removed:\n%s", got)
	}
}

// TestRemoveAllImports: removing the only spec leaves no empty import block.
func TestRemoveAllImports(t *testing.T) {
	src := "package x\n\nimport (\n\t\"strings\"\n)\n\ncomponent C() {\n\t<p>hi</p>\n}\n"
	got := mustFormat(t, src, []ImportRef{{Path: "strings"}})
	if strings.Contains(got, "import") {
		t.Fatalf("empty import block left behind:\n%s", got)
	}
}

// TestRemoveAliasedImport: an aliased import is removed by (name, path).
func TestRemoveAliasedImport(t *testing.T) {
	src := "package x\n\nimport sx \"strings\"\n\ncomponent C() {\n\t<p>hi</p>\n}\n"
	got := mustFormat(t, src, []ImportRef{{Name: "sx", Path: "strings"}})
	if strings.Contains(got, "strings") {
		t.Fatalf("aliased import not removed:\n%s", got)
	}
}

// TestBlankImportPreserved: a blank import is never in the unused set, so it
// survives even when another import is removed.
func TestBlankImportPreserved(t *testing.T) {
	src := "package x\n\nimport (\n\t_ \"embed\"\n\t\"strings\"\n)\n\ncomponent C() {\n\t<p>hi</p>\n}\n"
	got := mustFormat(t, src, []ImportRef{{Path: "strings"}})
	if !strings.Contains(got, "_ \"embed\"") {
		t.Fatalf("blank import wrongly removed:\n%s", got)
	}
}

// TestNoUnusedIsPlainFormat: empty unused ⇒ identical to Format.
func TestNoUnusedIsPlainFormat(t *testing.T) {
	src := "package x\n\nimport \"strings\"\n\ncomponent C() {\n\t<p>{ strings.ToUpper(\"x\") }</p>\n}\n"
	plain, err := Format("x.gsx", []byte(src))
	if err != nil {
		t.Fatal(err)
	}
	got := mustFormat(t, src, nil)
	if got != string(plain) {
		t.Fatalf("empty unused diverged from Format:\nremoving:\n%s\nformat:\n%s", got, plain)
	}
}

// TestRemoveIdempotent: removing then re-removing is stable.
func TestRemoveIdempotent(t *testing.T) {
	src := "package x\n\nimport \"strings\"\n\ncomponent C() {\n\t<p>hi</p>\n}\n"
	once := mustFormat(t, src, []ImportRef{{Path: "strings"}})
	twice := mustFormat(t, once, []ImportRef{{Path: "strings"}})
	if once != twice {
		t.Fatalf("not idempotent:\nonce:\n%s\ntwice:\n%s", once, twice)
	}
}
