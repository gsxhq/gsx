package codegen

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
)

type effectiveGoEnvironment struct {
	GOFLAGS string
}

// freezeGoCommandEnvironment resolves the effective GOFLAGS from the Module's
// Open-time environment immediately before its cold load. In normal mode gsx
// owns one source-inventory overlay and cannot combine it soundly with another
// overlay or an external packages driver.
func freezeGoCommandEnvironment(buildEnv []string, moduleRoot, packagesDriverPath string) ([]string, error) {
	driver := environmentValue(buildEnv, "GOPACKAGESDRIVER")
	switch {
	case driver == "off":
	case driver != "":
		return nil, fmt.Errorf("codegen: GOPACKAGESDRIVER %q is not supported in normal mode", driver)
	default:
		if packagesDriverPath != "" {
			return nil, fmt.Errorf("codegen: PATH-discovered gopackagesdriver %q is not supported in normal mode", packagesDriverPath)
		}
	}

	command := exec.Command("go", "env", "-json", "GOFLAGS")
	command.Env = buildEnv
	command.Dir = moduleRoot
	output, err := command.Output()
	if err != nil {
		return nil, fmt.Errorf("codegen: resolve effective Go environment: %w", err)
	}
	var effective effectiveGoEnvironment
	if err := json.Unmarshal(output, &effective); err != nil {
		return nil, fmt.Errorf("codegen: decode effective Go environment: %w", err)
	}
	flags, err := splitGoQuoted(effective.GOFLAGS)
	if err != nil {
		return nil, fmt.Errorf("codegen: parse effective GOFLAGS: %w", err)
	}
	var overlayValue string
	var overlayHasValue, overlaySeen bool
	for _, flag := range flags {
		name, value, hasValue := goFlagValue(flag)
		if name == "overlay" {
			overlaySeen = true
			overlayValue = value
			overlayHasValue = hasValue
		}
	}
	if overlaySeen && (!overlayHasValue || overlayValue != "") {
		return nil, fmt.Errorf("codegen: effective GOFLAGS -overlay is not supported in normal mode")
	}

	buildEnv = environmentWithValue(buildEnv, "GOFLAGS", effective.GOFLAGS)
	// x/tools/go/packages consults the live process PATH when no driver is
	// explicit in Config.Env. Pin the already-validated no-driver state so a
	// later process-environment mutation cannot escape this frozen boundary.
	return environmentWithValue(buildEnv, "GOPACKAGESDRIVER", "off"), nil
}

func goFlagValue(flag string) (name, value string, hasValue bool) {
	trimmed := strings.TrimPrefix(flag, "-")
	trimmed = strings.TrimPrefix(trimmed, "-")
	if name, value, found := strings.Cut(trimmed, "="); found {
		return name, value, true
	}
	return trimmed, "", false
}

func environmentValue(env []string, key string) string {
	prefix := key + "="
	for index := len(env) - 1; index >= 0; index-- {
		if strings.HasPrefix(env[index], prefix) {
			return env[index][len(prefix):]
		}
	}
	return ""
}

func environmentWithValue(env []string, key, value string) []string {
	prefix := key + "="
	out := make([]string, 0, len(env)+1)
	for _, entry := range env {
		if !strings.HasPrefix(entry, prefix) {
			out = append(out, entry)
		}
	}
	return append(out, prefix+value)
}

func isGoFlagSpace(char byte) bool {
	return char == ' ' || char == '\t' || char == '\n' || char == '\r'
}

// splitGoQuoted mirrors cmd/internal/quoted.Split, the parser used by the Go
// command for GOFLAGS. Keeping the same grammar avoids inventing a second flag
// interpretation at the overlay boundary.
func splitGoQuoted(source string) ([]string, error) {
	var fields []string
	for len(source) > 0 {
		for len(source) > 0 && isGoFlagSpace(source[0]) {
			source = source[1:]
		}
		if len(source) == 0 {
			break
		}
		if source[0] == '"' || source[0] == '\'' {
			quote := source[0]
			source = source[1:]
			index := 0
			for index < len(source) && source[index] != quote {
				index++
			}
			if index >= len(source) {
				return nil, fmt.Errorf("unterminated %c string", quote)
			}
			fields = append(fields, source[:index])
			source = source[index+1:]
			continue
		}
		index := 0
		for index < len(source) && !isGoFlagSpace(source[index]) {
			index++
		}
		fields = append(fields, source[:index])
		source = source[index:]
	}
	return fields, nil
}
