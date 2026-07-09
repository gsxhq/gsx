package stdpath

import "testing"

// TestInternalVisible pins Go's real internal-visibility rule directly against
// InternalVisible, independent of anything in package codegen: a path with an
// "internal" component is importable only from the tree rooted at that
// component's parent; a path with none is always visible; a vendor/... path
// is never visible; and the boundary check must be a path-segment match, not
// a bare string-prefix match (an importer literally named "ab" must not be
// treated as being "under" a prefix "a").
func TestInternalVisible(t *testing.T) {
	cases := []struct {
		name       string
		importPath string
		importer   string
		want       bool
	}{
		{"no internal component, any importer", "encoding/json", "example.com/anything", true},
		{"no internal component, empty importer", "fmt", "", true},
		{"top-level internal, empty prefix, matching importer", "internal/foo", "", true},
		{"top-level internal, empty prefix, unrelated importer", "internal/foo", "example.com/u", false},
		{"a/internal/b visible from a itself", "a/internal/b", "a", true},
		{"a/internal/b visible from a/c (importer under prefix)", "a/internal/b", "a/c", true},
		{"a/internal/b NOT visible from b", "a/internal/b", "b", false},
		{"a/internal/b NOT visible from unrelated path", "a/internal/b", "example.com/other", false},
		{"boundary: ab is not under a", "a/internal/b", "ab", false},
		{"vendor path never visible, even from an importer under the same tree", "vendor/x", "vendor", false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := InternalVisible(c.importPath, c.importer); got != c.want {
				t.Errorf("InternalVisible(%q, %q) = %v, want %v", c.importPath, c.importer, got, c.want)
			}
		})
	}
}

// TestImportableAgreesWithInternalPrefixDetection: Importable is the
// generator's importer-free query ("could ANY external code ever import this
// std path"), i.e. Importable(p) is true iff p has no internal component (and
// isn't vendor/...). Checked here across the same shapes as TestInternalVisible
// so the two queries cannot silently diverge on what counts as "has an
// internal component".
func TestImportableAgreesWithInternalPrefixDetection(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{"encoding/json", true},
		{"encoding/json/internal", false},
		{"internal/foo", false},
		{"a/internal/b", false},
		{"vendor/x", false},
		{"net/http/internal", false},
	}
	for _, c := range cases {
		if got := Importable(c.path); got != c.want {
			t.Errorf("Importable(%q) = %v, want %v", c.path, got, c.want)
		}
	}
}
