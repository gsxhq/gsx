package codegen

import "testing"

func TestDefaultFieldMatcher(t *testing.T) {
	t.Parallel()
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

func TestMatchOrderedAttrsField(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		declared map[string]bool
		attr     string
		want     string
		ok       bool
	}{
		{"declared prop field", map[string]bool{"Extra": true}, "extra", "Extra", true},
		{"synthesized Attrs lowercase", map[string]bool{"Attrs": true}, "attrs", "Attrs", true},
		{"synthesized Attrs capitalized", map[string]bool{"Attrs": true}, "Attrs", "Attrs", true},
		{"no Attrs field declared", map[string]bool{"Title": true}, "attrs", "", false},
		{"nil declared assumes Attrs", nil, "attrs", "Attrs", true},
		{"nil declared non-attrs identifier", nil, "extra", "Extra", true}, // assume-prop regime
		{"nil declared kebab falls through", nil, "data-x", "", false},
		{"kebab never targets Attrs", map[string]bool{"Attrs": true}, "at-trs", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Real call sites always normalize fm via fieldMatcherOrDefault before
			// calling matchField/matchOrderedAttrsField (see childPropsLiteral);
			// matchField itself calls fm(...) directly without that guard, so a
			// literal nil here would panic whenever declared != nil. Pass the
			// default matcher to match production call shape.
			got, ok := matchOrderedAttrsField(tc.declared, tc.attr, defaultFieldMatcher)
			if got != tc.want || ok != tc.ok {
				t.Errorf("matchOrderedAttrsField(%v, %q) = (%q, %v); want (%q, %v)", tc.declared, tc.attr, got, ok, tc.want, tc.ok)
			}
		})
	}
}

func TestAttrToFieldCandidate(t *testing.T) {
	t.Parallel()
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
