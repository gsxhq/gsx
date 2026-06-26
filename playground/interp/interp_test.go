package interp

import (
	"testing"

	"github.com/traefik/yaegi/interp"
	"github.com/traefik/yaegi/stdlib"
)

// TestYaegiInterpretsUnderGo126 de-risks the dependency: prove yaegi compiles
// and interprets Go under this toolchain (Go 1.26.1 is very new) and that its
// bundled stdlib symbols cover the small subset gsx-generated code uses.
func TestYaegiInterpretsUnderGo126(t *testing.T) {
	i := interp.New(interp.Options{})
	if err := i.Use(stdlib.Symbols); err != nil {
		t.Fatalf("Use(stdlib): %v", err)
	}
	const prog = `package main

import (
	"strconv"
	"strings"
)

func Run() string {
	return strings.ToUpper("hi") + strconv.Itoa(42)
}
`
	if _, err := i.Eval(prog); err != nil {
		t.Fatalf("Eval program: %v", err)
	}
	v, err := i.Eval("main.Run()")
	if err != nil {
		t.Fatalf("Eval call: %v", err)
	}
	if got := v.String(); got != "HI42" {
		t.Fatalf("interpreted Run() = %q, want HI42", got)
	}
}
