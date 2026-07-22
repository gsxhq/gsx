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

type helperGoView struct {
	manifest *sourceview.Manifest
	files    map[string]sourceview.FileSnapshot
}

// directComponentDeclarationFor decides whether a declaration can forward its
// public factory arguments exactly to a generated body helper. Target locality
// and GSX provenance are semantic call-site facts and are checked separately by
// directComponentTarget.
func directComponentDeclarationFor(component *gsxast.Component) (directComponentDeclaration, bool, error) {
	if component == nil {
		return directComponentDeclaration{}, false, fmt.Errorf("codegen: nil direct component declaration")
	}
	declaration, err := componentDeclarationFor(component)
	if err != nil {
		return directComponentDeclaration{}, false, err
	}
	return directComponentDeclarationFromParsed(component, declaration)
}

func directComponentDeclarationFromParsed(component *gsxast.Component, parsed componentDeclaration) (directComponentDeclaration, bool, error) {
	if component == nil {
		return directComponentDeclaration{}, false, fmt.Errorf("codegen: nil direct component declaration")
	}
	if component.Recv != "" {
		return directComponentDeclaration{}, false, nil
	}
	declaration := directComponentDeclaration{}
	for _, name := range parsed.typeParamNames {
		if name == "" || name == "_" || name == "ctx" || strings.HasPrefix(name, reservedPrefix) {
			return directComponentDeclaration{}, false, nil
		}
		declaration.typeParamNames = append(declaration.typeParamNames, name)
	}
	for _, parameter := range parsed.params {
		if parameter.name == "" || parameter.name == "_" {
			return directComponentDeclaration{}, false, nil
		}
		declaration.paramNames = append(declaration.paramNames, parameter.name)
	}
	if len(parsed.params) != 0 {
		declaration.variadic = parsed.params[len(parsed.params)-1].variadic
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

// directHelperOccupiedNamesFromView adds the build-oblivious Go declaration
// surface to names collected from the already-built full GSX skeletons. It
// includes inactive variants, same-package tests, and orphaned .x.go files.
// Only exact outputs owned by active GSX inputs are excluded.
func directHelperOccupiedNamesFromView(packageName string, files map[string]*gsxast.File, goFiles map[string]sourceview.FileSnapshot) (map[string]bool, error) {
	names := map[string]bool{}
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
		m.mu.Lock()
		manifest := m.helperGoSourceManifest
		cached := m.directHelperGoViews[dir]
		m.mu.Unlock()
		if manifest == nil {
			return nil, fmt.Errorf("codegen: authoritative helper Go source view is unavailable")
		}
		if cached.manifest == manifest {
			return cached.files, nil
		}
		files := manifest.HelperGoFiles(dir)
		m.mu.Lock()
		m.directHelperGoViews[dir] = helperGoView{manifest: manifest, files: files}
		m.mu.Unlock()
		return files, nil
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

// prepareDirectComponentFamilies attaches only the semantic family identity
// needed by target discovery. Forwarding metadata comes later from the exact
// declarations already parsed by the ordinary full skeleton build.
func prepareDirectComponentFamilies(files map[string]*gsxast.File, plan componentTargetPlan) componentTargetPlan {
	for _, path := range sortedGsxFilePaths(files) {
		for _, authored := range files[path].Decls {
			component, ok := authored.(*gsxast.Component)
			if !ok || component.Recv != "" {
				continue
			}
			emission, ok := plan.emission(component)
			if !ok {
				continue
			}
			declaration := directComponentDeclaration{family: directComponentFamily{logicalKey: plan.logicalKey(component)}}
			emission.direct = &declaration
			plan.emissions[component] = emission
		}
	}
	plan.directPrepared = true
	return plan
}

// finalizePreparedDirectComponentDeclarations replaces preparation markers
// with forwarding metadata derived from the declarations parsed by the full
// skeleton build. A variant family is direct only when every member is valid
// and forwardable.
func finalizePreparedDirectComponentDeclarations(files map[string]*gsxast.File, plan componentTargetPlan) componentTargetPlan {
	type member struct {
		component   *gsxast.Component
		declaration directComponentDeclaration
	}
	byKey := make(map[string][]member)
	rejected := make(map[string]bool)
	for _, path := range sortedGsxFilePaths(files) {
		for _, authored := range files[path].Decls {
			component, ok := authored.(*gsxast.Component)
			if !ok || component.Recv != "" {
				continue
			}
			key := plan.logicalKey(component)
			emission, exists := plan.emission(component)
			if !exists || !emission.declarationParsed {
				rejected[key] = true
				continue
			}
			direct, eligible, err := directComponentDeclarationFromParsed(component, emission.parsedDeclaration)
			if err != nil || !eligible {
				rejected[key] = true
				continue
			}
			byKey[key] = append(byKey[key], member{component: component, declaration: direct})
		}
	}
	for component, emission := range plan.emissions {
		if emission.direct != nil {
			emission.direct = nil
			plan.emissions[component] = emission
		}
	}
	for key, members := range byKey {
		if rejected[key] {
			continue
		}
		family := directComponentFamily{logicalKey: key}
		for _, member := range members {
			emission, ok := plan.emission(member.component)
			if !ok {
				continue
			}
			declaration := member.declaration
			declaration.family = family
			emission.direct = &declaration
			plan.emissions[member.component] = emission
		}
	}
	return plan
}

// assignDirectComponentDeclarationsFromView allocates final helper names from
// the ordinary full-skeleton lexical surface plus the build-oblivious Go view.
// Families are allocated in logical-key order.
func assignDirectComponentDeclarationsFromView(packageName string, files map[string]*gsxast.File, plan componentTargetPlan, lexicalNames map[string]bool, goFiles map[string]sourceview.FileSnapshot) (componentTargetPlan, error) {
	if !plan.directPrepared {
		plan = prepareDirectComponentFamilies(files, plan)
	}
	occupied, err := directHelperOccupiedNamesFromView(packageName, files, goFiles)
	if err != nil {
		return componentTargetPlan{}, err
	}
	for name := range lexicalNames {
		occupied[name] = true
	}
	byKey := make(map[string][]*gsxast.Component)
	for _, path := range sortedGsxFilePaths(files) {
		for _, authored := range files[path].Decls {
			component, ok := authored.(*gsxast.Component)
			if !ok {
				continue
			}
			emission, ok := plan.emission(component)
			if !ok || emission.direct == nil {
				continue
			}
			key := emission.direct.family.logicalKey
			byKey[key] = append(byKey[key], component)
		}
	}
	keys := make([]string, 0, len(byKey))
	for key := range byKey {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		members := byKey[key]
		if len(members) == 0 {
			continue
		}
		base := "_gsxrender" + members[0].Name
		helper := base
		for suffix := 1; occupied[helper]; suffix++ {
			helper = base + strconv.Itoa(suffix)
		}
		occupied[helper] = true
		family := directComponentFamily{logicalKey: key, helperName: helper}
		for _, component := range members {
			emission, ok := plan.emission(component)
			if !ok {
				return componentTargetPlan{}, fmt.Errorf("codegen: direct component %s is absent from the finalized plan", component.Name)
			}
			declaration := *emission.direct
			declaration.family = family
			emission.direct = &declaration
			plan.emissions[component] = emission
		}
	}
	return plan, nil
}

func (m *Module) allocateDirectComponentHelpers(dir, packageName string, files map[string]*gsxast.File, plan componentTargetPlan, lexicalNames map[string]bool) (componentTargetPlan, error) {
	goFiles, err := m.directHelperGoSourceView(dir)
	if err != nil {
		return componentTargetPlan{}, err
	}
	return assignDirectComponentDeclarationsFromView(packageName, files, plan, lexicalNames, goFiles)
}

func hasPreparedDirectComponents(plan componentTargetPlan) bool {
	for _, emission := range plan.emissions {
		if emission.direct != nil {
			return true
		}
	}
	return false
}

func hasLocalPreparedDirectTarget(facts map[callSiteID]componentTargetFact, analysisPackage *types.Package, plan componentTargetPlan) bool {
	if analysisPackage == nil {
		return false
	}
	eligible := make(map[string]bool)
	for _, emission := range plan.emissions {
		if emission.direct != nil {
			eligible[emission.direct.family.logicalKey] = true
		}
	}
	for _, fact := range facts {
		identity := fact.origin
		if identity == nil {
			identity = fact.object
		}
		if fact.declaration.direct != nil && eligible[fact.declaration.direct.logicalKey] && identity != nil && identity.Pkg() != nil && identity.Pkg().Path() == analysisPackage.Path() {
			return true
		}
	}
	return false
}

// refreshLocalDirectTargetFacts replaces the preparation-time family marker
// copied into root target facts with the final late-allocated helper identity.
// Imported facts already carry names allocated by their dependency analysis.
func refreshLocalDirectTargetFacts(facts map[callSiteID]componentTargetFact, analysisPackage *types.Package, plan componentTargetPlan) {
	if analysisPackage == nil {
		return
	}
	byKey := make(map[string]*directComponentFamily)
	for _, emission := range plan.emissions {
		if emission.direct == nil || emission.direct.family.helperName == "" {
			continue
		}
		family := emission.direct.family
		byKey[family.logicalKey] = &family
	}
	for site, fact := range facts {
		identity := fact.origin
		if identity == nil {
			identity = fact.object
		}
		if identity == nil || identity.Pkg() == nil || identity.Pkg().Path() != analysisPackage.Path() || fact.declaration.direct == nil {
			continue
		}
		fact.declaration.direct = byKey[fact.declaration.direct.logicalKey]
		facts[site] = fact
	}
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
