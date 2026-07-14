package codegen

import (
	goast "go/ast"
	"go/token"
	"go/types"
	"sort"
	"strings"

	gsxast "github.com/gsxhq/gsx/ast"
)

// componentSignature returns the ordered declaration contract of a component.
// Two components with the same componentKey that share this signature are
// drop-in build-tag variants (same declaration, different body); one with a
// different signature is a genuine conflict. Receiver variable names and bodies
// are not part of the contract.
func componentSignature(c *gsxast.Component) string {
	d, err := componentDeclarationFor(c)
	if err == nil {
		return d.canonical()
	}

	// A malformed variant still needs deterministic, collision-safe identity so
	// conflict reporting does not accidentally merge two different declarations.
	var b strings.Builder
	b.WriteString("raw-component-declaration-v1")
	appendCanonicalField(&b, strings.TrimSpace(c.Recv))
	appendCanonicalField(&b, strings.TrimSpace(c.TypeParams))
	appendCanonicalField(&b, strings.TrimSpace(c.Params))
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
// error message, or ("", false) if the error is not redeclaration-class. The
// returned name is the KEY under which redeclFacts groups the same declaration
// — a bare identifier for a func/var/const/type, and a receiver-qualified
// "<BaseType>.<Method>" for a method.
//
// go/types (observed on Go 1.26.1) emits redeclarations in two message
// families, exercised by a throwaway type-checker probe:
//
//   - Funcs, vars, consts, types → a PAIR of records:
//     "<name> redeclared in this block"   (at the 2nd+ decl)
//     "\tother declaration of <name>"     (a note, at the globally-first decl;
//     note the leading tab that TrimSpace strips)
//
//   - Methods → a SINGLE self-contained record:
//     "method <BaseType>.<Method> already declared at <file>:<line>:<col>"
//     (at the 2nd+ decl; the first-decl location is embedded in the text).
//     <BaseType> is the base receiver type with NO pointer '*' and NO generic
//     '[T]' — e.g. both `(f *Form)` and `(f Form[T])` report "Form.Field".
func redeclName(msg string) (string, bool) {
	msg = strings.TrimSpace(msg)
	if i := strings.Index(msg, " redeclared"); i > 0 {
		return msg[:i], true
	}
	const other = "other declaration of "
	if strings.HasPrefix(msg, other) {
		return strings.TrimSpace(msg[len(other):]), true
	}
	const method = "method "
	if strings.HasPrefix(msg, method) {
		if j := strings.Index(msg, " already declared"); j > len(method) {
			return msg[len(method):j], true // "<BaseType>.<Method>"
		}
	}
	return "", false
}

// redeclFacts records, per declaration key (see redeclName), whether that name
// is declared across ≥2 skeleton files (a candidate build-tag variant) and/or
// ≥2 times within a single file (a genuine within-file redeclaration). These
// facts are derived from the parsed skeleton ASTs, NOT from the go/types error
// positions, on purpose: go/types anchors EVERY redeclaration against the
// single globally-first decl of a name, and gsx feeds skeleton files to the
// checker in nondeterministic (map) order — so a within-file duplicate that
// happens to live in a non-anchor file is reported as if it were cross-file,
// making per-error F1==F2 detection unreliable. The skeleton AST sees every
// declaration regardless of order, so the within-file fact is exact.
type redeclFacts struct {
	crossFile map[string]bool // name declared in ≥2 distinct files
	withinDup map[string]bool // name declared ≥2 times within one file
}

// collectRedeclFacts walks the top-level declarations of every skeleton file
// and tallies, per redeclName key, the set of files it appears in and whether
// any single file declares it more than once.
func collectRedeclFacts(goFiles []*goast.File, fset *token.FileSet) redeclFacts {
	byName := map[string]map[string]int{} // name -> filename -> count in that file
	add := func(name string, pos token.Pos) {
		if name == "" {
			return
		}
		file := fset.Position(pos).Filename
		if byName[name] == nil {
			byName[name] = map[string]int{}
		}
		byName[name][file]++
	}
	for _, gf := range goFiles {
		for _, d := range gf.Decls {
			switch decl := d.(type) {
			case *goast.FuncDecl:
				if decl.Recv != nil && len(decl.Recv.List) > 0 {
					if base := recvBaseName(decl.Recv.List[0].Type); base != "" {
						add(base+"."+decl.Name.Name, decl.Pos())
					}
				} else {
					add(decl.Name.Name, decl.Pos())
				}
			case *goast.GenDecl:
				for _, spec := range decl.Specs {
					switch s := spec.(type) {
					case *goast.ValueSpec: // var / const
						for _, n := range s.Names {
							if n.Name != "_" {
								add(n.Name, n.Pos())
							}
						}
					case *goast.TypeSpec:
						if s.Name.Name != "_" {
							add(s.Name.Name, s.Name.Pos())
						}
					}
				}
			}
		}
	}
	facts := redeclFacts{crossFile: map[string]bool{}, withinDup: map[string]bool{}}
	for name, files := range byName {
		if len(files) >= 2 {
			facts.crossFile[name] = true
		}
		for _, c := range files {
			if c >= 2 {
				facts.withinDup[name] = true
				break
			}
		}
	}
	return facts
}

// recvBaseName returns the base type identifier of a method receiver, stripping
// a leading pointer and any generic type-argument list so it matches the
// "<BaseType>" go/types prints in a method-redeclaration message (see
// redeclName): `*Form` → "Form", `Form[T]` → "Form".
func recvBaseName(expr goast.Expr) string {
	switch t := expr.(type) {
	case *goast.StarExpr:
		return recvBaseName(t.X)
	case *goast.IndexExpr: // Form[T]
		return recvBaseName(t.X)
	case *goast.IndexListExpr: // Form[T, U]
		return recvBaseName(t.X)
	case *goast.Ident:
		return t.Name
	}
	return ""
}

// suppressCrossFileRedeclarations drops redeclaration-class errors for a name
// that is a tolerated cross-tag variant — declared across ≥2 files but never
// twice within a single file. gsx does not parse build tags; go build remains
// the arbiter of whether a cross-file same-name pair is an actual
// same-configuration duplicate.
//
// A name that IS duplicated within some file keeps ALL its redeclaration errors
// (blocking emission), even when a cross-file variant of the same name also
// exists. That within-file duplicate is a real mistake go/types must report;
// disentangling the specific cross-file record from the within-file one per
// error is not possible reliably (go/types' global-first anchoring, above), so
// gsx keeps the whole group — go build would reject the within-file duplicate
// under any tag too. Non-redeclaration errors are always kept.
func suppressCrossFileRedeclarations(errs []types.Error, facts redeclFacts) []types.Error {
	kept := errs[:0]
	for _, e := range errs {
		if name, ok := redeclName(e.Msg); ok && facts.crossFile[name] && !facts.withinDup[name] {
			continue // cross-file build-tag variant: tolerate
		}
		kept = append(kept, e)
	}
	return kept
}
