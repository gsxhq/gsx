package gen

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/mod/modfile"
	"golang.org/x/mod/module"
)

// templateManifest is the optional gsx-template.json at a fetched template's
// root: strip is a list of slash-path globs (see shouldStrip) removed before
// copying; env asks personalize to write generated or literal values into
// the scaffolded .env.
type templateManifest struct {
	Strip []string          `json:"strip"`
	Env   map[string]string `json:"env"`
}

// invalidModuleError reports that the caller-supplied module path failed
// module.CheckPath — a usage error the caller should surface as exit 2,
// unlike every other personalize failure (I/O, malformed manifest), which is
// an operational failure (exit 1).
type invalidModuleError struct {
	path string
	err  error
}

func (e *invalidModuleError) Error() string {
	return fmt.Sprintf("invalid module path %q: %v", e.path, e.err)
}

func (e *invalidModuleError) Unwrap() error { return e.err }

// personalize copies src (a fetched module zip or a --from local checkout)
// into destDir, rewriting it in place for newModule:
//
//  1. go.mod's module path is rewritten via the modfile write API.
//  2. An optional gsx-template.json at src's root lists strip globs (removed
//     before copying — the manifest file itself is always stripped) and env
//     entries (written to destDir/.env after the copy).
//  3. *.go, *.gsx, and gsx.toml files have the quoted old-module-path prefix
//     in their import/filter strings rewritten to newModule; package.json's
//     "name" is set to the new module's basename.
//
// Unlike scaffold (the embedded-template engine), personalize does NOT run
// «»/text-template rendering or the dot-/transformName renaming: a fetched
// template is a literal repo, not a Go text/template source tree, so its
// files are copied byte-for-byte except for the targeted rewrites above.
func personalize(src fs.FS, destDir, newModule string) error {
	gomodData, err := fs.ReadFile(src, "go.mod")
	if err != nil {
		return fmt.Errorf("reading go.mod: %w", err)
	}
	mf, err := modfile.Parse("go.mod", gomodData, nil)
	if err != nil {
		return fmt.Errorf("parsing go.mod: %w", err)
	}
	if mf.Module == nil {
		return errors.New("go.mod has no module directive")
	}
	oldModule := mf.Module.Mod.Path

	if err := module.CheckPath(newModule); err != nil {
		return &invalidModuleError{path: newModule, err: err}
	}

	if err := mf.AddModuleStmt(newModule); err != nil {
		return fmt.Errorf("rewriting go.mod module path: %w", err)
	}
	mf.Cleanup()
	newGomod, err := mf.Format()
	if err != nil {
		return fmt.Errorf("formatting rewritten go.mod: %w", err)
	}

	manifest, err := readTemplateManifest(src)
	if err != nil {
		return err
	}

	oldQuoted := []byte(`"` + oldModule)
	newQuoted := []byte(`"` + newModule)

	walkErr := fs.WalkDir(src, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if shouldStrip(p, manifest) {
			return nil
		}

		var out []byte
		switch {
		case p == "go.mod":
			out = newGomod
		case p == "package.json":
			raw, err := fs.ReadFile(src, p)
			if err != nil {
				return err
			}
			out, err = rewritePackageJSON(raw, newModule)
			if err != nil {
				return fmt.Errorf("package.json: %w", err)
			}
		case strings.HasSuffix(p, ".go"), strings.HasSuffix(p, ".gsx"), p == "gsx.toml":
			raw, err := fs.ReadFile(src, p)
			if err != nil {
				return err
			}
			out = bytes.ReplaceAll(raw, oldQuoted, newQuoted)
		default:
			raw, err := fs.ReadFile(src, p)
			if err != nil {
				return err
			}
			out = raw
		}

		dest := filepath.Join(destDir, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return err
		}
		return os.WriteFile(dest, out, 0o644)
	})
	if walkErr != nil {
		return walkErr
	}

	return applyEnvSecrets(destDir, manifest.Env)
}

