package gsx

import (
	"fmt"
	"reflect"
	"strconv"
	"strings"
)

// valKind reports what a dynamically-typed renderable value is, so callers can
// act on the classification rather than re-deriving it.
//
//   - kindString — arbitrary content; the caller MUST HTML-escape it.
//   - kindBool   — "true"/"false"; escape-free.
//   - kindNumber — strconv's digit/sign/exponent/Inf/NaN charset; escape-free.
type valKind uint8

const (
	kindInvalid valKind = iota
	kindString
	kindBool
	kindNumber
)

// anyRenderVal is gsx's single runtime value classifier: it maps a dynamically
// typed value to its text form and kind, keyed on the value's UNDERLYING type.
//
// It is the runtime mirror of codegen's classify (internal/codegen/analyze.go),
// which reads t.Underlying() via go/types — so a named scalar (type Flag bool)
// renders the same whether gsx saw its type at generate time or only sees the
// boxed value now.
//
// Order mirrors classify exactly: Stringer is checked BEFORE the underlying
// kind, so time.Duration (a named int64 with String()) renders "1s" rather than
// its integer value. Callers that also handle Node/[]Node must check those
// first — reflect cannot see interface satisfaction the way go/types can.
func anyRenderVal(v any) (string, valKind, bool) {
	if v == nil {
		return "", kindInvalid, false
	}
	if s, ok := v.(fmt.Stringer); ok {
		return s.String(), kindString, true
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.String:
		return rv.String(), kindString, true
	case reflect.Bool:
		return strconv.FormatBool(rv.Bool()), kindBool, true
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return strconv.FormatInt(rv.Int(), 10), kindNumber, true
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return strconv.FormatUint(rv.Uint(), 10), kindNumber, true
	case reflect.Float32, reflect.Float64:
		return strconv.FormatFloat(rv.Float(), 'g', -1, 64), kindNumber, true
	case reflect.Slice:
		et := rv.Type().Elem()
		if et.Kind() == reflect.Uint8 { // []byte and any named byte-slice
			b := make([]byte, rv.Len())
			reflect.Copy(reflect.ValueOf(b), rv)
			return string(b), kindString, true
		}
		// []string joins with single spaces. The element must be exactly `string`,
		// not merely string-kinded: codegen lowers the static form to strings.Join,
		// whose parameter is []string, so a named element ([]Slug) cannot compile
		// there. Matching that restriction here keeps the static and runtime paths
		// identical — a named SLICE (type Tags []string) still joins, because its
		// element is string. See classify in internal/codegen/analyze.go.
		if et == reflect.TypeFor[string]() {
			parts := make([]string, rv.Len())
			for i := range parts {
				parts[i] = rv.Index(i).String()
			}
			return strings.Join(parts, " "), kindString, true
		}
	}
	return "", kindInvalid, false
}
