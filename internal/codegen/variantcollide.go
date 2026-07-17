package codegen

import (
	"bytes"
	"errors"
	"fmt"
	goast "go/ast"
	"go/build"
	"go/build/constraint"
	"go/token"
	"go/types"
	"io"
	"path/filepath"
	"strings"

	gsxast "github.com/gsxhq/gsx/ast"
	"github.com/gsxhq/gsx/internal/diag"
)

type componentVariantMember struct {
	path      string
	component *gsxast.Component
}

type componentVariantFamily struct {
	key     string
	members []componentVariantMember
}

// syntacticComponentTargetPlan is the importer-free lane's deliberately
// non-semantic emission plan. It makes every parsed component public and never
// recognizes, validates, or folds variant families. Its only consumer builds
// per-file parse/probe skeletons for syntactic editor features; the finalized
// semantic plan remains the sole authority for package acceptance and codegen.
func syntacticComponentTargetPlan(files map[string]*gsxast.File) componentTargetPlan {
	plan := componentTargetPlan{
		emissions:   map[*gsxast.Component]componentTargetEmission{},
		logicalKeys: map[*gsxast.Component]string{},
	}
	for _, file := range files {
		for _, declaration := range file.Decls {
			component, ok := declaration.(*gsxast.Component)
			if !ok {
				continue
			}
			plan.emissions[component] = componentTargetEmission{public: true}
			plan.logicalKeys[component] = componentKey(component)
		}
	}
	return plan
}

func reportInvalidComponentVariantFamily(key string, members []componentVariantMember, files map[string]*gsxast.File, sources map[string][]byte, bag *diag.Bag) {
	if bag == nil {
		return
	}
	filenames := make([]string, 0, len(members))
	for _, member := range members {
		filenames = append(filenames, filepath.Base(member.path))
	}
	name := strings.TrimPrefix(key, ".")
	for _, member := range members {
		constrained, err := componentFileHasEffectiveConstraint(member.path, files[member.path], sources[member.path])
		detail := "every member must be in a distinct file with a valid Go build constraint"
		if err != nil {
			detail = err.Error()
		} else if !constrained {
			detail = filepath.Base(member.path) + " has no effective Go build constraint"
		}
		bag.Errorf(member.component.NamePos, member.component.NamePos+token.Pos(len(member.component.Name)), "duplicate-component",
			"component %s cannot form a build variant family across %s: %s", name, strings.Join(filenames, ", "), detail)
	}
}

func componentFileHasEffectiveConstraint(path string, file *gsxast.File, source []byte) (bool, error) {
	if file == nil {
		return false, fmt.Errorf("missing parsed source for %s", path)
	}
	if len(source) == 0 {
		source = []byte(file.Doc + "\n\npackage " + file.Package + "\n")
	}
	sourceConstrained, err := sourceHasEffectiveBuildConstraint(source)
	if err != nil {
		return false, fmt.Errorf("invalid build constraint in %s: %w", filepath.Base(path), err)
	}
	if sourceConstrained {
		return true, nil
	}
	return generatedFilenameHasBuildConstraint(path)
}

var errMultipleGoBuildConstraints = errors.New("multiple //go:build comments")

// sourceHasEffectiveBuildConstraint follows go/build's leading-header rules.
// The parser's File.Doc is the exact byte prefix before package, so appending a
// package clause reconstructs the boundary on which the Go command operates.
func sourceHasEffectiveBuildConstraint(source []byte) (bool, error) {
	trimmed, goBuild, err := parseBuildConstraintHeader(source)
	if err != nil {
		return false, err
	}
	if goBuild != nil {
		_, err := constraint.Parse(string(goBuild))
		return err == nil, err
	}
	for len(trimmed) > 0 {
		line := trimmed
		if index := bytes.IndexByte(line, '\n'); index >= 0 {
			line, trimmed = line[:index], trimmed[index+1:]
		} else {
			trimmed = nil
		}
		text := string(bytes.TrimSpace(line))
		if !constraint.IsPlusBuild(text) {
			continue
		}
		if _, err := constraint.Parse(text); err == nil {
			return true, nil
		}
	}
	return false, nil
}

