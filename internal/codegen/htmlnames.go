package codegen

// htmlElementNames is the WHATWG HTML living-standard element name index
// (https://html.spec.whatwg.org/multipage/indices.html#elements-3) plus the
// svg and math foreign-content roots: all ~115 current elements, a..wbr.
//
// Used ONLY by the self-reference diagnostic in the component-call preprocessor — never
// by resolution itself (see the 2026-07-10 spec: no reserved table in
// resolution). Resolution treats every lowercase identifier tag uniformly
// (declared name → component, self-name → leaf, else → leaf); this table
// exists solely to decide whether a self-excluded tag is a deliberate
// wrapper (div, span, ...) or a near-certain recursion mistake (item, card,
// ... — not a real HTML element) worth warning about.
var htmlElementNames = map[string]bool{
	"a": true, "abbr": true, "address": true, "area": true, "article": true,
	"aside": true, "audio": true, "b": true, "base": true, "bdi": true,
	"bdo": true, "blockquote": true, "body": true, "br": true, "button": true,
	"canvas": true, "caption": true, "cite": true, "code": true, "col": true,
	"colgroup": true, "data": true, "datalist": true, "dd": true, "del": true,
	"details": true, "dfn": true, "dialog": true, "div": true, "dl": true,
	"dt": true, "em": true, "embed": true, "fieldset": true, "figcaption": true,
	"figure": true, "footer": true, "form": true, "h1": true, "h2": true,
	"h3": true, "h4": true, "h5": true, "h6": true, "head": true,
	"header": true, "hgroup": true, "hr": true, "html": true, "i": true,
	"iframe": true, "img": true, "input": true, "ins": true, "kbd": true,
	"label": true, "legend": true, "li": true, "link": true, "main": true,
	"map": true, "mark": true, "math": true, "menu": true, "meta": true,
	"meter": true, "nav": true, "noscript": true, "object": true, "ol": true,
	"optgroup": true, "option": true, "output": true, "p": true,
	"picture": true, "pre": true, "progress": true, "q": true, "rp": true,
	"rt": true, "ruby": true, "s": true, "samp": true, "script": true,
	"search": true, "section": true, "select": true,
	"selectedcontent": true, "slot": true, "small": true, "source": true,
	"span": true, "strong": true, "style": true,
	"sub": true, "summary": true, "sup": true, "svg": true, "table": true,
	"tbody": true, "td": true, "template": true, "textarea": true,
	"tfoot": true, "th": true, "thead": true, "time": true, "title": true,
	"tr": true, "track": true, "u": true, "ul": true, "var": true,
	"video": true, "wbr": true,
}

// voidElementNames is the WHATWG void-element set
// (https://html.spec.whatwg.org/multipage/syntax.html#void-elements): elements
// that have no end tag. Unlike htmlElementNames above (diagnostic-only), this
// table IS consulted by emit: it drives canonical tag serialization
// (emitOpenTagEnd) and the void-children diagnostic. Keys are lowercase, but
// lookups fold case (strings.ToLower(el.Tag)): HTML tag names are
// case-insensitive, so a mixed-case HTML tag like <bR> — still an HTML
// element in gsx, since only an uppercase first letter makes a component
// tag — must classify the same as <br>.
var voidElementNames = map[string]bool{
	"area": true, "base": true, "br": true, "col": true, "embed": true,
	"hr": true, "img": true, "input": true, "link": true, "meta": true,
	"param": true, "source": true, "track": true, "wbr": true,
}
