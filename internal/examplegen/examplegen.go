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

	"github.com/gsxhq/gsx/internal/txtar"
)

// Example is one fully-parsed fixture.
type Example struct {
	Name     string
	Summary  string
	Category string
	Order    int
	Source   string       // playground source string (single verbatim, or txtar-joined)
	Invoke   string
	Files    []SourceFile // individual source files, for per-file docs blocks
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
		case strings.HasSuffix(f.Name, ".gsx"):
			if strings.ContainsAny(f.Name, "/\\") {
				return Example{}, fmt.Errorf("source file %q must be a bare *.gsx (one package)", f.Name)
			}
			if pkg := packageName(f.Data); pkg != "views" {
				return Example{}, fmt.Errorf("source file %q is package %q, must be views", f.Name, pkg)
			}
			files = append(files, SourceFile{Name: f.Name, Body: string(f.Data)})
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
		b.WriteString("## " + cat + "\n\n")
		for _, e := range exs {
			if e.Category != cat {
				continue
			}
			b.WriteString("### " + e.Name + "\n\n")
			if e.Summary != "" {
				b.WriteString(e.Summary + "\n\n")
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

// Generate loads examplesDir and writes the docs Markdown to mdPath and the
// preset JSON to each path in jsonPaths.
func Generate(examplesDir, mdPath string, jsonPaths ...string) error {
	exs, err := Load(examplesDir)
	if err != nil {
		return err
	}
	if err := os.WriteFile(mdPath, RenderMarkdown(exs), 0o644); err != nil {
		return err
	}
	pj, err := presetsJSON(exs)
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
