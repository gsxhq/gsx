package codegen

import (
	"fmt"
	"path/filepath"
)

// GoWorkFile returns the exact workspace file frozen into this Module's Go
// command universe. An empty result means the authoritative GOWORK value is
// "off".
func (m *Module) GoWorkFile() (string, error) {
	if m == nil {
		return "", fmt.Errorf("codegen: nil Module")
	}
	if m.buildEnvErr != nil {
		return "", m.buildEnvErr
	}
	return normalizedGoWorkFile(environmentValue(m.buildEnv, "GOWORK")), nil
}

// ResolveGoWorkFile captures the Go command universe that a newly opened Module
// at moduleRoot would use and returns its exact resolved workspace file. This is
// intentionally a real Go environment query: filesystem walks cannot account
// for GOENV-persisted GOWORK settings.
func ResolveGoWorkFile(moduleRoot string) (string, error) {
	context := CaptureGoCommandContext(moduleRoot)
	if context.buildEnvErr != nil {
		return "", context.buildEnvErr
	}
	return normalizedGoWorkFile(environmentValue(context.buildEnv, "GOWORK")), nil
}

func normalizedGoWorkFile(path string) string {
	if path == "" || path == "off" {
		return ""
	}
	return filepath.Clean(path)
}
