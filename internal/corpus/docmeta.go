package corpus

import (
	"strconv"
	"strings"
)

// docMeta is the parsed `-- doc --` block of an example fixture: the human
// metadata that drives the docs page and the preset list.
type docMeta struct {
	Name     string
	Summary  string
	Category string
	Order    int
}

// parseDocMeta parses a `-- doc --` body of `key: value` lines. Unknown keys
// are ignored; a missing or unparseable `order` is 0.
func parseDocMeta(b []byte) docMeta {
	var m docMeta
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		key, val, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		val = strings.TrimSpace(val)
		switch key {
		case "name":
			m.Name = val
		case "summary":
			m.Summary = val
		case "category":
			m.Category = val
		case "order":
			if n, err := strconv.Atoi(val); err == nil {
				m.Order = n
			}
		}
	}
	return m
}
