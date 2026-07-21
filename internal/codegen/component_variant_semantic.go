package codegen

import (
	"fmt"
	goast "go/ast"
	goparser "go/parser"
	"go/token"
	"go/types"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	gsxast "github.com/gsxhq/gsx/ast"
	"github.com/gsxhq/gsx/internal/diag"
)

// privateComponentTargetPlan gives every component a unique analysis-only
// declaration name. It makes no statement about variant membership: the plan
// exists only so go/types can see every exact signature without public-name
// redeclarations. Acceptance is decided later from those semantic signatures.
func privateComponentTargetPlan(files map[string]*gsxast.File, allocate func(*gsxast.Component) (string, string)) componentTargetPlan {
	plan := componentTargetPlan{
		emissions:   map[*gsxast.Component]componentTargetEmission{},
		logicalKeys: map[*gsxast.Component]string{},
	}
	index := 0
	paths := make([]string, 0, len(files))
	for path := range files {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		for _, declaration := range files[path].Decls {
			component, ok := declaration.(*gsxast.Component)
			if !ok {
				continue
			}
			index++
			name := fmt.Sprintf("_gsxtargetpreflight%d", index)
			propsName := fmt.Sprintf("_gsxtargetpropspreflight%d", index)
			if allocate != nil {
				name, propsName = allocate(component)
			}
			plan.emissions[component] = componentTargetEmission{splitBody: true, bodyName: name, analysisPropsName: propsName}
			plan.logicalKeys[component] = componentKey(component)
		}
	}
	return plan
}

func publicPlanWhenComponentNamesAreUnique(files map[string]*gsxast.File) (componentTargetPlan, bool) {
	plan := componentTargetPlan{
		emissions:   map[*gsxast.Component]componentTargetEmission{},
		logicalKeys: map[*gsxast.Component]string{},
	}
	seen := map[string]bool{}
	for _, file := range files {
		for _, declaration := range file.Decls {
			component, ok := declaration.(*gsxast.Component)
			if !ok {
				continue
			}
			if seen[component.Name] {
				return componentTargetPlan{}, false
			}
			seen[component.Name] = true
			plan.emissions[component] = componentTargetEmission{public: true}
			plan.logicalKeys[component] = componentKey(component)
		}
	}
	return plan, true
}

func buildComponentSignatureFiles(
	dir string,
	files map[string]*gsxast.File,
	plan componentTargetPlan,
	fset *token.FileSet,
	bag *diag.Bag,
) ([]*goast.File, []string, error) {
	paths := make([]string, 0, len(files))
	for path := range files {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	goFiles := make([]*goast.File, 0, len(paths))
	var importPaths []string
	for _, path := range paths {
		skeleton, err := buildComponentTargetSkeleton(
			files[path], funcTables{}, fset, bag, nil, plan, skeletonTargetDeclarations,
		)
		if err != nil {
			return nil, nil, err
		}
		for _, spec := range skeleton.imports {
			importPaths = append(importPaths, spec.path)
		}
		skeletonPath := filepath.Join(dir, strings.TrimSuffix(filepath.Base(path), ".gsx")+".signature.x.go")
		file, err := goparser.ParseFile(fset, skeletonPath, skeleton.source, goparser.SkipObjectResolution)
		if err != nil {
			return nil, nil, skeletonParseError(err)
		}
		goFiles = append(goFiles, file)
	}
	return goFiles, importPaths, nil
}

func (m *Module) loadedPackageName(importPath string) (string, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if dir := m.sourcePackageDirs[importPath]; dir != "" {
		if source, ok := m.sourcePackages[dir]; ok && source.name != "" {
			return source.name, true
		}
	}
	if pkg := m.extPkgs[importPath]; pkg != nil && pkg.Name() != "" {
		return pkg.Name(), true
	}
	return "", false
}

// componentAnalysisOccupiedNames reserves every identifier spelling visible in
// authored source, plus the implicit bindings of default imports. Analysis-only
// declarations must never satisfy a reference the author wrote: even an
// otherwise-undefined `_gsxtarget...` use therefore forces allocation of a
// different private name and remains an ordinary positioned Go error.
func (m *Module) componentAnalysisOccupiedNames(files []*goast.File, importer types.Importer) (map[string]bool, error) {
	occupied := map[string]bool{}
	for _, file := range files {
		goast.Inspect(file, func(node goast.Node) bool {
			if identifier, ok := node.(*goast.Ident); ok {
				occupied[identifier.Name] = true
			}
			return true
		})
		for _, spec := range file.Imports {
			if spec.Name != nil {
				continue
			}
			path, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				return nil, fmt.Errorf("codegen: decode component-signature import: %w", err)
			}
			name, ok := m.loadedPackageName(path)
			if !ok && importer != nil {
				pkg, importErr := importer.Import(path)
				if importErr == nil && pkg != nil && pkg.Name() != "" {
					name, ok = pkg.Name(), true
				}
			}
			if !ok {
				// The normal successful path has a cold-inventory identity for every
				// import. If an authored import is unresolved, collision allocation is
				// not the diagnostic authority: leave it unreserved and let the real
				// checker report the positioned import/type error. No semantic plan can
				// be accepted from an unresolved receiver below.
				continue
			}
			occupied[name] = true
		}
	}
	return occupied, nil
}

