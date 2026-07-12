package gsx

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestAttrsFoldWhitespaceAndBoolValues(t *testing.T) {
	contribs := decodeContribs([]byte{0x03, 0, 4, 0, 2, 5, 5})
	if got, ok := contribs[0][0].Value.(string); !ok || got != " a " {
		t.Fatalf("class whitespace value = %#v (%T), want %q (string)", contribs[0][0].Value, contribs[0][0].Value, " a ")
	}
	if got, ok := contribs[0][2].Value.(bool); !ok || !got {
		t.Fatalf("disabled value = %#v (%T), want true (bool)", contribs[0][2].Value, contribs[0][2].Value)
	}

	for name, attrs := range map[string]Attrs{
		"fold":      ConcatAttrs(contribs...),
		"reference": referenceLastWins(contribs),
	} {
		var buf bytes.Buffer
		W(&buf).Spread(context.Background(), attrs, nil, nil, nil, nil, nil)
		if got, want := buf.String(), ` class="a b" disabled`; got != want {
			t.Errorf("%s render = %q, want %q", name, got, want)
		}
	}
}

// FuzzAttrsFoldMatchesReference proves the governing principle of the
// multi-spread merge feature at its runtime leaf: folding N attribute
// contributors with ConcatAttrs and rendering the result through
// Writer.Spread must be byte-identical to an INDEPENDENTLY computed
// last-wins-with-class/style-aggregation reference, for any sequence of
// contributors (including untaken conditional bags that contribute nothing).
//
// The two sides below both call Spread — the shared rendering leaf — but on
// two differently BUILT bags: the left is the real merge path
// (ConcatAttrs, which merely concatenates and lets Spread resolve
// duplicates at render time); the right is a bag already reduced, by a
// hand-rolled reference algorithm that never calls ConcatAttrs, to one
// entry per key (last-wins scalars; pre-joined class/style). If Spread's
// last-wins/aggregation algorithm ever diverges from the documented rule —
// or ConcatAttrs ever drops/reorders a contributed pair — the two renders
// disagree and the fuzzer finds it.
func FuzzAttrsFoldMatchesReference(f *testing.F) {
	// two disjoint bags: {id:a} + {data-x:b}
	f.Add([]byte{0x01, 3, 1, 0x01, 4, 2})
	// two colliding bags: {href:a} then {href:b} — last (b) must win
	f.Add([]byte{0x01, 2, 1, 0x01, 2, 2})
	// class in both contributors — must aggregate, not last-win
	f.Add([]byte{0x01, 0, 1, 0x01, 0, 2})
	// style in both contributors — must aggregate with "; ", not last-win
	f.Add([]byte{0x01, 1, 1, 0x01, 1, 2})
	// an untaken conditional (bit7 set, bit6 clear) contributes nothing, even
	// though it encodes a pair that must be discarded
	f.Add([]byte{0x81, 2, 1})
	// all-empty: no contributors at all
	f.Add([]byte{})
	// a mix: disjoint + colliding + class + untaken conditional in one run
	f.Add([]byte{0x01, 3, 1, 0x01, 2, 2, 0x01, 0, 1, 0x81, 2, 3, 0x01, 0, 3})
	// edge-trimmed class token and a true boolean scalar
	f.Add([]byte{0x03, 0, 4, 0, 2, 5, 5})
	// false boolean scalar
	f.Add([]byte{0x01, 5, 6})

	f.Fuzz(func(t *testing.T, data []byte) {
		contribs := decodeContribs(data)

		var g, w bytes.Buffer
		W(&g).Spread(context.Background(), ConcatAttrs(contribs...), []string{"href"}, nil, nil, nil, nil)
		W(&w).Spread(context.Background(), referenceLastWins(contribs), []string{"href"}, nil, nil, nil, nil)
		if g.String() != w.String() {
			t.Fatalf("fold != reference\ncontribs=%v\n got=%q\nwant=%q", contribs, g.String(), w.String())
		}
	})
}

// foldKeyAlphabet is the fixed key alphabet decodeContribs draws from: the
// two aggregating keys (class, style), one URL-sink key (href, routed
// through the nav sink in the fuzz harness), and three plain scalars.
var foldKeyAlphabet = []string{"class", "style", "href", "id", "data-x", "disabled"}

