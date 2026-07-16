package examplegen

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadSortsAndJoins(t *testing.T) {
	exs, err := Load("testdata")
	if err != nil {
		t.Fatal(err)
	}
	if len(exs) != 2 {
		t.Fatalf("want 2 examples, got %d", len(exs))
	}
	if exs[0].Name != "Hello" || exs[1].Name != "Two files" {
		t.Fatalf("order wrong: %s, %s", exs[0].Name, exs[1].Name)
	}
	// single-file: verbatim source, package normalized? No — source is verbatim.
	if !strings.HasPrefix(exs[0].Source, "package views") {
		t.Fatalf("single source: %q", exs[0].Source)
	}
	// multi-file: txtar-joined, files sorted by name (lib before page).
	m := exs[1].Source
	if !strings.HasPrefix(m, "-- lib.gsx --\n") || !strings.Contains(m, "\n-- page.gsx --\n") {
		t.Fatalf("multi source not txtar-joined:\n%s", m)
	}
}

func TestTryPayloadRoundTrip(t *testing.T) {
	// Mirror the Vue decoder: base64 std → JSON → {s,i}.
	src, inv := "package views\n", "Hello()"
	payload := tryPayload(src, inv)
	raw, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		t.Fatal(err)
	}
	var o struct {
		S string `json:"s"`
		I string `json:"i"`
	}
	if err := json.Unmarshal(raw, &o); err != nil {
		t.Fatal(err)
	}
	if o.S != src || o.I != inv {
		t.Fatalf("round-trip mismatch: %+v", o)
	}
}

func TestPresetsJSON(t *testing.T) {
	exs, _ := Load("testdata")
	b, err := presetsJSON(exs)
	if err != nil {
		t.Fatal(err)
	}
	var got []map[string]string
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if got[0]["name"] != "Hello" || got[0]["source"] == "" || got[0]["invoke"] == "" || got[0]["category"] != "Basics" {
		t.Fatalf("preset[0] = %v", got[0])
	}
}

func TestRenderMarkdownProseEscape(t *testing.T) {
	exs := []Example{
		{
			Name:     "X",
			Summary:  "a <script> & <style> tag",
			Category: "Y",
			Files:    []SourceFile{{Name: "a.gsx", Body: "package views\n"}},
			Source:   "package views\n",
			Invoke:   "X()",
		},
	}
	md := string(RenderMarkdown(exs))
	if !strings.Contains(md, "a &lt;script&gt; &amp; &lt;style&gt; tag") {
		t.Errorf("summary not escaped in prose; got:\n%s", md)
	}
	if strings.Contains(md, "<script>") {
		t.Errorf("raw <script> found in prose output; got:\n%s", md)
	}
	if strings.Contains(md, "<style>") {
		t.Errorf("raw <style> found in prose output; got:\n%s", md)
	}
}

func TestLoadParsesPageAndRender(t *testing.T) {
	dir := t.TempDir()
	fixture := `-- doc --
name: Conditional
summary: a conditional attr
category: Basics
page: attributes
pageOrder: 30
-- input.gsx --
package views

component C(on bool) {
	<a { if on { class="x" } }>y</a>
}
-- invoke --
C(true)
-- render.golden --
<a class="x">y</a>
`
	if err := os.WriteFile(filepath.Join(dir, "x.txtar"), []byte(fixture), 0o644); err != nil {
		t.Fatal(err)
	}
	exs, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(exs) != 1 {
		t.Fatalf("want 1, got %d", len(exs))
	}
	e := exs[0]
	if e.Page != "attributes" || e.PageOrder != 30 {
		t.Fatalf("page route wrong: %q %d", e.Page, e.PageOrder)
	}
	if e.Render != "<a class=\"x\">y</a>\n" {
		t.Fatalf("render not loaded: %q", e.Render)
	}
}

