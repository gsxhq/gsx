// Package examplegen turns the single-source examples/*.txtar fixtures into the
// docs Examples page and the playground preset lists. It is the one place that
// knows the playground source string format and the #try= payload encoding, so
// docs, frontend presets, and backend cache-seed can never drift.
package examplegen

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/gsxhq/gsx/gen"
	"github.com/gsxhq/gsx/internal/txtar"
)

// Example is one fully-parsed fixture.
type Example struct {
	Name      string
	Summary   string
	Category  string
	Order     int
	Page      string // syntax-page slug; "" = gallery only
	PageOrder int    // order within the page (falls back to Order)
	Source    string // playground source string (single verbatim, or txtar-joined)
	Invoke    string
	Render    string // rendered HTML (render.golden); required when Page != ""
	Files     []SourceFile // individual source files, for per-file docs blocks
}

// SourceFile is one .gsx file of an example.
type SourceFile struct {
	Name string
	Body string
}

// Load reads every *.txtar in dir into Examples, sorted by (Order, filename).
func Load(dir string) ([]Example, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	type loaded struct {
		ex   Example
		file string
	}
	var ls []loaded
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".txtar") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		ex, err := loadOne(path)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", e.Name(), err)
		}
		ls = append(ls, loaded{ex, e.Name()})
	}
	sort.SliceStable(ls, func(i, j int) bool {
		if ls[i].ex.Order != ls[j].ex.Order {
			return ls[i].ex.Order < ls[j].ex.Order
		}
		return ls[i].file < ls[j].file
	})
	out := make([]Example, len(ls))
	for i, l := range ls {
		out[i] = l.ex
	}
	return out, nil
}

func loadOne(path string) (Example, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Example{}, err
	}
	arc := txtar.Parse(data)
	var ex Example
	var files []SourceFile
	var invoke string
	hasDoc := false
	for _, f := range arc.Files {
		switch {
		case f.Name == "doc":
			hasDoc = true
			parseDoc(&ex, f.Data)
		case f.Name == "invoke":
			invoke = strings.TrimSpace(string(f.Data))
		case f.Name == "render.golden":
			ex.Render = string(f.Data)
		case strings.HasSuffix(f.Name, ".gsx"):
			if strings.ContainsAny(f.Name, "/\\") {
				return Example{}, fmt.Errorf("source file %q must be a bare *.gsx (one package)", f.Name)
			}
			if pkg := packageName(f.Data); pkg != "views" {
				return Example{}, fmt.Errorf("source file %q is package %q, must be views", f.Name, pkg)
			}
			// Canonicalize the displayed source with the same formatter as
			// `gsx fmt` / the playground, so docs + presets are never one-liners.
			formatted, ferr := gen.Format(f.Name, f.Data)
			if ferr != nil {
				return Example{}, fmt.Errorf("format %q: %w", f.Name, ferr)
			}
			files = append(files, SourceFile{Name: f.Name, Body: string(formatted)})
		}
	}
	if !hasDoc {
		return Example{}, fmt.Errorf("missing -- doc -- section")
	}
	if len(files) == 0 {
		return Example{}, fmt.Errorf("no .gsx source files")
	}
	if invoke == "" {
		return Example{}, fmt.Errorf("missing -- invoke -- section")
	}
	if ex.Page != "" && ex.Render == "" {
		return Example{}, fmt.Errorf("routed example (page: %s) requires a -- render.golden -- section", ex.Page)
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Name < files[j].Name })
	ex.Files = files
	ex.Invoke = invoke
	ex.Source = joinSource(files)
	return ex, nil
}

// joinSource returns the playground source string: a single file verbatim, or
// multiple files in Go-Playground txtar format (sorted by name).
func joinSource(files []SourceFile) string {
	if len(files) == 1 {
		return files[0].Body
	}
	var b strings.Builder
	for _, f := range files {
		b.WriteString("-- " + f.Name + " --\n")
		body := f.Body
		if !strings.HasSuffix(body, "\n") {
			body += "\n"
		}
		b.WriteString(body)
	}
	return b.String()
}

func parseDoc(ex *Example, b []byte) {
	for _, line := range strings.Split(string(b), "\n") {
		key, val, ok := strings.Cut(strings.TrimSpace(line), ":")
		if !ok {
			continue
		}
		key, val = strings.TrimSpace(key), strings.TrimSpace(val)
		switch key {
		case "name":
			ex.Name = val
		case "summary":
			ex.Summary = val
		case "category":
			ex.Category = val
		case "order":
			if n, err := strconv.Atoi(val); err == nil {
				ex.Order = n
			}
		case "page":
			ex.Page = val
		case "pageOrder":
			if n, err := strconv.Atoi(val); err == nil {
				ex.PageOrder = n
			}
		}
	}
}

func packageName(src []byte) string {
	for _, line := range strings.Split(string(src), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "package ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "package "))
		}
	}
	return ""
}

// tryPayload encodes {s:source, i:invoke} as the #try= hash value: JSON →
// std base64. Matches the Vue decoder (atob over UTF-8 → JSON.parse → o.s/o.i).
func tryPayload(source, invoke string) string {
	b, _ := json.Marshal(struct {
		S string `json:"s"`
		I string `json:"i"`
	}{source, invoke})
	return base64.StdEncoding.EncodeToString(b)
}

