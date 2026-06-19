package gsx

import (
	"io"
	"strings"
)

// htmlReplacer escapes the bytes unsafe in HTML text and double-quoted attribute
// contexts. The entity set matches html.EscapeString.
var htmlReplacer = strings.NewReplacer(
	"&", "&amp;",
	"<", "&lt;",
	">", "&gt;",
	`"`, "&#34;",
	"'", "&#39;",
)

// writeHTML streams s to w with HTML text/attribute escaping. strings.Replacer
// writes safe runs directly, so this allocates only for the (rare) entity spans.
func writeHTML(w io.Writer, s string) error {
	_, err := htmlReplacer.WriteString(w, s)
	return err
}

// blockedURL replaces a URL whose scheme is not allow-listed (mirrors
// html/template's #ZgotmplZ sentinel for an unsafe URL).
const blockedURL = "about:invalid#gsx"

// urlSanitize returns s unchanged when it is relative/fragment/query or carries
// an allow-listed scheme (http, https, mailto, tel — case-insensitive); any
// other scheme (javascript:, vbscript:, data:, …) yields blockedURL.
func urlSanitize(s string) string {
	if i := strings.IndexByte(s, ':'); i >= 0 {
		// A scheme exists only when no '/', '?', or '#' precedes the ':'.
		if !strings.ContainsAny(s[:i], "/?#") {
			switch strings.ToLower(s[:i]) {
			case "http", "https", "mailto", "tel":
				// allowed
			default:
				return blockedURL
			}
		}
	}
	return s
}

// writeURL streams a sanitized, attribute-escaped URL value to w.
func writeURL(w io.Writer, s string) error {
	return writeHTML(w, urlSanitize(s))
}
