package gen

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"go/format"
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
// validateNewModule — a usage error the caller should surface as exit 2,
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

// validateNewModule checks mod with module.CheckImportPath — the same
// acceptance rule `go mod init` effectively applies, deliberately looser than
// module.CheckPath (which additionally demands a dotted, publishable-looking
// leading path element). A bare "myapp", the default `gsx new myapp` derives
// from the target directory's name with zero flags, must work for a fetched
// or --from template exactly as it already does for the embedded templates —
// only genuinely malformed input (stray whitespace, disallowed punctuation,
// reserved Windows device names, empty elements) is rejected. Returns an
// *invalidModuleError (Unwrap-able) so callers can classify the failure as a
// usage error (exit 2) rather than an operational one. new calls this before
// resolveTemplateSource, so an invalid --module fails before any network
// fetch; personalize calls it again as its own authoritative check, so the
// function is correct when called directly (as the tests do).
func validateNewModule(mod string) error {
	if err := module.CheckImportPath(mod); err != nil {
		return &invalidModuleError{path: mod, err: err}
	}
	return nil
}

// personalize copies src (a fetched module zip or a --from local checkout)
// into destDir, rewriting it in place for newModule:
//
//  1. go.mod's module path is rewritten via the modfile write API.
//  2. An optional gsx-template.json at src's root lists strip globs (removed
//     before copying — the manifest file itself is always stripped) and env
//     entries (written to destDir/.env after the copy).
//  3. *.go, *.gsx, and gsx.toml files (at any depth, not just the root) have
//     every quoted reference to the old module path — the bare `"<old>"`
//     import and any subpackage `"<old>/sub/pkg"` — rewritten to newModule
//     (see replaceModuleRefs for the exact boundary rule; a sibling module
//     such as `"<old>-extras"` is never touched). package.json's "name" is
//     set to the new module's basename, in place, preserving the rest of the
//     document byte-for-byte.
//  4. *.go files are additionally run through go/format.Source after the
//     rewrite: the new module path can sort differently than the old one
//     within its (unchanged) gofmt import group — e.g. an own-module import
//     that used to sort as "github.com/gsxhq/template/..." now sorts under
//     "example.com/...", which is a valid position in a *different* place in
//     the block than the byte replace left it. format.Source's ast.SortImports
//     re-sorts within each existing blank-line-separated group, which is
//     sufficient here since the rewrite only ever changes a path's sort key,
//     never which group it belongs to (no import gains or loses a blank-line
//     boundary from a module rename). *.gsx files are deliberately excluded:
//     gsx isn't Go syntax and format.Source would just error on them.
//
// Unlike scaffold (the embedded-template engine), personalize does NOT run
// «»/text-template rendering or the dot-/transformName renaming: a fetched
// template is a literal repo, not a Go text/template source tree, so its
// files are copied byte-for-byte except for the targeted rewrites above.
//
// src is read the way golang.org/x/mod/zip publishes a module directory
// (omitFromTemplate): VCS directories, nested modules, vendored packages and
// non-regular files (symlinks) are omitted. A proxy zip has already had that
// policy applied, so for a fetched template it is a no-op; for a --from
// local checkout it is what keeps the template repo's .git/ (and a
// developer's symlinked node_modules) out of the new project. Anything else
// a checkout carries that shouldn't ship — node_modules/, a local .env — is
// the manifest's strip list's job.
//
// Each file's permission bits are preserved from the source (floored at
// 0o644), so an executable script in a --from checkout stays executable;
// proxy zips carry no permission bits, so a fetched template's files always
// come back as the floor.
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

	if err := validateNewModule(newModule); err != nil {
		return err
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

	// rewrite rewrites every quoted reference to the old module path (the
	// bare `"old"` import and any subpackage `"old/sub"`) to the new one; see
	// replaceModuleRefs for the boundary rule.
	rewrite := func(raw []byte) []byte { return replaceModuleRefs(raw, oldModule, newModule) }

	walkErr := fs.WalkDir(src, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if omitted, skip := omitFromTemplate(src, p, d); omitted {
			return skip
		}
		if d.IsDir() {
			return nil
		}
		if shouldStrip(p, manifest) {
			return nil
		}

		info, err := d.Info()
		if err != nil {
			return err
		}
		// Preserve the source's permission bits (so an executable script in
		// the template stays executable), floored at 0o644 so a source file
		// with unusually tight permissions still comes out readable. Module
		// zips from the proxy carry no permission bits at all (see the
		// golang.org/x/mod/zip note on personalize's doc comment), so this
		// only has anything beyond the floor to preserve for --from.
		mode := info.Mode().Perm() | 0o644

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
		case strings.HasSuffix(p, ".go"):
			raw, err := fs.ReadFile(src, p)
			if err != nil {
				return err
			}
			rewritten := rewrite(raw)
			// Re-sort imports disturbed by the module rename (see point 4 of
			// the doc comment above). A parse failure here means the
			// template's Go source was already malformed, or uses syntax the
			// pinned toolchain can't parse — not something personalize
			// should hard-fail the whole scaffold over. Ship the rewritten
			// (unsorted-import) bytes in that case: the user gets a scaffold
			// they can see is broken and fix, rather than no scaffold at
			// all.
			if formatted, ferr := format.Source(rewritten); ferr == nil {
				out = formatted
			} else {
				out = rewritten
			}
		case strings.HasSuffix(p, ".gsx"), path.Base(p) == "gsx.toml":
			raw, err := fs.ReadFile(src, p)
			if err != nil {
				return err
			}
			out = rewrite(raw)
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
		return os.WriteFile(dest, out, mode)
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
// fs.WalkDir) should be omitted from the copy. The manifest file itself is
// always stripped. Each strip pattern is a path.Match glob tested against
// rel and against every ancestor directory of rel, so a pattern that names
// a directory — `docs`, `docs/`, or `docs/*` — strips its whole subtree, and
// `*.md` strips only root-level markdown files (path.Match's `*` never
// crosses a `/`). A trailing `/` is accepted as directory sugar and ignored
// for matching.
func shouldStrip(rel string, manifest templateManifest) bool {
	if rel == "gsx-template.json" {
		return true
	}
	for _, pattern := range manifest.Strip {
		pattern = strings.TrimSuffix(pattern, "/")
		if pattern == "" {
			continue
		}
		for p := rel; p != "." && p != ""; p = path.Dir(p) {
			if ok, _ := path.Match(pattern, p); ok {
				return true
			}
		}
	}
	return false
}

// rewritePackageJSON sets the top-level "name" to the new module's basename
// by splicing the new value over the old one in place — key order, indent,
// and every other byte of the document are preserved (a fetched template's
// package.json should still diff cleanly against upstream). It walks the
// document with a streaming json.Decoder to locate the top-level "name"
// value's byte range; a document with no top-level "name" gets one inserted
// right after the opening brace.
func rewritePackageJSON(raw []byte, newModule string) ([]byte, error) {
	name, err := json.Marshal(path.Base(newModule))
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if tok != json.Delim('{') {
		return nil, fmt.Errorf("top-level value is %v, want an object", tok)
	}
	afterBrace := dec.InputOffset()
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		key, ok := keyTok.(string)
		if !ok {
			return nil, fmt.Errorf("object key is %v, want a string", keyTok)
		}
		if key != "name" {
			// Consume (and discard) the whole value, whatever its shape.
			var skip json.RawMessage
			if err := dec.Decode(&skip); err != nil {
				return nil, err
			}
			continue
		}
		// InputOffset after the key token sits just past the key string;
		// the value starts after the ':' and any whitespace. Decode the
		// value into a RawMessage to find its end, then locate its start
		// by scanning forward from the key over the separator.
		valueStartHint := dec.InputOffset()
		var value json.RawMessage
		if err := dec.Decode(&value); err != nil {
			return nil, err
		}
		end := dec.InputOffset()
		start := bytes.Index(raw[valueStartHint:end], value)
		if start < 0 {
			return nil, errors.New("could not locate \"name\" value")
		}
		start += int(valueStartHint)
		out := make([]byte, 0, len(raw)-len(value)+len(name))
		out = append(out, raw[:start]...)
		out = append(out, name...)
		out = append(out, raw[end:]...)
		return out, nil
	}
	// No top-level "name": insert one after the opening brace, followed by
	// a comma when the object has other members.
	rest := bytes.TrimLeft(raw[afterBrace:], " \t\r\n")
	sep := ""
	if len(rest) > 0 && rest[0] != '}' {
		sep = ","
	}
	out := make([]byte, 0, len(raw)+len(name)+16)
	out = append(out, raw[:afterBrace]...)
	out = append(out, []byte("\n  \"name\": "+string(name)+sep)...)
	out = append(out, raw[afterBrace:]...)
	return out, nil
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
	for _, kv := range loadDotEnv(destDir) {
		if k, _, ok := strings.Cut(kv, "="); ok {
			existing[k] = true
		}
	}
	existingContent, err := os.ReadFile(envPath)
	if err != nil && !os.IsNotExist(err) {
		return err
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

// replaceModuleRefs rewrites every quoted reference to oldModule in raw — a
// `"` followed by oldModule and then either a closing `"` (the bare import)
// or a `/` (a subpackage path) — to newModule. The opening quote is what
// distinguishes an import/filter string from prose that merely mentions the
// path; the terminating `"`/`/` is what keeps a sibling module sharing the
// prefix (`"<old>-extras/x"`, `"<old>s/util"`) intact.
func replaceModuleRefs(raw []byte, oldModule, newModule string) []byte {
	needle := []byte(`"` + oldModule)
	var out []byte
	for {
		i := bytes.Index(raw, needle)
		if i < 0 {
			break
		}
		end := i + len(needle)
		if end < len(raw) && (raw[end] == '"' || raw[end] == '/') {
			out = append(out, raw[:i]...)
			out = append(out, '"')
			out = append(out, newModule...)
		} else {
			out = append(out, raw[:end]...)
		}
		raw = raw[end:]
	}
	if out == nil {
		return raw
	}
	return append(out, raw...)
}

// omitFromTemplate applies golang.org/x/mod/zip's module-directory
// publication policy to one WalkDir entry (p is the slash path relative to
// the template root): VCS directories (.bzr/.git/.hg/.svn), nested modules
// (a non-root directory holding a go.mod), files under a vendor/ tree other
// than vendor/modules.txt, and non-regular files are omitted. It returns
// omitted=true with the value the WalkDir callback should return (SkipDir
// for a directory, nil for a file).
func omitFromTemplate(src fs.FS, p string, d fs.DirEntry) (omitted bool, skip error) {
	if p == "." {
		return false, nil
	}
	if d.IsDir() {
		switch path.Base(p) {
		case ".bzr", ".git", ".hg", ".svn":
			return true, fs.SkipDir
		}
		if info, err := fs.Stat(src, path.Join(p, "go.mod")); err == nil && !info.IsDir() {
			return true, fs.SkipDir
		}
		return false, nil
	}
	if !d.Type().IsRegular() {
		return true, nil
	}
	if isVendoredPath(p) {
		return true, nil
	}
	return false, nil
}

// isVendoredPath mirrors x/mod/zip's (go1.24+) vendor rule: a file whose
// path contains a "vendor" element that is followed by at least one more
// directory element — i.e. a file inside a vendored *package* — is omitted,
// as is vendor/modules.txt.
func isVendoredPath(p string) bool {
	if p == "vendor/modules.txt" {
		return true
	}
	var i int
	if strings.HasPrefix(p, "vendor/") {
		i = len("vendor/")
	} else if j := strings.Index(p, "/vendor/"); j >= 0 {
		i = j + len("/vendor/")
	} else {
		return false
	}
	return strings.Contains(p[i:], "/")
}