// parseBuildConstraintHeader is a focused port of go/build.parseFileHeader.
// It deliberately retains the standard library's blank-line and comment-block
// rules instead of approximating directive placement.
func parseBuildConstraintHeader(content []byte) (trimmed, goBuild []byte, err error) {
	end := 0
	pending := content
	ended := false
	inSlashStar := false

lines:
	for len(pending) > 0 {
		line := pending
		if index := bytes.IndexByte(line, '\n'); index >= 0 {
			line, pending = line[:index], pending[index+1:]
		} else {
			pending = nil
		}
		line = bytes.TrimSpace(line)
		if len(line) == 0 && !ended {
			end = len(content) - len(pending)
			continue lines
		}
		if !bytes.HasPrefix(line, []byte("//")) {
			ended = true
		}
		if !inSlashStar && constraint.IsGoBuild(string(line)) {
			if goBuild != nil {
				return nil, nil, errMultipleGoBuildConstraints
			}
			goBuild = append([]byte(nil), line...)
		}

	comments:
		for len(line) > 0 {
			if inSlashStar {
				if index := bytes.Index(line, []byte("*/")); index >= 0 {
					inSlashStar = false
					line = bytes.TrimSpace(line[index+2:])
					continue comments
				}
				continue lines
			}
			if bytes.HasPrefix(line, []byte("//")) {
				continue lines
			}
			if bytes.HasPrefix(line, []byte("/*")) {
				inSlashStar = true
				line = bytes.TrimSpace(line[2:])
				continue comments
			}
			break lines
		}
	}
	return content[:end], goBuild, nil
}

func generatedFilenameHasBuildConstraint(path string) (bool, error) {
	name := strings.TrimSuffix(filepath.Base(path), ".gsx") + ".x.go"
	stem, _, _ := strings.Cut(name, ".")
	_, suffix, found := strings.Cut(stem, "_")
	if !found {
		return false, nil
	}
	parts := strings.Split(suffix, "_")
	if len(parts) > 0 && parts[len(parts)-1] == "test" {
		parts = parts[:len(parts)-1]
	}
	if len(parts) == 0 {
		return false, nil
	}
	const invalid = "gsx_invalid_platform"
	neutral, err := generatedFilenameMatches(name, invalid, invalid)
	if err != nil || neutral {
		return false, err
	}
	last := parts[len(parts)-1]
	if matches, err := generatedFilenameMatches(name, last, invalid); err != nil || matches {
		return matches, err
	}
	if matches, err := generatedFilenameMatches(name, invalid, last); err != nil || matches {
		return matches, err
	}
	if len(parts) >= 2 {
		return generatedFilenameMatches(name, parts[len(parts)-2], last)
	}
	return false, nil
}

func generatedFilenameMatches(name, goos, goarch string) (bool, error) {
	context := build.Default
	context.GOOS = goos
	context.GOARCH = goarch
	context.OpenFile = func(string) (io.ReadCloser, error) {
		return io.NopCloser(strings.NewReader("package p\n")), nil
	}
	return context.MatchFile(".", name)
}

type variantParamIdentity struct {
	name string
	role declarationParamRole
}

func componentVariantParamIdentity(component *gsxast.Component) ([]variantParamIdentity, error) {
	declaration, err := componentDeclarationFor(component)
	if err != nil {
		return nil, err
	}
	identity := make([]variantParamIdentity, 0, len(declaration.params))
	for _, parameter := range declaration.params {
		identity = append(identity, variantParamIdentity{name: parameter.name, role: parameter.role})
	}
	return identity, nil
}

func variantFuncObjects(files []*goast.File, info *types.Info, plan componentTargetPlan) map[*gsxast.Component]*types.Func {
	byName := make(map[string]*gsxast.Component)
	for component, emission := range plan.emissions {
		if emission.splitBody && emission.bodyName != "" {
			byName[emission.bodyName] = component
		}
	}
	objects := make(map[*gsxast.Component]*types.Func, len(byName))
	for _, file := range files {
		for _, declaration := range file.Decls {
			function, ok := declaration.(*goast.FuncDecl)
			if !ok {
				continue
			}
			component := byName[function.Name.Name]
			if component == nil {
				continue
			}
			if object, ok := info.Defs[function.Name].(*types.Func); ok {
				objects[component] = object
			}
		}
	}
	return objects
}

func componentVariantFamilySignaturesMatch(
	members []componentVariantMember,
	objects map[*gsxast.Component]*types.Func,
	signatureErrors map[*gsxast.Component]bool,
) bool {
	var firstSignature *types.Signature
	var firstParams []variantParamIdentity
	for index, member := range members {
		if signatureErrors[member.component] {
			return false
		}
		object := objects[member.component]
		if object == nil {
			return false
		}
		signature, ok := object.Type().(*types.Signature)
		if !ok || !componentVariantSignatureUsable(signature) {
			return false
		}
		params, err := componentVariantParamIdentity(member.component)
		if err != nil {
			return false
		}
		if index == 0 {
			firstSignature = signature
			firstParams = params
			continue
		}
		if !equalVariantParamIdentity(firstParams, params) || !identicalComponentVariantSignatures(firstSignature, signature) {
			return false
		}
	}
	return len(members) > 0
}

