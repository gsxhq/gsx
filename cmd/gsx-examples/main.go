// Command gsx-examples regenerates the docs Examples page and the playground
// preset lists from the single-source examples/*.txtar fixtures. Run from the
// repo root (e.g. `make examples`). Generated files are committed.
package main

import (
	"flag"
	"log"

	"github.com/gsxhq/gsx/internal/examplegen"
)

func main() {
	examplesDir := flag.String("examples", "examples", "directory of *.txtar fixtures")
	mdOut := flag.String("md", "docs/guide/examples.md", "docs Markdown output path")
	docsJSON := flag.String("docs-json", "docs/examples.json", "frontend preset JSON output path")
	serverJSON := flag.String("server-json", "playground/server/examples.json", "backend preset JSON output path")
	partials := flag.String("partials", "docs/guide/syntax/_generated", "routed-example partials output dir")
	flag.Parse()

	if err := examplegen.Generate(*examplesDir, *mdOut, *partials, *docsJSON, *serverJSON); err != nil {
		log.Fatalf("gsx-examples: %v", err)
	}
	log.Printf("generated %s, %s", *docsJSON, *serverJSON)
}
