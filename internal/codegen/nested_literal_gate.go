package codegen

import (
	"strings"

	gsxparser "github.com/gsxhq/gsx/parser"
)

// isWholeLiteral reports whether src is exactly one prefixed literal (an
// f, js or css literal, either delimiter) and nothing else: a braced
// attribute value of that shape has its own lowering and is not nested.
func isWholeLiteral(src string) bool {
	cs := gsxparser.EmbeddedConstructs(src)
	return len(cs) == 1 && !cs[0].IsElement && cs[0].Off == 0 && cs[0].End == len(strings.TrimRight(src, " \t\r\n"))
}
