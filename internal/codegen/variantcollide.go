package codegen

import (
	"go/types"
	"sort"
	"strings"

	gsxast "github.com/gsxhq/gsx/ast"
)

// componentSignature returns a canonical string of a component's CALLER
// signature — what a `<Comp .../>` invocation type-checks against. Two
// components with the same componentKey that share this signature are drop-in
// build-tag variants (same name, different body); one with a different
// signature is a genuine conflict. The string is comparison-only (not parsed);
// it is order-independent over props (attrs map to fields by name) and ignores
// the receiver VARIABLE name.
func componentSignature(c *gsxast.Component) string {
	var b strings.Builder

	// Receiver: type (parseRecv's recvType already carries the pointer-ness,
	// e.g. "*Form" vs "Form" — a method vs func component, and the owning
	// type, are caller-visible; the receiver var name is not).
	if c.Recv != "" {
		if _, recvType, _, err := parseRecv(c.Recv); err == nil {
			b.WriteString("recv:")
			b.WriteString(recvType)
		} else {
			b.WriteString("recv:<unparsable>")
		}
	}
	b.WriteByte('|')

	// Generic type params: normalized source.
	b.WriteString("tp:")
	b.WriteString(strings.Join(strings.Fields(c.TypeParams), " "))
	b.WriteByte('|')

	// Props: sorted "FieldName type" entries, plus synthesized Children/Attrs.
	var fields []string
	if params, err := parseParams(c.Params); err == nil {
		for _, p := range params {
			fields = append(fields, fieldName(p.name)+" "+strings.Join(strings.Fields(p.typ), " "))
		}
	}
	if usesChildren(c.Body) {
		fields = append(fields, "Children gsx.Node")
	}
	if usesAttrs(c.Body) {
		fields = append(fields, "Attrs gsx.Attrs")
	}
	sort.Strings(fields)
	b.WriteString("props:")
	b.WriteString(strings.Join(fields, ";"))
	return b.String()
}

type conflictComp struct {
	path string
	comp *gsxast.Component
}

type signatureConflict struct {
	key   string
	comps []conflictComp
}

// detectSignatureConflicts finds components that share a componentKey across
// DIFFERENT files but do not share a signature — a genuine ambiguity gsx
// cannot paper over. A key whose cross-file decls all share one signature is a
// tolerated build-tag variant (no conflict); a key declared twice in a single
// file is a within-file redeclaration left to the raw go/types error.
func detectSignatureConflicts(files map[string]*gsxast.File) []signatureConflict {
	type decl struct {
		path string
		comp *gsxast.Component
		sig  string
	}
	byKey := map[string][]decl{}
	// Iterate files in sorted path order for determinism.
	paths := make([]string, 0, len(files))
	for p := range files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		for _, d := range files[p].Decls {
			c, ok := d.(*gsxast.Component)
			if !ok {
				continue
			}
			key := componentKey(c)
			byKey[key] = append(byKey[key], decl{p, c, componentSignature(c)})
		}
	}

	var out []signatureConflict
	keys := make([]string, 0, len(byKey))
	for k := range byKey {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, key := range keys {
		decls := byKey[key]
		// Distinct files that declare this key.
		fileSet := map[string]bool{}
		sigSet := map[string]bool{}
		for _, d := range decls {
			fileSet[d.path] = true
			sigSet[d.sig] = true
		}
		if len(fileSet) < 2 || len(sigSet) < 2 {
			continue // single-file (within-file) or all one signature (tolerated)
		}
		comps := make([]conflictComp, 0, len(decls))
		for _, d := range decls {
			comps = append(comps, conflictComp{d.path, d.comp})
		}
		out = append(out, signatureConflict{key: key, comps: comps})
	}
	return out
}

// redeclName extracts the declared name from a go/types redeclaration-class
// error message, or ("", false) if the error is not redeclaration-class. It
// recognizes both records go/types emits: "<name> redeclared in this block"
// (at the second decl) and "other declaration of <name>" (at the first).
func redeclName(msg string) (string, bool) {
	msg = strings.TrimSpace(msg)
	if i := strings.Index(msg, " redeclared"); i > 0 {
		return msg[:i], true
	}
	const p = "other declaration of "
	if strings.HasPrefix(msg, p) {
		return strings.TrimSpace(msg[len(p):]), true
	}
	return "", false
}

// suppressCrossFileRedeclarations drops redeclaration-class errors whose
// declared name is redeclared ACROSS files (a tolerated cross-tag variant of a
// component or helper). A same-file redeclaration keeps its error (a real
// within-file mistake), as do all non-redeclaration errors. gsx does not parse
// build tags; go build remains the arbiter of whether a cross-file same-name
// pair is an actual same-configuration duplicate.
func suppressCrossFileRedeclarations(errs []types.Error) []types.Error {
	// filesByName[name] = set of distinct filenames where a redeclaration-class
	// error for that name was reported.
	filesByName := map[string]map[string]bool{}
	for _, e := range errs {
		if name, ok := redeclName(e.Msg); ok {
			fn := e.Fset.Position(e.Pos).Filename
			if filesByName[name] == nil {
				filesByName[name] = map[string]bool{}
			}
			filesByName[name][fn] = true
		}
	}
	kept := errs[:0]
	for _, e := range errs {
		if name, ok := redeclName(e.Msg); ok && len(filesByName[name]) >= 2 {
			continue // cross-file variant: tolerate
		}
		kept = append(kept, e)
	}
	return kept
}
