package gen

import (
	"os"
	"testing"
)

// TestMain isolates every gen test from the real user cache. Tests that
// specifically exercise caching override GSXCACHE with t.Setenv to a temp dir.
func TestMain(m *testing.M) {
	if os.Getenv("GSXCACHE") == "" {
		os.Setenv("GSXCACHE", "off") // default: cache disabled in tests
	}
	os.Exit(m.Run())
}
