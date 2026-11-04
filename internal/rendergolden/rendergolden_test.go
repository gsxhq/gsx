package rendergolden

import (
	"testing"

	"github.com/gsxhq/gsx/internal/txtar"
)

func TestExactArchiveRoundTrip(t *testing.T) {
	for _, html := range []string{"", "x", "x\n", "x\n\n", "\n", "\r\n", " \t<p>a\n b</p>\t ", "<p>\r\n\tx</p>\r\n"} {
		t.Run(html, func(t *testing.T) {
			encoded := Encode(html)
			if html != "" && string(encoded) != html+"\n" {
				t.Fatalf("framed bytes = %q, want %q", encoded, html+"\n")
			}
			archive := txtar.Format(&txtar.Archive{Files: []txtar.File{{Name: "render.golden", Data: encoded}, {Name: "next", Data: []byte("other\n")}}})
			parsed := txtar.Parse(archive)
			got, err := Decode(parsed.Files[0].Data)
			if err != nil || got != html {
				t.Fatalf("round trip = %q, %v; want %q", got, err, html)
			}
		})
	}
}

func TestMissingFrame(t *testing.T) {
	if _, err := Decode([]byte("<p>x</p>")); err == nil {
		t.Fatal("accepted a nonempty section without its framing LF")
	}
}
