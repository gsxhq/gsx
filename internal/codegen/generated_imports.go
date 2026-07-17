package codegen

import (
	"fmt"
	"go/types"
	"maps"
)

// generatedImportAllocator owns the reserved aliases used to spell semantic
// types in generated positional calls.
type generatedImportAllocator struct {
	prefix string
	next   int
	byPath map[string]string
	order  []importSpec
}

func newGeneratedImportAllocator(prefix string) *generatedImportAllocator {
	return &generatedImportAllocator{prefix: prefix, byPath: map[string]string{}}
}

func (a *generatedImportAllocator) alloc(path string) string {
	if alias, ok := a.byPath[path]; ok {
		return alias
	}
	a.next++
	alias := fmt.Sprintf("%s%d", a.prefix, a.next)
	a.byPath[path] = alias
	a.order = append(a.order, importSpec{name: alias, path: path})
	return alias
}

func (a *generatedImportAllocator) specs() []importSpec {
	return a.order
}

type generatedImportTxn struct {
	owner   *generatedImportAllocator
	baseLen int
	work    *generatedImportAllocator
}

func (a *generatedImportAllocator) begin() *generatedImportTxn {
	work := &generatedImportAllocator{
		prefix: a.prefix,
		next:   a.next,
		byPath: maps.Clone(a.byPath),
		order:  append([]importSpec(nil), a.order...),
	}
	return &generatedImportTxn{owner: a, baseLen: len(a.order), work: work}
}

func (t *generatedImportTxn) qualifier(current *types.Package) types.Qualifier {
	return func(p *types.Package) string {
		if p == nil || p == current {
			return ""
		}
		return t.work.alloc(p.Path())
	}
}

func (t *generatedImportTxn) commit() {
	if len(t.owner.order) != t.baseLen {
		panic("codegen: generated import allocator mutated during an open transaction")
	}
	t.owner.next = t.work.next
	t.owner.byPath = t.work.byPath
	t.owner.order = t.work.order
}