func componentShippingPropsName(component *gsxast.Component, declarationName string) string {
	if component.Recv == "" {
		return declarationName + "Props"
	}
	_, _, receiverName, err := parseRecv(component.Recv)
	if err != nil {
		return declarationName + "Props"
	}
	return receiverName + declarationName + "Props"
}

func allocateComponentAnalysisNames(files map[string]*gsxast.File, occupied map[string]bool) componentTargetPlan {
	for _, file := range files {
		for _, declaration := range file.Decls {
			component, ok := declaration.(*gsxast.Component)
			if !ok {
				continue
			}
			occupied[component.Name] = true
			occupied[componentShippingPropsName(component, component.Name)] = true
		}
	}
	next := 0
	return privateComponentTargetPlan(files, func(component *gsxast.Component) (string, string) {
		for {
			next++
			name := fmt.Sprintf("_gsxtargetbody%d", next)
			propsName := fmt.Sprintf("_gsxtargetprops%d", next)
			if occupied[name] || occupied[propsName] {
				continue
			}
			occupied[name] = true
			occupied[propsName] = true
			return name, propsName
		}
	})
}

type semanticVariantGroup struct {
	name           string
	hasReceiver    bool
	receiverOrigin *types.TypeName
	isolated       bool
	members        []componentVariantMember
}

// semanticReceiverOrigin returns the declaration identity that legally owns
// object. Pointer/value spelling and receiver type-parameter names are not part
// of family membership: Go gives T and *T one method namespace, and Receiver[T]
// and Receiver[U] declare methods on the same generic origin. Their exact
// receiver shapes remain part of signature validation after grouping.
//
// A named receiver's underlying declaration is deliberately not traversed.
// Errors in one of its fields belong to the real package checker and do not
// turn aliases of the same declaration into different receiver families.
//
// Merely resolving a receiver type is insufficient: go/types can construct a
// function object for an illegal method on a non-local type or an invalid alias
// receiver. A legal method declaration installs this exact private preflight
// object on the named origin. Object identity in the origin method set is the
// semantic legality proof; no package-name or receiver-text heuristic is used.
func semanticReceiverOrigin(object *types.Func) *types.TypeName {
	if object == nil {
		return nil
	}
	signature, ok := object.Type().(*types.Signature)
	if !ok || signature.Recv() == nil {
		return nil
	}
	t := signature.Recv().Type()
	t = types.Unalias(t)
	if pointer, ok := t.(*types.Pointer); ok {
		t = types.Unalias(pointer.Elem())
	}
	named, ok := t.(*types.Named)
	if !ok || named.Obj() == nil {
		return nil
	}
	for typeArg := range named.TypeArgs().Types() {
		if !semanticReceiverTypeArgUsable(typeArg, map[types.Type]bool{}) {
			return nil
		}
	}
	origin := named.Origin()
	if origin == nil {
		return nil
	}
	for method := range origin.Methods() {
		if method == object {
			return origin.Obj()
		}
	}
	return nil
}

func semanticReceiverTypeArgUsable(t types.Type, seen map[types.Type]bool) bool {
	if t == nil {
		return false
	}
	t = types.Unalias(t)
	if seen[t] {
		return true
	}
	seen[t] = true
	switch t := t.(type) {
	case *types.Basic:
		return t.Kind() != types.Invalid
	case *types.Array:
		return semanticReceiverTypeArgUsable(t.Elem(), seen)
	case *types.Slice:
		return semanticReceiverTypeArgUsable(t.Elem(), seen)
	case *types.Pointer:
		return semanticReceiverTypeArgUsable(t.Elem(), seen)
	case *types.Map:
		return semanticReceiverTypeArgUsable(t.Key(), seen) && semanticReceiverTypeArgUsable(t.Elem(), seen)
	case *types.Chan:
		return semanticReceiverTypeArgUsable(t.Elem(), seen)
	case *types.Named:
		for typeArg := range t.TypeArgs().Types() {
			if !semanticReceiverTypeArgUsable(typeArg, seen) {
				return false
			}
		}
		return true
	case *types.TypeParam:
		// The receiver's declared type parameters establish identity directly;
		// errors in their constraints belong to the normal checker.
		return true
	default:
		return false
	}
}

