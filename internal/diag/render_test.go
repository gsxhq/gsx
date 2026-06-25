package diag

import (
	"bytes"
	"go/token"
	"strings"
	"testing"
)

func TestRenderRichSnippet(t *testing.T) {
	src := func(name string) ([]byte, bool) {
		if name == "views.gsx" {
			return []byte("package p\n\ncomponent X(ctx string) {\n}\n"), true
		}
		return nil, false
	}
	var buf bytes.Buffer
	RenderRich(&buf, []Diagnostic{{
		Start:    token.Position{Filename: "views.gsx", Line: 3, Column: 13},
		End:      token.Position{Filename: "views.gsx", Line: 3, Column: 16},
		Severity: Error, Code: "reserved-param",
		Message: `param name "ctx" is reserved`, Help: "rename the parameter",
	}}, src)
	out := buf.String()
	for _, want := range []string{
		`error[reserved-param]: param name "ctx" is reserved`,
		`--> views.gsx:3:13`,
		`component X(ctx string) {`,
		`^^^`,
		`= help: rename the parameter`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("rich output missing %q:\n%s", want, out)
		}
	}
}

func TestRenderRichDegradesWithoutSource(t *testing.T) {
	var buf bytes.Buffer
	RenderRich(&buf, []Diagnostic{{
		Start: token.Position{Filename: "x.gsx", Line: 2, Column: 4}, Severity: Error, Message: "m",
	}}, nil)
	if !strings.Contains(buf.String(), "x.gsx:2:4: error: m") {
		t.Errorf("expected compact degradation, got:\n%s", buf.String())
	}
}

func TestRenderCompactPositionless(t *testing.T) {
	var buf bytes.Buffer
	// A positionless diagnostic (zero Start) must NOT print a "0:0:" prefix —
	// just "severity[code]: message" (e.g. a parser error whose position lives
	// in the message text in Slice 1).
	RenderCompact(&buf, []Diagnostic{
		{Severity: Error, Message: "index.gsx: 28:3: mismatched close tag </Layout>", Source: "parser"},
	})
	got := buf.String()
	if strings.Contains(got, ":0:0:") {
		t.Errorf("positionless diagnostic must not print a :0:0: prefix:\n%s", got)
	}
	if got != "error: index.gsx: 28:3: mismatched close tag </Layout>\n" {
		t.Errorf("compact positionless render wrong:\n%q", got)
	}
}

func TestRenderRichPositionless(t *testing.T) {
	var buf bytes.Buffer
	RenderRich(&buf, []Diagnostic{
		{Severity: Error, Code: "syntax", Message: "mismatched close tag", Help: "close the open tag"},
	}, nil)
	got := buf.String()
	if strings.Contains(got, ":0:0:") || strings.Contains(got, "-->") {
		t.Errorf("positionless rich render must not print a :0:0: prefix or --> location:\n%s", got)
	}
	if !strings.Contains(got, "error[syntax]: mismatched close tag") {
		t.Errorf("rich positionless render missing header:\n%s", got)
	}
	if !strings.Contains(got, "= help: close the open tag") {
		t.Errorf("rich positionless render should still show help:\n%s", got)
	}
}