// foldValAlphabet is the fixed value alphabet: empty, plain and whitespace
// bearing strings, plus both boolean values.
var foldValAlphabet = []any{"", "a", "b", "x y", " a ", true, false}

// decodeContribs deterministically decodes data into a sequence of
// contributors (each either a plain bag or, for an untaken conditional, an
// empty/nil bag) over the fixed key/value alphabets above. Layout, read one
// byte at a time:
//
//   - ctrl byte: bit7 = isConditional, bit6 = taken (meaningless unless
//     isConditional), bits2:0 = number of key/value pairs (0-7) that follow
//   - then nPairs * 2 bytes: (keyIndex, valIndex), each reduced mod alphabet
//     length
//
// The pair bytes are always consumed (keeping later contributors' byte
// alignment stable under fuzzer mutation of the cond/taken bits), but an
// untaken conditional (isConditional && !taken) discards them and
// contributes nil — modeling AttrsCond's untaken branch, which never
// contributes attributes to the fold.
func decodeContribs(data []byte) []Attrs {
	const maxContribs = 8
	var contribs []Attrs
	i := 0
	for len(contribs) < maxContribs && i < len(data) {
		ctrl := data[i]
		i++
		isCond := ctrl&0x80 != 0
		taken := ctrl&0x40 != 0
		nPairs := int(ctrl & 0x07)

		var bag Attrs
		for p := 0; p < nPairs && i+1 < len(data); p++ {
			kb, vb := data[i], data[i+1]
			i += 2
			bag = append(bag, Attr{
				Key:   foldKeyAlphabet[int(kb)%len(foldKeyAlphabet)],
				Value: foldValAlphabet[int(vb)%len(foldValAlphabet)],
			})
		}
		if isCond && !taken {
			bag = nil // untaken conditional: contributes nothing
		}
		contribs = append(contribs, bag)
	}
	return contribs
}

// referenceLastWins is an INDEPENDENT reference implementation of gsx's
// documented merge rule (see Attrs.Get, Attrs.Class, Attrs.Style, and
// Writer.Spread's doc comment): flatten every contributor in source order,
// then keep exactly one entry per key at the position of its LAST
// occurrence — a plain scalar keeps its last value; "class"/"style" instead
// aggregate EVERY occurrence across the whole flattened sequence (join " "
// for class, after trimming each piece; join "; " for style, untrimmed).
//
// This never calls ConcatAttrs, Attrs.Merge, Attrs.Class, or Attrs.Style —
// it reimplements the rule from scratch — so it can actually disagree with
// the production merge/render path if either one has a bug.
func referenceLastWins(contribs []Attrs) Attrs {
	var flat Attrs
	for _, c := range contribs {
		flat = append(flat, c...)
	}
	if len(flat) == 0 {
		return nil
	}

	lastIdx := make(map[string]int, len(flat))
	for i, kv := range flat {
		lastIdx[kv.Key] = i
	}

	var out Attrs
	for i, kv := range flat {
		if lastIdx[kv.Key] != i {
			continue // not this key's last occurrence — superseded
		}
		val := kv.Value
		switch kv.Key {
		case "class":
			val = refJoinAll(flat, "class", " ", true)
		case "style":
			val = refJoinAll(flat, "style", "; ", false)
		}
		out = append(out, Attr{Key: kv.Key, Value: val})
	}
	return out
}

// refJoinAll joins every value for key across flat, in order, with sep. When
// trim is true each piece is space-trimmed before joining (the documented
// class rule); style aggregation does not trim. Empty pieces (after
// trimming, for class) are skipped rather than producing a doubled
// separator — matching the documented "no silent loss, no empty joins" rule.
func refJoinAll(flat Attrs, key, sep string, trim bool) string {
	var out string
	for _, kv := range flat {
		if kv.Key != key {
			continue
		}
		piece, ok := kv.Value.(string)
		if !ok {
			continue
		}
		if trim {
			piece = strings.TrimSpace(piece)
		}
		switch {
		case out == "":
			out = piece
		case piece == "":
			// keep out unchanged
		default:
			out = out + sep + piece
		}
	}
	return out
}
