package examplegen

import (
	"encoding/base64"
	"encoding/json"
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
	src, inv := "package views\n", "Hello(HelloProps{})"
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

func TestRenderMarkdown(t *testing.T) {
	exs, _ := Load("testdata")
	md := string(RenderMarkdown(exs))
	// category headings
	if !strings.Contains(md, "## Basics") || !strings.Contains(md, "## Components & composition") {
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
