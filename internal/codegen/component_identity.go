package codegen

import (
	"fmt"
	"go/types"

	"github.com/gsxhq/gsx/internal/tagcallable"
)

func componentResultType(sig *types.Signature, runtime runtimeContract) (types.Type, error) {
	if sig == nil {
		return nil, fmt.Errorf("component-signature: nil callable signature")
	}
	checked := make(map[types.Type]bool)
	if invalidSemanticTypeSeen(runtime.node, checked) {
		return nil, fmt.Errorf("component-signature-runtime: incomplete runtime node type")
	}
	results := sig.Results()
	if results.Len() != 1 {
		return nil, fmt.Errorf("component-result-count: callable has %d results; want exactly one", results.Len())
	}
	result := results.At(0).Type()
	// The assignability half of this check is single-sourced through
	// tagcallable.IsResult (shared with internal/lsp's mirrored tag-value
	// scan); the result count above stays a separate, earlier check so its
	// diagnostic can distinguish "wrong count" from "wrong type".
	if invalidSemanticTypeSeen(result, checked) || !tagcallable.IsResult(sig, runtime.node) {
		return nil, fmt.Errorf("component-result-type: result %s is not assignable to %s", result, runtime.node)
	}
	return result, nil
}
