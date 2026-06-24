package codegen

import "testing"

func TestDefaultFieldMatcher(t *testing.T) {
	tests := []struct {
		attr      string
		fields    []string
		wantField string
		wantOK    bool
	}{
		// Plain identifier → capitalize first letter, field present.
		{"variant", []string{"Variant"}, "Variant", true},
		// Plain identifier → capitalize, but field absent.
		{"variant", []string{"Size"}, "", false},
		// Already-capitalized identifier → unchanged, field present.
		{"fullWidth", []string{"FullWidth"}, "FullWidth", true},
		// Kebab → CamelCase, field present.
		{"full-width", []string{"FullWidth"}, "FullWidth", true},
		{"aria-label", []string{"AriaLabel"}, "AriaLabel", true},
		// Kebab → CamelCase, but field absent (falls through to Attrs).
		{"data-id", []string{"Variant"}, "", false},
		// Multiple fields, match in the middle.
		{"aria-label", []string{"FullWidth", "AriaLabel", "Variant"}, "AriaLabel", true},
	}
	for _, tt := range tests {
		field, ok := defaultFieldMatcher(tt.attr, tt.fields)
		if ok != tt.wantOK || field != tt.wantField {
			t.Errorf("defaultFieldMatcher(%q, %v) = (%q, %v); want (%q, %v)",
				tt.attr, tt.fields, field, ok, tt.wantField, tt.wantOK)
		}
	}
}

func TestAttrToFieldCandidate(t *testing.T) {
	tests := []struct {
		attr string
		want string
	}{
		{"variant", "Variant"},
		{"fullWidth", "FullWidth"},
		{"full-width", "FullWidth"},
		{"aria-label", "AriaLabel"},
		{"data-id", "DataId"},
		// Malformed kebab: leading/trailing dash → "".
		{"-foo", ""},
		{"foo-", ""},
	}
	for _, tt := range tests {
		got := attrToFieldCandidate(tt.attr)
		if got != tt.want {
			t.Errorf("attrToFieldCandidate(%q) = %q; want %q", tt.attr, got, tt.want)
		}
	}
}
