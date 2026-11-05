package gen

import (
	"strings"
	"testing"
)

func TestUnifiedDiffHunks(t *testing.T) {
	var a, b []string
	for i := range 30 {
		line := "line " + string(rune('a'+i%26)) + "\n"
		a = append(a, line)
		b = append(b, line)
	}
	b[5] = "changed f\n"
	b = append(b[:20], b[21:]...) // delete line 21
	got := unifiedDiff("p.gsx", []byte(strings.Join(a, "")), []byte(strings.Join(b, "")))
	want := `--- p.gsx.orig
+++ p.gsx
@@ -3,7 +3,7 @@
 line c
 line d
 line e
-line f
+changed f
 line g
 line h
 line i
@@ -18,7 +18,6 @@
 line r
 line s
 line t
-line u
 line v
 line w
 line x
`
	if got != want {
		t.Fatalf("diff:\n%s\nwant:\n%s", got, want)
	}
	if d := unifiedDiff("p.gsx", []byte("x\n"), []byte("x\n")); d != "" {
		t.Fatalf("equal inputs diff = %q", d)
	}
	// Changes within 2*context merge into one hunk; an empty side is 0,0.
	if d := unifiedDiff("p.gsx", nil, []byte("new\n")); d != "--- p.gsx.orig\n+++ p.gsx\n@@ -0,0 +1,1 @@\n+new\n" {
		t.Fatalf("insert into empty = %q", d)
	}
}
