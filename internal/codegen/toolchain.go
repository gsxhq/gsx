package codegen

import (
	goparser "go/parser"
	"go/token"
	"sync"
)

// toolchainHasGenericMethods reports whether the ACTIVE toolchain's go/parser
// accepts methods with type parameters (accepted for go1.27; rejected by all
// earlier releases). Probed once per process by parsing a canonical generic
// method — the same parser that will consume our emitted skeletons, so the
// probe can't drift from reality.
var toolchainHasGenericMethods = sync.OnceValue(func() bool {
	const src = "package p\ntype S struct{}\nfunc (S) M[T any](v T) T { return v }\n"
	_, err := goparser.ParseFile(token.NewFileSet(), "generic_method_probe.go", src, 0)
	return err == nil
})
