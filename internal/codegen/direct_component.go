package codegen

import (
	"fmt"
	goast "go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	gsxast "github.com/gsxhq/gsx/ast"
	"github.com/gsxhq/gsx/internal/diag"
	"github.com/gsxhq/gsx/internal/sourceview"
)

// directComponentFamily is the package-wide identity of a generated direct
// renderer. Every mutually exclusive declaration variant in one semantic
// component family shares this identity.
type directComponentFamily struct {
	logicalKey string
	helperName string
}

// directComponentDeclaration contains only facts owned by one authoritative
// GSX component declaration. In particular, forwarding names are never copied
// from another build variant in the same family.
type directComponentDeclaration struct {
	family         directComponentFamily
	typeParamNames []string
	paramNames     []string
	variadic       bool
}

// directComponentDeclarationFor decides whether a declaration can forward its
// public factory arguments exactly to a generated body helper. Target locality
// and GSX provenance are semantic call-site facts and are checked separately by
// directComponentTarget.
func directComponentDeclarationFor(component *gsxast.Component) (directComponentDeclaration, bool, error) {
	if component == nil {
		return directComponentDeclaration{}, false, fmt.Errorf("codegen: nil direct component declaration")
	}
	if component.Recv != "" {
		return directComponentDeclaration{}, false, nil
	}

	typeParams, _, err := parseTypeParamFieldList(component.TypeParams)
	if err != nil {
		return directComponentDeclaration{}, false, err
	}
	declaration := directComponentDeclaration{}
	if typeParams != nil {
		for _, field := range typeParams.List {
			if len(field.Names) == 0 {
				return directComponentDeclaration{}, false, nil
			}
			for _, name := range field.Names {
				if name == nil || name.Name == "" || name.Name == "_" || name.Name == "ctx" || strings.HasPrefix(name.Name, reservedPrefix) {
					return directComponentDeclaration{}, false, nil
				}
				declaration.typeParamNames = append(declaration.typeParamNames, name.Name)
			}
		}
	}

	params, err := parseComponentParamDecls(component.Params)
	if err != nil {
		return directComponentDeclaration{}, false, err
	}
	for _, parameter := range params {
		if parameter.name == "" || parameter.name == "_" {
			return directComponentDeclaration{}, false, nil
		}
		declaration.paramNames = append(declaration.paramNames, parameter.name)
	}
	if len(params) != 0 {
		declaration.variadic = params[len(params)-1].variadic
	}
	return declaration, true, nil
}

func collectPackageLevelGoNames(names map[string]bool, file *goast.File) {
	if file == nil {
		return
	}
	for _, declaration := range file.Decls {
		switch declaration := declaration.(type) {
		case *goast.FuncDecl:
			if declaration.Recv == nil && declaration.Name != nil {
				names[declaration.Name.Name] = true
			}
		case *goast.GenDecl:
			for _, specification := range declaration.Specs {
				switch specification := specification.(type) {
				case *goast.TypeSpec:
					names[specification.Name.Name] = true
				case *goast.ValueSpec:
					for _, name := range specification.Names {
						names[name.Name] = true
					}
				}
			}
		}
	}
}

// directHelperOccupiedNamesFromView collects the complete package declaration
// surface relevant to generated helper names from one immutable source view.
// Unlike packageDeclNames it includes every Go build variant, same-package
// test, and orphaned .x.go. Only exact outputs owned by active GSX inputs are
// excluded.
func directHelperOccupiedNamesFromView(packageName string, files map[string]*gsxast.File, goFiles map[string]sourceview.FileSnapshot) (map[string]bool, error) {
	names := packageDeclNamesFromFiles(nil, files)
	owned := pairedTargetOutputs(files)
	paths := make([]string, 0, len(goFiles))
	for path := range goFiles {
		paths = append(paths, filepath.Clean(path))
	}
	sort.Strings(paths)
	fset := token.NewFileSet()
	for _, path := range paths {
		if owned[path] {
			continue
		}
		snapshot := goFiles[path]
		source, present := snapshot.Source()
		if !present {
			if snapshot.State() == sourceview.FileUnreadable {
				return nil, fmt.Errorf("codegen: read direct helper declarations in %s: %w", path, snapshot.Err())
			}
			continue
		}
		file, parseErr := parser.ParseFile(fset, path, source, 0)
		if parseErr != nil {
			return nil, fmt.Errorf("codegen: parse direct helper declarations in %s: %w", path, parseErr)
		}
		if file.Name == nil || file.Name.Name != packageName {
			continue
		}
		collectPackageLevelGoNames(names, file)
	}
	return names, nil
}

func (m *Module) directHelperGoSourceView(dir string) (map[string]sourceview.FileSnapshot, error) {
	if m.opts.SourceOnly {
		return map[string]sourceview.FileSnapshot{}, nil
	}
	if m.opts.Bundle == nil {
		manifest, err := m.buildSourceInventoryManifest()
		if err != nil {
			return nil, err
		}
		return manifest.HelperGoFiles(dir), nil
	}
	m.mu.Lock()
	overrides := make(map[string][]byte)
	savedFiles := make(map[string]sourceview.FileSnapshot)
	for path, source := range m.overrides {
		if filepath.Dir(path) == dir && strings.HasSuffix(path, ".go") {
			overrides[path] = append([]byte(nil), source...)
		}
	}
	for path, snapshot := range m.savedFileSnapshots {
		if filepath.Dir(path) == dir && strings.HasSuffix(path, ".go") {
			savedFiles[path] = cloneSourceFileSnapshot(snapshot)
		}
	}
	m.mu.Unlock()

	paths := make(map[string]bool)
	entries, err := os.ReadDir(dir)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".go") {
			paths[filepath.Join(dir, entry.Name())] = true
		}
	}
	for path := range overrides {
		paths[path] = true
	}
	for path := range savedFiles {
		paths[path] = true
	}
	view := make(map[string]sourceview.FileSnapshot, len(paths))
	for path := range paths {
		override, overridden := overrides[path]
		saved, savedKnown := savedFiles[path]
		switch {
		case overridden:
			view[path] = sourceview.PresentFile(override)
		case savedKnown:
			view[path] = cloneSourceFileSnapshot(saved)
		default:
			view[path] = sourceview.ReadFileSnapshot(path)
		}
	}
	return view, nil
}