func TestGenerateRoutesPartials(t *testing.T) {
	in := t.TempDir()
	routed := `-- doc --
name: Conditional Attr
summary: a conditional attr
category: Basics
page: attributes
pageOrder: 30
-- input.gsx --
package views

component C(on bool) {
	<a { if on { class="x" } }>y</a>
}
-- invoke --
C(true)
-- render.golden --
<a class="x">y</a>
`
	gallery := `-- doc --
name: Gallery Only
summary: stays in the gallery
category: Misc
-- input.gsx --
package views

component G() {
	<b>g</b>
}
-- invoke --
G()
-- render.golden --
<b>g</b>
`
	os.WriteFile(filepath.Join(in, "10-routed.txtar"), []byte(routed), 0o644)
	os.WriteFile(filepath.Join(in, "20-gallery.txtar"), []byte(gallery), 0o644)

	out := t.TempDir()
	md := filepath.Join(out, "examples.md")
	partials := filepath.Join(out, "_generated")
	if err := Generate(in, md, partials, filepath.Join(out, "d.json")); err != nil {
		t.Fatal(err)
	}

	partial, err := os.ReadFile(filepath.Join(partials, "attributes", "030-conditional-attr.md"))
	if err != nil {
		t.Fatalf("partial not written: %v", err)
	}
	ps := string(partial)
	for _, want := range []string{"```gsx", "Renders:", "```html", `<a class="x">y</a>`, "/playground#try="} {
		if !strings.Contains(ps, want) {
			t.Errorf("partial missing %q:\n%s", want, ps)
		}
	}
	if strings.Contains(ps, "# ") || strings.Contains(ps, "## ") {
		t.Errorf("partial must carry no heading:\n%s", ps)
	}

	mdBytes, _ := os.ReadFile(md)
	mdStr := string(mdBytes)
	if strings.Contains(mdStr, "Conditional Attr") {
		t.Errorf("routed example must NOT appear in examples.md")
	}
	if !strings.Contains(mdStr, "Gallery Only") {
		t.Errorf("gallery example must appear in examples.md")
	}

	dj, _ := os.ReadFile(filepath.Join(out, "d.json"))
	if !strings.Contains(string(dj), "Conditional Attr") || !strings.Contains(string(dj), "Gallery Only") {
		t.Errorf("playground presets must include all examples")
	}
}

// TestGenerateAllRoutedOmitsMdPath verifies that when every example is routed
// to a syntax page (gallery is empty), Generate does NOT write mdPath — and
// removes a pre-existing file — while still writing partials and presets JSON.
func TestGenerateAllRoutedOmitsMdPath(t *testing.T) {
	in := t.TempDir()
	routed := `-- doc --
name: Routed Only
summary: only routed, no gallery
category: Basics
page: expressions
pageOrder: 10
-- input.gsx --
package views

component R() {
	<span>r</span>
}
-- invoke --
R()
-- render.golden --
<span>r</span>
`
	if err := os.WriteFile(filepath.Join(in, "10-routed.txtar"), []byte(routed), 0o644); err != nil {
		t.Fatal(err)
	}

	out := t.TempDir()
	md := filepath.Join(out, "examples.md")
	partials := filepath.Join(out, "_generated")
	jsonOut := filepath.Join(out, "presets.json")

	// Pre-create md to prove Generate removes it when gallery is empty.
	if err := os.WriteFile(md, []byte("old content\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := Generate(in, md, partials, jsonOut); err != nil {
		t.Fatal(err)
	}

	// mdPath must NOT exist when gallery is empty.
	if _, err := os.Stat(md); !os.IsNotExist(err) {
		t.Errorf("expected mdPath to be absent when gallery empty, got err=%v", err)
	}

	// The partial for the routed example must exist.
	partialPath := filepath.Join(partials, "expressions", "010-routed-only.md")
	if _, err := os.Stat(partialPath); err != nil {
		t.Errorf("partial not written: %v", err)
	}

	// Presets JSON must contain the routed example.
	dj, err := os.ReadFile(jsonOut)
	if err != nil {
		t.Fatalf("presets JSON not written: %v", err)
	}
	if !strings.Contains(string(dj), "Routed Only") {
		t.Errorf("presets JSON missing routed example: %s", dj)
	}
}

func TestRenderMarkdown(t *testing.T) {
	exs, _ := Load("testdata")
	md := string(RenderMarkdown(exs))
	// category headings
	if !strings.Contains(md, "## Basics") || !strings.Contains(md, "## Components &amp; composition") {
		t.Fatalf("missing category headings:\n%s", md)
	}
	// example heading + summary + a gsx fence + a playground link
	if !strings.Contains(md, "### Hello") || !strings.Contains(md, "A greeting.") {
		t.Fatalf("missing example heading/summary")
	}
	if !strings.Contains(md, "```gsx") {
		t.Fatalf("missing gsx code fence")
	}
	if !strings.Contains(md, "/playground#try=") {
		t.Fatalf("missing playground link")
	}
	// multi-file example shows per-file captions
	if !strings.Contains(md, "**lib.gsx**") || !strings.Contains(md, "**page.gsx**") {
		t.Fatalf("multi-file example missing per-file captions:\n%s", md)
	}
}
