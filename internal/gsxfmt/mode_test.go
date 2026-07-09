package gsxfmt

import (
	"strings"
	"testing"
)

func TestParseImportsMode(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want ImportsMode
	}{
		{"gofmt", ImportsGofmt},
		{"goimports", ImportsGoimports},
	} {
		got, err := ParseImportsMode(tc.in)
		if err != nil {
			t.Fatalf("ParseImportsMode(%q): %v", tc.in, err)
		}
		if got != tc.want {
			t.Fatalf("ParseImportsMode(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

// TestParseImportsModeRejectsUnknown: the error names both valid spellings.
func TestParseImportsModeRejectsUnknown(t *testing.T) {
	_, err := ParseImportsMode("gofumpt")
	if err == nil {
		t.Fatal("want error for unknown mode")
	}
	for _, want := range []string{"gofumpt", "gofmt", "goimports"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q does not mention %q", err, want)
		}
	}
}

// TestImportsModeZeroIsUnset: the zero value must be ImportsUnset so an absent
// config key is distinguishable from an explicit "gofmt".
func TestImportsModeZeroIsUnset(t *testing.T) {
	var m ImportsMode
	if m != ImportsUnset {
		t.Fatalf("zero ImportsMode = %v, want ImportsUnset", m)
	}
}

// TestImportsModeOr: Or falls back to def only when unset.
func TestImportsModeOr(t *testing.T) {
	if got := ImportsUnset.Or(ImportsGoimports); got != ImportsGoimports {
		t.Fatalf("Unset.Or(goimports) = %v", got)
	}
	if got := ImportsGofmt.Or(ImportsGoimports); got != ImportsGofmt {
		t.Fatalf("Gofmt.Or(goimports) = %v, want Gofmt", got)
	}
}

// TestImportsModePredicates: only goimports removes and reorders.
func TestImportsModePredicates(t *testing.T) {
	if !ImportsGoimports.RemoveUnused() || !ImportsGoimports.Reorder() {
		t.Fatal("goimports must remove and reorder")
	}
	if ImportsGofmt.RemoveUnused() || ImportsGofmt.Reorder() {
		t.Fatal("gofmt must neither remove nor reorder")
	}
	if ImportsUnset.RemoveUnused() || ImportsUnset.Reorder() {
		t.Fatal("unset must neither remove nor reorder (callers must resolve via Or first)")
	}
}

func TestImportsModeString(t *testing.T) {
	if ImportsGofmt.String() != "gofmt" || ImportsGoimports.String() != "goimports" {
		t.Fatal("String() spellings must round-trip ParseImportsMode")
	}
}
