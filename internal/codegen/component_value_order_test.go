package codegen

import "testing"

func TestExprHasOrderedOperation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		expr string
		want bool
	}{
		{name: "call", expr: "next()", want: true},
		{name: "conversion is conservatively call-shaped", expr: "string(value)", want: true},
		{name: "receive", expr: "<-ch", want: true},
		{name: "logical and", expr: "a && b", want: true},
		{name: "logical or", expr: "a || b", want: true},
		{name: "index", expr: "values[0]", want: false},
		{name: "selector", expr: "value.Name", want: false},
		{name: "inert composite literal", expr: `[]string{"a"}`, want: false},
		{name: "call in composite literal", expr: `[]string{next()}`, want: true},
		{name: "unexecuted function literal body", expr: `func() string { return next() }`, want: false},
		{name: "called function literal", expr: `(func() string { return next() })()`, want: true},
		{name: "parse failure", expr: ":", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := exprHasOrderedOperation(tt.expr); got != tt.want {
				t.Fatalf("exprHasOrderedOperation(%q) = %v, want %v", tt.expr, got, tt.want)
			}
		})
	}
}
