package corpus

import "bytes"

// rewriteImportPath rewrites quoted import strings equal to oldPath or under
// oldPath+"/" so their prefix becomes newPath. Only quoted occurrences are
// touched: the match must be preceded by a double quote and followed by either
// a double quote (exact) or a slash (subpackage). This avoids rewriting a
// longer sibling path like "example.com/application".
func rewriteImportPath(src []byte, oldPath, newPath string) []byte {
	if oldPath == "" {
		return src
	}
	var out bytes.Buffer
	needle := []byte(`"` + oldPath)
	for {
		i := bytes.Index(src, needle)
		if i < 0 {
			out.Write(src)
			break
		}
		end := i + len(needle)
		// Next byte after the matched path must be `"` or `/` to be a real
		// path boundary (not a longer sibling).
		if end < len(src) && (src[end] == '"' || src[end] == '/') {
			out.Write(src[:i])
			out.WriteByte('"')
			out.WriteString(newPath)
			src = src[end:]
		} else {
			out.Write(src[:end])
			src = src[end:]
		}
	}
	return out.Bytes()
}
