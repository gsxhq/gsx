package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
)

var testPool *pool

func TestMain(m *testing.M) {
	// gsxMod is the module root two levels up from playground/server.
	wd, _ := os.Getwd()
	gsxMod := filepath.Clean(filepath.Join(wd, "..", ".."))
	p, err := newPool(gsxMod, "", 2)
	if err != nil {
		println("setup failed:", err.Error())
		os.Exit(1)
	}
	testPool = p
	os.Exit(m.Run())
}

func TestRenderSuccess(t *testing.T) {
	resp := testPool.render(renderReq{
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
	resp := testPool.render(renderReq{
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
	resp := testPool.render(renderReq{
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

func TestOversizeRejected(t *testing.T) {
	h := makeRenderHandler(testPool)
	big := bytes.Repeat([]byte("x"), 70*1024)
	body := `{"gsx":"` + string(big) + `","invoke":"Hello(HelloProps{})"}`
	req := httptest.NewRequest("POST", "/render", strings.NewReader(body))
	rec := httptest.NewRecorder()
	withLimits(h, 64*1024, make(chan struct{}, 1)).ServeHTTP(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("code = %d, want 413", rec.Code)
	}
}

func TestMethodGuard(t *testing.T) {
	h := makeRenderHandler(testPool)
	req := httptest.NewRequest("GET", "/render", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("code = %d, want 405", rec.Code)
	}
}

func TestImportRejected(t *testing.T) {
	resp := testPool.render(renderReq{
		GSX:    "package views\n\nimport \"net/http\"\n\ncomponent C() {\n\t<p>{http.MethodGet}</p>\n}\n",
		Invoke: "C(CProps{})",
	})
	if resp.HTML != "" {
		t.Fatalf("expected rejection, got html %q", resp.HTML)
	}
	found := false
	for _, d := range resp.Diagnostics {
		if d.Severity == "error" && strings.Contains(d.Message, "not allowed") && strings.Contains(d.Message, "net/http") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected an 'import not allowed: net/http' diagnostic, got %+v (err=%q)", resp.Diagnostics, resp.Error)
	}
}

func TestAllowedImportRenders(t *testing.T) {
	resp := testPool.render(renderReq{
		GSX:    "package views\n\nimport \"strings\"\n\ncomponent C(s string) {\n\t<p>{strings.ToUpper(s)}</p>\n}\n",
		Invoke: `C(CProps{S: "hi"})`,
	})
	if resp.Error != "" || len(resp.Diagnostics) != 0 {
		t.Fatalf("unexpected error/diags: %q %+v", resp.Error, resp.Diagnostics)
	}
	if strings.TrimSpace(resp.HTML) != "<p>HI</p>" {
		t.Fatalf("html = %q", resp.HTML)
	}
}

func TestPoolConcurrent(t *testing.T) {
	wd, _ := os.Getwd()
	gsxMod := filepath.Clean(filepath.Join(wd, "..", ".."))
	p, err := newPool(gsxMod, "", 3)
	if err != nil {
		t.Fatal(err)
	}
	const n = 12
	results := make([]string, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			name := "U" + strconv.Itoa(i)
			resp := p.render(renderReq{
				GSX:    "package views\n\ncomponent G(name string) {\n\t<p>{name}</p>\n}\n",
				Invoke: `G(GProps{Name: "` + name + `"})`,
			})
			results[i] = strings.TrimSpace(resp.HTML)
		}(i)
	}
	wg.Wait()
	for i := 0; i < n; i++ {
		want := "<p>U" + strconv.Itoa(i) + "</p>"
		if results[i] != want {
			t.Errorf("req %d: got %q want %q", i, results[i], want)
		}
	}
}

// --- splitSources unit tests ---

func TestSplitSourcesSingle(t *testing.T) {
	got, err := splitSources("package foo\n\ncomponent A() { <p>x</p> }\n")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 file, got %d", len(got))
	}
	b, ok := got["comp.gsx"]
	if !ok {
		t.Fatal("single-file source must map to comp.gsx")
	}
	if string(b) != "package views\n\ncomponent A() { <p>x</p> }\n" {
		t.Fatalf("package line not normalized: %q", b)
	}
}

func TestSplitSourcesMulti(t *testing.T) {
	src := "-- a.gsx --\npackage views\n\ncomponent A() { <p>a</p> }\n-- b.gsx --\npackage views\n\ncomponent B() { <p>b</p> }\n"
	got, err := splitSources(src)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got["a.gsx"] == nil || got["b.gsx"] == nil {
		t.Fatalf("want a.gsx+b.gsx, got %v", keys(got))
	}
}

func TestSplitSourcesRejectsBadName(t *testing.T) {
	if _, err := splitSources("-- ../evil.gsx --\npackage views\n"); err == nil {
		t.Fatal("expected error for path-traversal file name")
	}
	if _, err := splitSources("-- sub/x.gsx --\npackage views\n"); err == nil {
		t.Fatal("expected error for nested file name")
	}
	if _, err := splitSources("-- notes.txt --\npackage views\n"); err == nil {
		t.Fatal("expected error for non-.gsx file name")
	}
}

func keys(m map[string][]byte) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

// --- multi-file integration test ---

func TestRenderMultiFile(t *testing.T) {
	if testing.Short() {
		t.Skip("multi-file render needs the toolchain; skipped in -short")
	}
	src := "-- comp.gsx --\npackage views\n\ncomponent Card(title string) { <section>{title}{children}</section> }\n" +
		"-- page.gsx --\npackage views\n\ncomponent Page() { <Card title=\"Hi\"><em>x</em></Card> }\n"
	resp := testPool.render(renderReq{GSX: src, Invoke: "Page(PageProps{})"})
	if resp.Error != "" || len(resp.Diagnostics) > 0 {
		t.Fatalf("render error: %s diags=%v", resp.Error, resp.Diagnostics)
	}
	want := "<section>Hi<em>x</em></section>"
	if resp.HTML != want {
		t.Fatalf("HTML = %q want %q", resp.HTML, want)
	}
}