func reportComponentVariantSignatureMismatch(members []componentVariantMember, bag *diag.Bag) {
	if bag == nil {
		return
	}
	filenames := make([]string, 0, len(members))
	for _, member := range members {
		filenames = append(filenames, filepath.Base(member.path))
	}
	for _, member := range members {
		bag.Errorf(member.component.NamePos, member.component.NamePos+token.Pos(len(member.component.Name)), "duplicate-component",
			"component %s has different or unresolved semantic signatures across build variants (%s); parameter names and roles, function types, constraints, and receiver types must be valid and match",
			member.component.Name, strings.Join(filenames, ", "))
	}
}

func equalVariantParamIdentity(left, right []variantParamIdentity) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

// --- raw-Go cross-file build-variant redeclaration tolerance ---
//
// The variant-family machinery above governs same-name COMPONENT declarations.
// Raw Go declarations in a file's verbatim region (const/var/type/func) are not
// *gsxast.Component nodes, so they never reach it; a same-name pair across two
// build-tagged files therefore surfaces as a native go/types "redeclared in
// this block" error. gsx tolerates such a pair only when it can PROVE the two
// files never build together AND the declarations are signature-identical —
// the raw-Go analogue of the component path's "distinct build constraint +
// matching signature" rule. Everything else (non-disjoint tags, differing
// signatures, within-file duplicates) keeps the error; go build remains the
// arbiter of which tagged file is active.

// redeclName extracts the declared name from a go/types redeclaration-class
// diagnostic. go/types (observed on Go 1.26.1) emits two message families:
// funcs/vars/consts/types produce a "<name> redeclared in this block" record
// plus an "other declaration of <name>" note; methods produce a single
// "method <Base>.<Method> already declared at …" record. <Base> is the receiver
// base type with no pointer or generic arg list.
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
			return msg[len(method):j], true
		}
	}
	return "", false
}

// recvBaseName returns a method receiver's base type identifier, stripping a
// leading pointer and any generic argument list so it matches the "<Base>"
// go/types prints in a method-redeclaration message: `*Form` → "Form",
// `Form[T]` → "Form".
func recvBaseName(expr goast.Expr) string {
	switch t := expr.(type) {
	case *goast.StarExpr:
		return recvBaseName(t.X)
	case *goast.IndexExpr:
		return recvBaseName(t.X)
	case *goast.IndexListExpr:
		return recvBaseName(t.X)
	case *goast.Ident:
		return t.Name
	}
	return ""
}

// variantDeclSite is one top-level declaration occurrence of a redeclared name.
type variantDeclSite struct {
	file string // filepath.Base of the declaring source file
	sig  string // syntactic signature/type key (identical across true variants)
}

// collectVariantDeclSites records one variantDeclSite per top-level name each
// skeleton file declares, keyed by the redeclName form (methods as
// "<Base>.<Method>"). Facts come from the skeleton ASTs, not go/types error
// positions: go/types anchors every redeclaration against the single
// globally-first decl, and files are fed to the checker in map order, so
// per-error file attribution is unreliable — the AST sees every declaration.
func collectVariantDeclSites(goFiles []*goast.File, fset *token.FileSet) map[string][]variantDeclSite {
	sites := map[string][]variantDeclSite{}
	add := func(name, sig string, pos token.Pos) {
		if name == "" {
			return
		}
		sites[name] = append(sites[name], variantDeclSite{
			file: filepath.Base(fset.Position(pos).Filename),
			sig:  sig,
		})
	}
	for _, gf := range goFiles {
		for _, d := range gf.Decls {
			switch decl := d.(type) {
			case *goast.FuncDecl:
				sig := types.ExprString(decl.Type)
				if decl.Recv != nil && len(decl.Recv.List) > 0 {
					if base := recvBaseName(decl.Recv.List[0].Type); base != "" {
						add(base+"."+decl.Name.Name, sig, decl.Pos())
					}
				} else {
					add(decl.Name.Name, sig, decl.Pos())
				}
			case *goast.GenDecl:
				for _, spec := range decl.Specs {
					switch s := spec.(type) {
					case *goast.ValueSpec:
						for i, n := range s.Names {
							if n.Name != "_" {
								add(n.Name, valueSpecSig(s, i), n.Pos())
							}
						}
					case *goast.TypeSpec:
						if s.Name.Name != "_" {
							add(s.Name.Name, "type "+types.ExprString(s.Type), s.Name.Pos())
						}
					}
				}
			}
		}
	}
	return sites
}