// proseEscaper HTML-escapes angle brackets and ampersands in prose fields so
// VitePress/Vue does not interpret them as HTML elements. Single-pass so
// inserted entities are not double-escaped.
var proseEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")

// RenderMarkdown emits the docs Examples page: a fixed intro, then examples
// grouped under ## {category} headings (categories in first-seen order), each
// with its summary, one ```gsx block per source file (captioned when >1 file),
// and an Open-in-Playground link.
func RenderMarkdown(exs []Example) []byte {
	var b strings.Builder
	b.WriteString("# Examples\n\n")
	b.WriteString("A gallery of gsx features. Each example is compiled and checked in CI; click **Open in Playground** to run and edit it live.\n\n")
	b.WriteString("<!-- GENERATED by cmd/gsx-examples from examples/*.txtar — do not edit by hand. -->\n\n")

	var order []string
	seen := map[string]bool{}
	for _, e := range exs {
		if !seen[e.Category] {
			seen[e.Category] = true
			order = append(order, e.Category)
		}
	}
	for _, cat := range order {
		b.WriteString("## " + proseEscaper.Replace(cat) + "\n\n")
		for _, e := range exs {
			if e.Category != cat {
				continue
			}
			b.WriteString("### " + proseEscaper.Replace(e.Name) + "\n\n")
			if e.Summary != "" {
				b.WriteString(proseEscaper.Replace(e.Summary) + "\n\n")
			}
			for _, f := range e.Files {
				if len(e.Files) > 1 {
					b.WriteString("**" + f.Name + "**\n\n")
				}
				b.WriteString("```gsx\n")
				body := f.Body
				if !strings.HasSuffix(body, "\n") {
					body += "\n"
				}
				b.WriteString(body)
				b.WriteString("```\n\n")
			}
			b.WriteString("[▶ Open in Playground](/playground#try=" + tryPayload(e.Source, e.Invoke) + ")\n\n")
		}
	}
	return []byte(b.String())
}

// slug lowercases name and collapses non-alphanumerics to single dashes.
func slug(name string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(name) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			dash = false
		} else if !dash {
			b.WriteByte('-')
			dash = true
		}
	}
	return strings.Trim(b.String(), "-")
}

// partialRelPath is "<page>/<NNN>-<slug>.md".
func partialRelPath(e Example) string {
	n := e.PageOrder
	if n == 0 {
		n = e.Order
	}
	return filepath.Join(e.Page, fmt.Sprintf("%03d-%s.md", n, slug(e.Name)))
}

// RenderPartial emits one routed example as an include partial: source gsx,
// rendered html, and a Playground link. No heading — the page owns headings.
func RenderPartial(e Example) []byte {
	var b strings.Builder
	b.WriteString("<!-- GENERATED from examples/*.txtar by cmd/gsx-examples — do not edit. -->\n\n")
	for _, f := range e.Files {
		if len(e.Files) > 1 {
			b.WriteString("**" + f.Name + "**\n\n")
		}
		b.WriteString("```gsx\n")
		body := f.Body
		if !strings.HasSuffix(body, "\n") {
			body += "\n"
		}
		b.WriteString(body)
		b.WriteString("```\n\n")
	}
	b.WriteString("Renders:\n\n```html\n")
	r := e.Render
	if !strings.HasSuffix(r, "\n") {
		r += "\n"
	}
	b.WriteString(r)
	b.WriteString("```\n\n")
	b.WriteString("[▶ Open in Playground](/playground#try=" + tryPayload(e.Source, e.Invoke) + ")\n")
	return []byte(b.String())
}

// Generate loads examplesDir, writes routed examples as partials under
// partialsDir, the unrouted gallery to mdPath, and presets to each jsonPaths.
func Generate(examplesDir, mdPath, partialsDir string, jsonPaths ...string) error {
	exs, err := Load(examplesDir)
	if err != nil {
		return err
	}
	// Rebuild the partials tree from scratch so renamed/removed examples leave
	// no orphan partials (the drift check would otherwise miss the deletion).
	if err := os.RemoveAll(partialsDir); err != nil {
		return err
	}
	var gallery []Example
	for _, e := range exs {
		if e.Page == "" {
			gallery = append(gallery, e)
			continue
		}
		full := filepath.Join(partialsDir, partialRelPath(e))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(full, RenderPartial(e), 0o644); err != nil {
			return err
		}
	}
	if err := os.WriteFile(mdPath, RenderMarkdown(gallery), 0o644); err != nil {
		return err
	}
	pj, err := presetsJSON(exs) // playground shows ALL examples
	if err != nil {
		return err
	}
	for _, p := range jsonPaths {
		if err := os.WriteFile(p, pj, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// presetsJSON renders the preset list for the frontend + backend.
func presetsJSON(exs []Example) ([]byte, error) {
	type preset struct {
		Name     string `json:"name"`
		Category string `json:"category"`
		Source   string `json:"source"`
		Invoke   string `json:"invoke"`
	}
	out := make([]preset, len(exs))
	for i, e := range exs {
		out[i] = preset{e.Name, e.Category, e.Source, e.Invoke}
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	if err := enc.Encode(out); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
