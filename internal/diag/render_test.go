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
		Start: token.Position{Filename: "views.gsx", Line: 3, Column: 13},
		End:   token.Position{Filename: "views.gsx", Line: 3, Column: 16},
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