// componentVariantSignatureErrors attributes preflight checker errors to the
// exact private function signature that contains them. A malformed type can
// retain a fully shaped go/types representation (for example map[[]int]string
// or an invalid generic instantiation), so inspecting the resulting type graph
// alone cannot prove that an authored signature is valid.
//
// The private declaration name is the stable bridge back to its GSX component.
// Positions are compared in the parsed Go syntax's token space rather than via
// //line-adjusted source coordinates. Errors elsewhere in the package remain
// the normal checker's concern and do not poison a valid component signature.
func componentVariantSignatureErrors(
	files []*goast.File,
	plan componentTargetPlan,
	typeErrs []types.Error,
) map[*gsxast.Component]bool {
	byName := make(map[string]*gsxast.Component)
	for component, emission := range plan.emissions {
		if emission.splitBody && emission.bodyName != "" {
			byName[emission.bodyName] = component
		}
	}
	type signatureSpan struct {
		component *gsxast.Component
		start     token.Pos
		end       token.Pos
	}
	var spans []signatureSpan
	for _, file := range files {
		for _, declaration := range file.Decls {
			function, ok := declaration.(*goast.FuncDecl)
			if !ok {
				continue
			}
			component := byName[function.Name.Name]
			if component == nil || function.Type.Params == nil {
				continue
			}
			start := function.Name.Pos()
			if function.Recv != nil {
				start = function.Recv.Pos()
			}
			spans = append(spans, signatureSpan{
				component: component,
				start:     start,
				end:       function.Type.Params.End(),
			})
		}
	}
	invalid := make(map[*gsxast.Component]bool)
	for _, typeErr := range typeErrs {
		if typeErr.Pos == token.NoPos {
			continue
		}
		for _, span := range spans {
			if span.start <= typeErr.Pos && typeErr.Pos < span.end {
				invalid[span.component] = true
				break
			}
		}
	}
	return invalid
}

func finalizedPlanFromSemanticReceivers(
	files map[string]*gsxast.File,
	sources map[string][]byte,
	privatePlan componentTargetPlan,
	objects map[*gsxast.Component]*types.Func,
	signatureErrors map[*gsxast.Component]bool,
	bag *diag.Bag,
) componentTargetPlan {
	var groups []semanticVariantGroup
	paths := make([]string, 0, len(files))
	for path := range files {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		for _, declaration := range files[path].Decls {
			component, ok := declaration.(*gsxast.Component)
			if !ok {
				continue
			}
			member := componentVariantMember{path: path, component: component}
			if component.Recv == "" {
				found := false
				for index := range groups {
					if !groups[index].hasReceiver && groups[index].name == component.Name {
						groups[index].members = append(groups[index].members, member)
						found = true
						break
					}
				}
				if !found {
					groups = append(groups, semanticVariantGroup{name: component.Name, members: []componentVariantMember{member}})
				}
				continue
			}
			receiverOrigin := semanticReceiverOrigin(objects[component])
			// An unresolved receiver cannot prove semantic family membership. Keep
			// it isolated; the later checker reports the underlying type error.
			found := false
			if receiverOrigin != nil {
				for index := range groups {
					if groups[index].hasReceiver && groups[index].receiverOrigin == receiverOrigin && groups[index].name == component.Name {
						groups[index].members = append(groups[index].members, member)
						found = true
						break
					}
				}
			}
			if !found {
				groups = append(groups, semanticVariantGroup{
					name:           component.Name,
					hasReceiver:    true,
					receiverOrigin: receiverOrigin,
					isolated:       receiverOrigin == nil,
					members:        []componentVariantMember{member},
				})
			}
		}
	}

	plan := componentTargetPlan{
		emissions:   map[*gsxast.Component]componentTargetEmission{},
		logicalKeys: map[*gsxast.Component]string{},
	}
	isolate := func(reason string, member componentVariantMember) string {
		filePos := token.NoPos
		if file := files[member.path]; file != nil {
			filePos = file.Pos()
		}
		offset := int(member.component.NamePos - filePos)
		return fmt.Sprintf("!%s:%d:%s:%d:%s", reason, len(member.path), member.path, offset, member.component.Name)
	}
	publishIsolated := func(reason string, members []componentVariantMember, private bool) {
		for _, member := range members {
			plan.logicalKeys[member.component] = isolate(reason, member)
			if private {
				plan.emissions[member.component] = privatePlan.emissions[member.component]
			} else {
				plan.emissions[member.component] = componentTargetEmission{public: true}
			}
		}
	}
	for _, group := range groups {
		key := componentKey(group.members[0].component)
		if group.receiverOrigin != nil {
			key = group.receiverOrigin.Name() + "." + group.name
		}
		if group.isolated {
			publishIsolated("unresolved-receiver", group.members, false)
			continue
		}
		if len(group.members) == 1 {
			plan.logicalKeys[group.members[0].component] = key
			plan.emissions[group.members[0].component] = componentTargetEmission{public: true}
			continue
		}
		valid := true
		counts := map[string]int{}
		for _, member := range group.members {
			counts[member.path]++
		}
		for _, count := range counts {
			if count > 1 {
				valid = false
				break
			}
		}
		if valid {
			for _, member := range group.members {
				constrained, err := componentFileHasEffectiveConstraint(member.path, files[member.path], sources[member.path])
				if err != nil || !constrained {
					valid = false
					break
				}
			}
		}
		if !valid {
			plan.invalidMembership = true
			reportInvalidComponentVariantFamily(key, group.members, files, sources, bag)
			publishIsolated("invalid-variant", group.members, true)
			continue
		}
		if !componentVariantFamilySignaturesMatch(group.members, objects, signatureErrors) {
			plan.invalidMembership = true
			reportComponentVariantSignatureMismatch(group.members, bag)
			publishIsolated("invalid-signature", group.members, true)
			continue
		}
		for _, member := range group.members {
			plan.logicalKeys[member.component] = key
		}
		family := componentVariantFamily{key: key, members: group.members}
		plan.families = append(plan.families, family)
		for index, member := range group.members {
			private := privatePlan.emissions[member.component]
			plan.emissions[member.component] = componentTargetEmission{
				public:            index == 0,
				splitBody:         true,
				bodyName:          private.bodyName,
				analysisPropsName: private.analysisPropsName,
			}
		}
	}
	return plan
}

