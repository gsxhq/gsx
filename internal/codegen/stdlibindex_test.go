package codegen

import (
	"os/exec"
	"strings"
	"testing"
)

// TestStdlibIndexIsFresh diffs the generated table against a live `go list std`.
// A Go upgrade that adds, removes, or renames a std package fails here rather
// than silently under-resolving. Regenerate with:
//
//	go generate ./internal/codegen
func TestStdlibIndexIsFresh(t *testing.T) {
	if testing.Short() {
		t.Skip("runs `go list std`")
	}
	out, err := exec.Command("go", "list", "-f", "{{.Name}} {{.ImportPath}}", "std").Output()
	if err != nil {
		t.Skipf("go list std: %v", err)
	}
	live := map[string][]string{}
	for line := range strings.SplitSeq(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) != 2 || strings.Contains(f[1], "internal/") || strings.HasPrefix(f[1], "vendor/") {
			continue
		}
		live[f[0]] = append(live[f[0]], f[1])
	}
	if len(live) != len(stdlibIndex) {
		t.Fatalf("stdlibIndex has %d names, `go list std` has %d — run `go generate ./internal/codegen`", len(stdlibIndex), len(live))
	}
	for name, paths := range live {
		got, ok := stdlibIndex[name]
		if !ok {
			t.Errorf("stdlibIndex missing %q — run `go generate ./internal/codegen`", name)
			continue
		}
		if strings.Join(got, ",") != strings.Join(paths, ",") {
			t.Errorf("stdlibIndex[%q] = %v, want %v — run `go generate ./internal/codegen`", name, got, paths)
		}
	}
}
