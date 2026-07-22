package lsp

import "testing"

func TestCompletionTokenSpan(t *testing.T) {
	cases := []struct {
		name               string
		text               string
		off                int
		allowDash          bool
		wantStart, wantEnd int
	}{
		{"mid ident", "{ user.Na }", 9, false, 7, 9}, // token "Na"
		{"after dot", "{ user. }", 7, false, 7, 7},   // empty token at cursor
		{"attr dash", "<div hx-ge", 10, true, 5, 10}, // token "hx-ge"
		{"start of file", "ab", 2, false, 0, 2},      // whole buffer is the token
		{"utf8 before token", "é{x", 4, false, 3, 4}, // multi-byte rune earlier in the
		// buffer must not perturb byte-offset scanning of the ascii token "x"
		// (a rune-count-based scanner would miscompute this).
		{"unicode ident rune", "{ café", 7, false, 2, 7}, // token "café": scan must decode
		// the trailing 2-byte 'é' as an identifier rune, not stop mid-sequence.
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotStart, gotEnd := completionTokenSpan(tc.text, tc.off, tc.allowDash)
			if gotStart != tc.wantStart || gotEnd != tc.wantEnd {
				t.Fatalf("completionTokenSpan(%q, %d, %v) = (%d, %d), want (%d, %d)",
					tc.text, tc.off, tc.allowDash, gotStart, gotEnd, tc.wantStart, tc.wantEnd)
			}
		})
	}
}

// TestNewCompletionItemEdit pins the TextEdit produced for a token preceded by
// a surrogate-pair rune (𝔘 U+1D518, astral plane, encoded as 2 UTF-16 code
// units / 4 UTF-8 bytes) so a future regression to byte-counting in either
// encoding is caught.
//
// text (byte offsets):
//
//	{   0
//	sp  1
//	𝔘   2-5  (4 UTF-8 bytes; U+1D518 > U+FFFF so 2 UTF-16 units)
//	x   6
//	.   7
//	N   8
//	a   9
//	sp  10
//	}   11
//
// The "Na" token spans bytes [8, 10). In UTF-16 code units, "{ 𝔘x." before it
// is '{'(1) + ' '(1) + '𝔘'(2) + 'x'(1) + '.'(1) = 6 units, and "{ 𝔘x.Na" is
// 6 + 'N'(1) + 'a'(1) = 8 units. In UTF-8 (byte-counted) encoding the
// character values equal the byte offsets themselves: 8 and 10.
func TestNewCompletionItemEdit(t *testing.T) {
	text := "{ 𝔘x.Na }"

	off := 10 // cursor right after "Na"
	start, end := completionTokenSpan(text, off, false)
	if start != 8 || end != 10 {
		t.Fatalf("completionTokenSpan(%q, %d, false) = (%d, %d), want (8, 10)", text, off, start, end)
	}

	t.Run("utf16", func(t *testing.T) {
		item := newCompletionItem(text, start, end, encUTF16, "Name", "Name", ciKindField, tierMember, "string", nil)
		if item.TextEdit == nil {
			t.Fatal("TextEdit is nil")
		}
		wantRange := Range{
			Start: Position{Line: 0, Character: 6},
			End:   Position{Line: 0, Character: 8},
		}
		if item.TextEdit.Range != wantRange {
			t.Fatalf("Range = %+v, want %+v", item.TextEdit.Range, wantRange)
		}
		if got := positionForByteOffset(text, start, encUTF16); got != wantRange.Start {
			t.Fatalf("positionForByteOffset(start) = %+v, want %+v", got, wantRange.Start)
		}
		if got := positionForByteOffset(text, end, encUTF16); got != wantRange.End {
			t.Fatalf("positionForByteOffset(end) = %+v, want %+v", got, wantRange.End)
		}
	})

	t.Run("utf8", func(t *testing.T) {
		item := newCompletionItem(text, start, end, encUTF8, "Name", "Name", ciKindField, tierMember, "string", nil)
		if item.TextEdit == nil {
			t.Fatal("TextEdit is nil")
		}
		wantRange := Range{
			Start: Position{Line: 0, Character: 8},
			End:   Position{Line: 0, Character: 10},
		}
		if item.TextEdit.Range != wantRange {
			t.Fatalf("Range = %+v, want %+v", item.TextEdit.Range, wantRange)
		}
		if got := positionForByteOffset(text, start, encUTF8); got != wantRange.Start {
			t.Fatalf("positionForByteOffset(start) = %+v, want %+v", got, wantRange.Start)
		}
		if got := positionForByteOffset(text, end, encUTF8); got != wantRange.End {
			t.Fatalf("positionForByteOffset(end) = %+v, want %+v", got, wantRange.End)
		}
	})
}

func TestNewCompletionItemFields(t *testing.T) {
	text := "{ user.Na }"
	start, end := 7, 9 // "Na"

	t.Run("label equals newText: no FilterText, TextEdit inserts label", func(t *testing.T) {
		item := newCompletionItem(text, start, end, encUTF16, "Name", "Name", ciKindField, tierMember, "string", nil)
		if item.Label != "Name" {
			t.Fatalf("Label = %q, want %q", item.Label, "Name")
		}
		if item.Kind != ciKindField {
			t.Fatalf("Kind = %d, want %d", item.Kind, ciKindField)
		}
		if item.Detail != "string" {
			t.Fatalf("Detail = %q, want %q", item.Detail, "string")
		}
		if item.SortText != "10Name" {
			t.Fatalf("SortText = %q, want %q", item.SortText, "10Name")
		}
		if item.FilterText != "" {
			t.Fatalf("FilterText = %q, want empty (newText == label)", item.FilterText)
		}
		if item.TextEdit.NewText != "Name" {
			t.Fatalf("TextEdit.NewText = %q, want %q", item.TextEdit.NewText, "Name")
		}
	})

	t.Run("newText differs from label: FilterText set", func(t *testing.T) {
		item := newCompletionItem(text, start, end, encUTF16, "class", `class=""`, ciKindProperty, tierContext, "", nil)
		if item.FilterText != `class=""` {
			t.Fatalf("FilterText = %q, want %q", item.FilterText, `class=""`)
		}
		if item.TextEdit.NewText != `class=""` {
			t.Fatalf("TextEdit.NewText = %q, want %q", item.TextEdit.NewText, `class=""`)
		}
		if item.SortText != "05class" {
			t.Fatalf("SortText = %q, want %q", item.SortText, "05class")
		}
	})

	t.Run("sortText tiers pad to two digits", func(t *testing.T) {
		for tier, want := range map[int]string{
			tierLocal:     "05x",
			tierMember:    "10x",
			tierPackage:   "30x",
			tierImported:  "40x",
			tierUniverse:  "50x",
			tierKeyword:   "60x",
			tierSecondary: "20x",
		} {
			item := newCompletionItem(text, start, end, encUTF16, "x", "x", ciKindText, tier, "", nil)
			if item.SortText != want {
				t.Fatalf("tier %d: SortText = %q, want %q", tier, item.SortText, want)
			}
		}
	})

	t.Run("documentation is carried through", func(t *testing.T) {
		doc := &MarkupContent{Kind: "markdown", Value: "```go\nfunc F()\n```"}
		item := newCompletionItem(text, start, end, encUTF16, "F", "F", ciKindFunction, tierPackage, "func()", doc)
		if item.Documentation != doc {
			t.Fatalf("Documentation not carried through")
		}
	})
}
