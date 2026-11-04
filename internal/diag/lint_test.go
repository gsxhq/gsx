package diag

import (
	"reflect"
	"testing"
)

func TestParseLintLevel(t *testing.T) {
	for in, want := range map[string]LintLevel{"off": LintOff, "warning": LintWarning, "error": LintError} {
		got, err := ParseLintLevel(in)
		if err != nil || got != want {
			t.Errorf("ParseLintLevel(%q) = %v, %v; want %v", in, got, err, want)
		}
		if got.String() != in {
			t.Errorf("LintLevel(%d).String() = %q, want %q", got, got.String(), in)
		}
	}
	for _, bad := range []string{"", "Off", "warn", "info", "hint"} {
		if _, err := ParseLintLevel(bad); err == nil {
			t.Errorf("ParseLintLevel(%q) accepted", bad)
		}
	}
}

func TestLintPolicyApply(t *testing.T) {
	lint := func(code string) Diagnostic { return Diagnostic{Severity: Warning, Code: code, Message: code} }
	in := []Diagnostic{
		lint("child-element-spacing"),
		lint("duplicate-attribute"),
		lint("url-always-blocked"),
		{Severity: Error, Code: "parse-error"},
		{Severity: Warning, Code: "not-a-lint"},
	}
	policy := LintPolicy{
		"child-element-spacing": LintError,
		"duplicate-attribute":   LintOff,
		"url-always-blocked":    LintWarning,
	}
	got := policy.Apply(in)
	want := []Diagnostic{
		{Severity: Error, Code: "child-element-spacing", Message: "child-element-spacing"},
		lint("url-always-blocked"),
		{Severity: Error, Code: "parse-error"},
		{Severity: Warning, Code: "not-a-lint"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Apply = %+v\nwant %+v", got, want)
	}
	if in[0].Severity != Warning {
		t.Fatal("Apply mutated its input")
	}
	if got := LintPolicy(nil).Apply(in); !reflect.DeepEqual(got, in) {
		t.Fatalf("nil policy changed diagnostics: %+v", got)
	}
}

func TestLintPolicyFingerprint(t *testing.T) {
	for _, tc := range []struct {
		a, b LintPolicy
		same bool
	}{
		{nil, LintPolicy{}, true},
		{nil, LintPolicy{"duplicate-attribute": LintWarning}, true},
		{nil, LintPolicy{"duplicate-attribute": LintOff}, false},
		{LintPolicy{"duplicate-attribute": LintOff}, LintPolicy{"duplicate-attribute": LintError}, false},
		{
			LintPolicy{"duplicate-attribute": LintOff, "wrapper-cycle": LintError},
			LintPolicy{"wrapper-cycle": LintError, "duplicate-attribute": LintOff},
			true,
		},
	} {
		if got := tc.a.Fingerprint() == tc.b.Fingerprint(); got != tc.same {
			t.Errorf("Fingerprint(%v) == Fingerprint(%v) is %v, want %v", tc.a, tc.b, got, tc.same)
		}
	}
}

func TestLintCodesSorted(t *testing.T) {
	codes := LintCodes()
	for i := 1; i < len(codes); i++ {
		if codes[i-1] >= codes[i] {
			t.Fatalf("LintCodes not sorted and unique: %v", codes)
		}
	}
	for _, c := range codes {
		if !IsLint(c) {
			t.Fatalf("IsLint(%q) = false", c)
		}
	}
}
