package parser

import (
	"go/token"
	"testing"
)

// TestSoundnessNoDesync feeds inputs that previously desynced go/scanner over
// markup prose (C1) or mis-located the control-flow body brace (I2). Each must
// parse without error.
func TestSoundnessNoDesync(t *testing.T) {
	cases := []struct {
		name string
		src  string
	}{
		{"interp after apostrophe", "package p\ncomponent C(n int) { <p>Today's items: {n}</p> }"},
		{"goblock after apostrophe", "package p\ncomponent C(name string) { <p>it's {{ x := f(name) }}{x}</p> }"},
		{"class after apostrophe", "package p\ncomponent C(a string) { <p>don't <b class={a}>x</b></p> }"},
		{"if after apostrophe", "package p\ncomponent C(c bool) { <p>you're {n} <span>{ if c { <a/> } }</span></p> }"},
		{"spread after apostrophe", "package p\ncomponent C() { <p>Jack's <input {...attrs}/></p> }"},
		{"apostrophe in control body", "package p\ncomponent C(c bool) { { if c { <p>it's here</p> } } }"},
		{"apostrophe in nested element", "package p\ncomponent C() { <ul><li>can't</li><li>won't</li></ul> }"},
		{"multi-component apostrophe", "package p\ncomponent A() { <p>Jack's</p> }\ncomponent B() { <span>ok</span> }"},
		{"for range slice literal", "package p\ncomponent C() { <ul>{ for _, v := range []int{1,2} { <li>{v}</li> } }</ul> }"},
		{"for range map literal", "package p\ncomponent C() { { for k := range map[string]int{\"a\":1} { <i>{k}</i> } } }"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ParseFile(token.NewFileSet(), "t.gsx", tc.src, 0); err != nil {
				t.Fatalf("parse error: %v", err)
			}
		})
	}
}

// TestSoundnessCleanErrors confirms genuinely malformed inputs still fail fast
// with a position, no panic, no hang.
func TestSoundnessCleanErrors(t *testing.T) {
	bad := []string{
		"package p\ncomponent C() { <p>hi</p>",          // unterminated body
		"package p\ncomponent C(n int) { {n }",          // unterminated interp/body
		"package p\ncomponent C() { <input {...attrs }", // unterminated tag/body
	}
	for _, src := range bad {
		if _, err := ParseFile(token.NewFileSet(), "t.gsx", src, 0); err == nil {
			t.Fatalf("expected error for %q, got nil", src)
		}
	}
}
