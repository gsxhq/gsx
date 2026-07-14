package codegen

import (
	"strings"
	"testing"
)

// parseParams must record, per parameter, the verbatim TYPE text and its byte
// span within the (trimmed) param source — the inputs the LSP uses to bridge a
// cursor on a parameter type into the type-checked skeleton.
func TestParseParamsTypeSpans(t *testing.T) {
	cases := []struct {
		src    string
		want   []param // name, typ(printer), typeSrc; offsets checked structurally
		nTypes []string
	}{
		{src: "comments []store.Comment", nTypes: []string{"[]store.Comment"}},
		{src: "a, b string", nTypes: []string{"string", "string"}},
		{src: "p *User, n int", nTypes: []string{"*User", "int"}},
		{src: "m map[string]store.Post", nTypes: []string{"map[string]store.Post"}},
	}
	for _, tc := range cases {
		got, err := parseParams(tc.src)
		if err != nil {
			t.Fatalf("parseParams(%q): %v", tc.src, err)
		}
		if len(got) != len(tc.nTypes) {
			t.Fatalf("parseParams(%q) = %d params, want %d", tc.src, len(got), len(tc.nTypes))
		}
		trimmed := strings.TrimSpace(tc.src)
		for i, p := range got {
			// typeSrc is the verbatim type text and must equal the source slice the
			// offset/length point at — this is the identity the skeleton relies on.
			if p.typeSrc != tc.nTypes[i] {
				t.Errorf("param %d of %q: typeSrc=%q, want %q", i, tc.src, p.typeSrc, tc.nTypes[i])
			}
			if p.typeLen != len(p.typeSrc) {
				t.Errorf("param %d of %q: typeLen=%d, want %d", i, tc.src, p.typeLen, len(p.typeSrc))
			}
			if p.typeOff < 0 || p.typeOff+p.typeLen > len(trimmed) {
				t.Fatalf("param %d of %q: span [%d,%d) out of range for %q", i, tc.src, p.typeOff, p.typeOff+p.typeLen, trimmed)
			}
			if slice := trimmed[p.typeOff : p.typeOff+p.typeLen]; slice != p.typeSrc {
				t.Errorf("param %d of %q: source[%d:%d]=%q, want %q (typeSrc)", i, tc.src, p.typeOff, p.typeOff+p.typeLen, slice, p.typeSrc)
			}
		}
	}
}

// The final declaration parser retains unnamed parameters, but the shipping
// Props path remains named-only until the atomic cutover. Sharing the Go parse
// must not silently change that old model.
func TestParseParamsKeepsNamedOnlyShape(t *testing.T) {
	got, err := parseParams("a, b string, _ bool, rest ...byte")
	if err != nil {
		t.Fatal(err)
	}
	wantNames := []string{"a", "b", "_", "rest"}
	if len(got) != len(wantNames) {
		t.Fatalf("parseParams returned %d params, want %d", len(got), len(wantNames))
	}
	for i, p := range got {
		if p.name != wantNames[i] {
			t.Errorf("param %d name=%q, want %q", i, p.name, wantNames[i])
		}
	}

	unnamed, err := parseParams("string, bool, ...byte")
	if err != nil {
		t.Fatal(err)
	}
	if len(unnamed) != 0 {
		t.Fatalf("legacy parseParams returned %d unnamed params, want 0", len(unnamed))
	}
}