// readTemplateManifest reads the optional gsx-template.json at src's root. A
// missing manifest is not an error — most --from local checkouts won't have
// one — and yields a zero-value templateManifest (no strips, no env).
func readTemplateManifest(src fs.FS) (templateManifest, error) {
	data, err := fs.ReadFile(src, "gsx-template.json")
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return templateManifest{}, nil
		}
		return templateManifest{}, fmt.Errorf("reading gsx-template.json: %w", err)
	}
	var m templateManifest
	if err := json.Unmarshal(data, &m); err != nil {
		return templateManifest{}, fmt.Errorf("parsing gsx-template.json: %w", err)
	}
	return m, nil
}

// shouldStrip reports whether the slash-separated path rel (as produced by
// fs.WalkDir) should be omitted from the copy: the manifest file itself is
// always stripped; a strip pattern ending in "/" strips that whole subtree; a
// bare pattern is matched with path.Match against rel.
func shouldStrip(rel string, manifest templateManifest) bool {
	if rel == "gsx-template.json" {
		return true
	}
	for _, pattern := range manifest.Strip {
		if dir, ok := strings.CutSuffix(pattern, "/"); ok {
			if rel == dir || strings.HasPrefix(rel, dir+"/") {
				return true
			}
			continue
		}
		if ok, _ := path.Match(pattern, rel); ok {
			return true
		}
	}
	return false
}

// rewritePackageJSON sets "name" to the new module's basename and
// re-marshals with a 2-space indent (encoding/json sorts object keys
// alphabetically on marshal, so field order is not preserved — an accepted
// tradeoff for a mechanical rewrite of a fetched template's package.json).
func rewritePackageJSON(raw []byte, newModule string) ([]byte, error) {
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	m["name"] = path.Base(newModule)
	out, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(out, '\n'), nil
}

// applyEnvSecrets resolves each manifest env entry ("secret-hex-32" ⇒ a fresh
// crypto/rand-generated 32-byte hex string; "literal:<v>" ⇒ <v> verbatim) and
// appends any keys not already present in destDir/.env — creating the file
// if it doesn't exist, and never overwriting a key the template (or a prior
// run) already set.
func applyEnvSecrets(destDir string, env map[string]string) error {
	if len(env) == 0 {
		return nil
	}
	envPath := filepath.Join(destDir, ".env")
	existing := map[string]bool{}
	existingContent, err := os.ReadFile(envPath)
	if err != nil {
		if !os.IsNotExist(err) {
			return err
		}
	} else {
		for _, line := range strings.Split(string(existingContent), "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			if k, _, ok := strings.Cut(line, "="); ok {
				existing[k] = true
			}
		}
	}

	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var toAppend []string
	for _, k := range keys {
		if existing[k] {
			continue
		}
		v, err := resolveEnvValue(env[k])
		if err != nil {
			return fmt.Errorf("gsx-template.json: env %s: %w", k, err)
		}
		toAppend = append(toAppend, k+"="+v)
	}
	if len(toAppend) == 0 {
		return nil
	}

	f, err := os.OpenFile(envPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	if len(existingContent) > 0 && !bytes.HasSuffix(existingContent, []byte("\n")) {
		if _, err := f.WriteString("\n"); err != nil {
			return err
		}
	}
	for _, line := range toAppend {
		if _, err := f.WriteString(line + "\n"); err != nil {
			return err
		}
	}
	return nil
}

// resolveEnvValue interprets one gsx-template.json env value spec:
// "secret-hex-32" generates a fresh random 32-byte hex secret; "literal:<v>"
// yields <v> verbatim.
func resolveEnvValue(spec string) (string, error) {
	if v, ok := strings.CutPrefix(spec, "literal:"); ok {
		return v, nil
	}
	if spec == "secret-hex-32" {
		b := make([]byte, 32)
		if _, err := rand.Read(b); err != nil {
			return "", err
		}
		return hex.EncodeToString(b), nil
	}
	return "", fmt.Errorf("unknown env value spec %q (want \"secret-hex-32\" or \"literal:<value>\")", spec)
}
