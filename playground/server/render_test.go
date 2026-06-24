package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var testRenderer *renderer

func TestMain(m *testing.M) {
	// gsxMod is the module root two levels up from playground/server.
	wd, _ := os.Getwd()
	gsxMod := filepath.Clean(filepath.Join(wd, "..", ".."))
	r, err := newRenderer(gsxMod, "")
	if err != nil {
		println("setup failed:", err.Error())
		os.Exit(1)
	}
	testRenderer = r
	os.Exit(m.Run())
}

func TestRenderSuccess(t *testing.T) {
	resp := testRenderer.render(renderReq{
		GSX:    "package views\n\ncomponent Greeting(name string) {\n\t<p>Hi {name}</p>\n}\n",
		Invoke: `Greeting(GreetingProps{Name: "World"})`,
	})
	if resp.Error != "" || len(resp.Diagnostics) != 0 {
		t.Fatalf("unexpected error/diags: %q %+v", resp.Error, resp.Diagnostics)
	}
	if got := strings.TrimSpace(resp.HTML); got != "<p>Hi World</p>" {
		t.Fatalf("html = %q", got)
	}
	if !strings.Contains(resp.GeneratedGo, "func Greeting(") {
		t.Fatalf("generatedGo missing func: %q", resp.GeneratedGo)
	}
}

func TestRenderDiagnostic(t *testing.T) {
	resp := testRenderer.render(renderReq{
		GSX:    "package views\n\ncomponent Bad() {\n\t<p>{missing}</p>\n}\n",
		Invoke: "Bad(BadProps{})",
	})
	if len(resp.Diagnostics) == 0 {
		t.Fatalf("expected a diagnostic, got none (err=%q)", resp.Error)
	}
	d := resp.Diagnostics[0]
	if d.Severity != "error" || d.Line == 0 {
		t.Fatalf("bad diagnostic: %+v", d)
	}
}

func TestRenderEscaping(t *testing.T) {
	resp := testRenderer.render(renderReq{
		GSX:    "package views\n\ncomponent C(s string) {\n\t<div>{s}</div>\n}\n",
		Invoke: `C(CProps{S: "<script>alert(1)</script>"})`,
	})
	if strings.Contains(resp.HTML, "<script>") {
		t.Fatalf("unescaped output: %q", resp.HTML)
	}
	if !strings.Contains(resp.HTML, "&lt;script&gt;") {
		t.Fatalf("expected escaped output, got: %q", resp.HTML)
	}
}