// finalizedComponentTargetPlan performs a transient signature-only type check
// under collision-free private names, then builds the one semantic emission
// plan shared by shipping, exact-target, and renderer declaration analysis.
// No types from this transient universe are retained in Module caches.
func (m *Module) finalizedComponentTargetPlan(
	dir, pkgPath, pkgName string,
	files map[string]*gsxast.File,
	sources map[string][]byte,
	fset *token.FileSet,
	bag *diag.Bag,
	importer types.Importer,
	typeEnvironment typeCheckEnvironment,
) (componentTargetPlan, error) {
	if len(files) == 0 {
		return componentTargetPlan{
			emissions:   map[*gsxast.Component]componentTargetEmission{},
			logicalKeys: map[*gsxast.Component]string{},
		}, nil
	}
	// A semantic family can only contain declarations with the same authored
	// component name. When names are globally unique there is nothing to fold,
	// so the exact all-public result needs neither private names nor a checker.
	if plan, unique := publicPlanWhenComponentNamesAreUnique(files); unique {
		return assignDirectComponentDeclarations(dir, pkgName, files, plan)
	}
	provisional := privateComponentTargetPlan(files, nil)
	provisionalFiles, _, err := buildComponentSignatureFiles(dir, files, provisional, fset, bag)
	if err != nil {
		return componentTargetPlan{}, err
	}
	companions, companionImports, err := m.parseTargetCompanionGoFiles(dir, files)
	if err != nil {
		return componentTargetPlan{}, err
	}
	occupied, err := m.componentAnalysisOccupiedNames(append(append([]*goast.File(nil), provisionalFiles...), companions...), importer)
	if err != nil {
		return componentTargetPlan{}, err
	}
	privatePlan := allocateComponentAnalysisNames(files, occupied)
	preflightFiles, importPaths, err := buildComponentSignatureFiles(dir, files, privatePlan, fset, bag)
	if err != nil {
		return componentTargetPlan{}, err
	}
	preflightFiles = append(preflightFiles, companions...)
	importPaths = append(importPaths, companionImports...)
	// Total direct path provenance is complete before the checker recurses.
	m.recordTargetImports(dir, importPaths)
	_, info, typeErrs := checkComponentTargetPackage(pkgPath, pkgName, preflightFiles, fset, importer, componentTargetCheckConfig{
		ignoreFuncBodies:         true,
		disableUnusedImportCheck: true,
		typeEnvironment:          typeEnvironment,
	})
	objects := variantFuncObjects(preflightFiles, info, privatePlan)
	signatureErrors := componentVariantSignatureErrors(preflightFiles, privatePlan, typeErrs)
	plan := finalizedPlanFromSemanticReceivers(files, sources, privatePlan, objects, signatureErrors, bag)
	return assignDirectComponentDeclarations(dir, pkgName, files, plan)
}