// assignDirectComponentDeclarations attaches forwarding metadata only after
// component family membership has been finalized. Families are allocated in
// logical-key order, making helper names independent of map and filesystem
// iteration order.
func assignDirectComponentDeclarationsFromView(packageName string, files map[string]*gsxast.File, plan componentTargetPlan, lexicalNames map[string]bool, goFiles map[string]sourceview.FileSnapshot) (componentTargetPlan, error) {
	occupied, err := directHelperOccupiedNamesFromView(packageName, files, goFiles)
	if err != nil {
		return componentTargetPlan{}, err
	}
	for name := range lexicalNames {
		occupied[name] = true
	}
	type member struct {
		component   *gsxast.Component
		declaration directComponentDeclaration
	}
	byKey := make(map[string][]member)
	rejected := make(map[string]bool)
	for _, path := range sortedGsxFilePaths(files) {
		for _, authored := range files[path].Decls {
			component, ok := authored.(*gsxast.Component)
			if !ok {
				continue
			}
			key := plan.logicalKey(component)
			declaration, eligible, declarationErr := directComponentDeclarationFor(component)
			if declarationErr != nil {
				// Signature parsing and its positioned diagnostic remain owned by the
				// existing skeleton/emission path. Direct rendering is an optional
				// lowering and must fail closed without changing that precedence.
				rejected[key] = true
				continue
			}
			if !eligible {
				rejected[key] = true
				continue
			}
			byKey[key] = append(byKey[key], member{component: component, declaration: declaration})
		}
	}
	keys := make([]string, 0, len(byKey))
	for key := range byKey {
		if !rejected[key] {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	for _, key := range keys {
		members := byKey[key]
		if len(members) == 0 {
			continue
		}
		base := "_gsxrender" + members[0].component.Name
		helper := base
		for suffix := 1; occupied[helper]; suffix++ {
			helper = base + strconv.Itoa(suffix)
		}
		occupied[helper] = true
		family := directComponentFamily{logicalKey: key, helperName: helper}
		for _, member := range members {
			emission, ok := plan.emission(member.component)
			if !ok {
				return componentTargetPlan{}, fmt.Errorf("codegen: direct component %s is absent from the finalized plan", member.component.Name)
			}
			declaration := member.declaration
			declaration.family = family
			emission.direct = &declaration
			plan.emissions[member.component] = emission
		}
	}
	return plan, nil
}

func (m *Module) assignDirectComponentDeclarations(
	dir, packageName string,
	files map[string]*gsxast.File,
	plan componentTargetPlan,
	fset *token.FileSet,
	importer types.Importer,
) (componentTargetPlan, error) {
	// Build the same exact component-signature files used by semantic analysis,
	// then reserve every identifier and implicit default-import binding visible
	// in those files. A direct helper is referenced from generated component
	// bodies, so package-level uniqueness alone is insufficient: a caller type
	// parameter, generic receiver binding, or file import can shadow it.
	// This is a speculative optimization prepass, not the package's diagnostic
	// authority. Build against an isolated bag so a declaration that ordinary
	// generation can recover from does not suppress healthy sibling output or
	// change diagnostic precedence. Discard every speculative diagnostic: the
	// ordinary path owns all severities. This function deliberately receives no
	// authoritative diagnostic bag, making speculative publication impossible.
	prepassBag := diag.NewBag(fset)
	signatureFiles, importPaths, err := buildComponentSignatureFiles(dir, files, plan, fset, prepassBag)
	if err != nil || prepassBag.HasErrors() {
		// Direct rendering is optional. A package on the unique-name fast path may
		// contain a declaration ordinary generation diagnoses and recovers from. If
		// its exact signature files cannot be constructed without an error, keep
		// every call on the established Node path and leave diagnostic ownership to
		// that ordinary path.
		return plan, nil
	}
	lexicalNames, err := m.componentAnalysisOccupiedNames(signatureFiles, importer)
	if err != nil {
		// Name resolution is part of the same optional prepass. If exact lexical
		// names cannot be constructed, fail closed to ordinary Node rendering.
		return plan, nil
	}
	// The imported package identity participates in the same exact-target graph
	// as signature checking, so changes to an implicit default-import name
	// invalidate both warm semantic state and the persistent source projection.
	m.recordTargetImports(dir, importPaths)
	goFiles, err := m.directHelperGoSourceView(dir)
	if err != nil {
		return componentTargetPlan{}, err
	}
	return assignDirectComponentDeclarationsFromView(packageName, files, plan, lexicalNames, goFiles)
}

// directComponentTarget admits only a same-package package function whose
// authoritative declaration provenance identifies it as a forwardable GSX
// component. Imported functions, methods, variables, dynamic values, and plain
// Go functions therefore cannot enter the direct branch.
func directComponentTarget(fact componentTargetFact, analysisPackage *types.Package) *directComponentFamily {
	if analysisPackage == nil || fact.provenance != targetPackageFunc || fact.declaration.direct == nil {
		return nil
	}
	identity := fact.origin
	if identity == nil {
		identity = fact.object
	}
	if identity == nil || identity.Pkg() == nil || identity.Pkg().Path() != analysisPackage.Path() {
		return nil
	}
	family := *fact.declaration.direct
	return &family
}
