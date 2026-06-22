package gen

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// moduleRoot walks up from dir to the nearest go.mod, returning its directory
// and the declared module path.
func moduleRoot(dir string) (string, string, error) {
	d, err := filepath.Abs(dir)
	if err != nil {
		return "", "", err
	}
	for {
		gomod := filepath.Join(d, "go.mod")
		if data, err := os.ReadFile(gomod); err == nil {
			return d, modulePathFromGoMod(data), nil
		}
		parent := filepath.Dir(d)
		if parent == d {
			return "", "", fmt.Errorf("gen: no go.mod found above %s", dir)
		}
		d = parent
	}
}

func modulePathFromGoMod(data []byte) string {
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "module ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "module "))
		}
	}
	return ""
}
