package lsp

import "testing"

func TestURIPathRoundTrip(t *testing.T) {
	cases := []string{"/x/page.gsx", "/a b/c.gsx", "/p/ünïcode.gsx"}
	for _, p := range cases {
		if got := uriToPath(pathToURI(p)); got != p {
			t.Fatalf("round trip %q -> %q", p, got)
		}
	}
	if got := pathToURI("/x/page.gsx"); got != "file:///x/page.gsx" {
		t.Fatalf("pathToURI = %q", got)
	}
}

func TestDocStoreOpenInDir(t *testing.T) {
	s := newDocStore()
	s.open(pathToURI("/proj/page.gsx"), "A", 1)
	s.open(pathToURI("/proj/card.gsx"), "B", 1)
	s.open(pathToURI("/other/x.gsx"), "C", 1)
	got := s.openInDir("/proj")
	if len(got) != 2 || got["/proj/page.gsx"] != "A" || got["/proj/card.gsx"] != "B" {
		t.Fatalf("openInDir = %v", got)
	}
	s.update(pathToURI("/proj/page.gsx"), "A2", 2)
	if txt, _ := s.text(pathToURI("/proj/page.gsx")); txt != "A2" {
		t.Fatalf("text after update = %q", txt)
	}
	s.close(pathToURI("/proj/page.gsx"))
	if _, ok := s.text(pathToURI("/proj/page.gsx")); ok {
		t.Fatal("doc still present after close")
	}
}