// valueSpecSig is a best-effort syntactic type key for a const/var name: its
// explicit type when written, else the syntactic kind of a literal value (two
// string-literal consts read identical; a string vs an int one does not). A
// non-literal untyped value falls back to the printed expression, which is
// conservative — a difference there keeps the redeclaration error.
func valueSpecSig(s *goast.ValueSpec, i int) string {
	if s.Type != nil {
		return types.ExprString(s.Type)
	}
	if i < len(s.Values) {
		if lit, ok := s.Values[i].(*goast.BasicLit); ok {
			return "lit:" + lit.Kind.String()
		}
		return "expr:" + types.ExprString(s.Values[i])
	}
	return ""
}

// collectConstraintTags gathers the tag names referenced by a build expression.
func collectConstraintTags(e constraint.Expr, set map[string]struct{}) {
	switch x := e.(type) {
	case *constraint.TagExpr:
		set[x.Tag] = struct{}{}
	case *constraint.NotExpr:
		collectConstraintTags(x.X, set)
	case *constraint.AndExpr:
		collectConstraintTags(x.X, set)
		collectConstraintTags(x.Y, set)
	case *constraint.OrExpr:
		collectConstraintTags(x.X, set)
		collectConstraintTags(x.Y, set)
	}
}

// constraintsProvablyDisjoint reports whether (a AND b) is unsatisfiable with
// every build tag treated as an independent boolean. This proves mutual
// exclusion for complementary custom tags (X vs !X) and any boolean-unsat pair.
// It deliberately does NOT model GOOS/GOARCH "exactly one is set" semantics (the
// known lists are not exported by the standard library), so e.g. linux vs
// windows is treated as satisfiable and such a pair is reported, not tolerated —
// a conservative bound, never a false tolerate. A nil (unconstrained) file is
// always active, so it is never disjoint from anything.
func constraintsProvablyDisjoint(a, b constraint.Expr) bool {
	if a == nil || b == nil {
		return false
	}
	tags := map[string]struct{}{}
	collectConstraintTags(a, tags)
	collectConstraintTags(b, tags)
	names := make([]string, 0, len(tags))
	for t := range tags {
		names = append(names, t)
	}
	if len(names) > 24 { // 2^24 enumeration ceiling; unrealistic for real constraints
		return false
	}
	for mask := 0; mask < (1 << uint(len(names))); mask++ {
		assign := make(map[string]bool, len(names))
		for i, name := range names {
			assign[name] = mask&(1<<uint(i)) != 0
		}
		ok := func(tag string) bool { return assign[tag] }
		if a.Eval(ok) && b.Eval(ok) {
			return false
		}
	}
	return true
}

// buildConstraintByFile maps each .gsx file's base name to its parsed leading
// //go:build expression (nil when it carries none), for the disjointness check.
func buildConstraintByFile(gsxFiles map[string]*gsxast.File) map[string]constraint.Expr {
	out := make(map[string]constraint.Expr, len(gsxFiles))
	for path, gf := range gsxFiles {
		out[filepath.Base(path)] = fileBuildConstraint(gf.Doc, gf.Package)
	}
	return out
}

// fileBuildConstraint parses a file's leading //go:build expression from its
// pre-package byte prefix (gsxast.File.Doc). Returns nil when the file carries
// no (or an invalid) //go:build line.
func fileBuildConstraint(doc, pkg string) constraint.Expr {
	source := []byte(doc + "\n\npackage " + pkg + "\n")
	_, goBuild, err := parseBuildConstraintHeader(source)
	if err != nil || goBuild == nil {
		return nil
	}
	expr, err := constraint.Parse(string(goBuild))
	if err != nil {
		return nil
	}
	return expr
}

// suppressCrossFileVariantRedeclarations drops redeclaration-class errors for a
// name whose declarations are all in distinct files with pairwise provably-
// disjoint build constraints and identical signatures — a tolerated build-tag
// variant that go build (not gsx) resolves. A within-file duplicate, a
// signature mismatch, or any non-disjoint pair keeps every error for that name.
func suppressCrossFileVariantRedeclarations(errs []types.Error, sites map[string][]variantDeclSite, constraintByFile map[string]constraint.Expr) []types.Error {
	tolerable := func(name string) bool {
		s := sites[name]
		if len(s) < 2 {
			return false
		}
		seen := map[string]bool{}
		for _, site := range s {
			if seen[site.file] {
				return false // within-file duplicate: a genuine mistake
			}
			seen[site.file] = true
			if site.sig != s[0].sig {
				return false // differing signature across variants
			}
		}
		for i := range s {
			for j := i + 1; j < len(s); j++ {
				if !constraintsProvablyDisjoint(constraintByFile[s[i].file], constraintByFile[s[j].file]) {
					return false
				}
			}
		}
		return true
	}
	kept := errs[:0]
	for _, e := range errs {
		if name, ok := redeclName(e.Msg); ok && tolerable(name) {
			continue
		}
		kept = append(kept, e)
	}
	return kept
}
