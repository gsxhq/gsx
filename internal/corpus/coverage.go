package corpus

import (
	"bytes"
	"fmt"
	"sort"
	"strings"
)

func coverageReport(cases []*caseDoc) []byte {
	sorted := append([]*caseDoc(nil), cases...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].name < sorted[j].name })
	var buf bytes.Buffer
	var render, errc, gen int
	for _, c := range sorted {
		f := c.facets()
		fmt.Fprintf(&buf, "%s\t%s\n", c.name, strings.Join(f, " "))
		for _, tag := range f {
			switch tag {
			case "render":
				render++
			case "diag(error)":
				errc++
			case "gen":
				gen++
			}
		}
	}
	fmt.Fprintf(&buf, "TOTAL: %d cases (render: %d, error: %d, gen-pinned: %d)\n", len(sorted), render, errc, gen)
	return buf.Bytes()
}
