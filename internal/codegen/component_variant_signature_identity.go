package codegen

import "go/types"

// componentVariantSignatureUsable proves that every type position that
// contributes to variant identity is semantically available. go/types uses the
// shared Invalid basic type after unresolved names, so types.Identical alone
// would otherwise make unrelated failures (MissingA and MissingB) compare
// equal. Named declarations are identity-bearing leaves: their underlying
// implementation is deliberately not traversed, allowing a valid receiver
// declaration to form a family while the normal checker independently reports
// an error in one of that declaration's fields.
func componentVariantSignatureUsable(signature *types.Signature) bool {
	if signature == nil {
		return false
	}
	seen := map[types.Type]bool{}
	if receiver := signature.Recv(); receiver != nil && !componentVariantTypeUsable(receiver.Type(), seen) {
		return false
	}
	return componentVariantTypeParamsUsable(signature.RecvTypeParams(), seen) &&
		componentVariantTypeParamsUsable(signature.TypeParams(), seen) &&
		componentVariantTupleUsable(signature.Params(), seen) &&
		componentVariantTupleUsable(signature.Results(), seen)
}

func componentVariantTypeParamsUsable(parameters *types.TypeParamList, seen map[types.Type]bool) bool {
	for index := 0; index < parameters.Len(); index++ {
		constraint := parameters.At(index).Constraint()
		if constraint == nil || !componentVariantTypeUsable(constraint, seen) {
			return false
		}
	}
	return true
}

func componentVariantTupleUsable(tuple *types.Tuple, seen map[types.Type]bool) bool {
	if tuple == nil {
		return true // go/types represents an empty parameter/result tuple as nil
	}
	for index := 0; index < tuple.Len(); index++ {
		if !componentVariantTypeUsable(tuple.At(index).Type(), seen) {
			return false
		}
	}
	return true
}

func componentVariantTypeUsable(t types.Type, seen map[types.Type]bool) bool {
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
		return t.Len() >= 0 && componentVariantTypeUsable(t.Elem(), seen)
	case *types.Slice:
		return componentVariantTypeUsable(t.Elem(), seen)
	case *types.Pointer:
		return componentVariantTypeUsable(t.Elem(), seen)
	case *types.Map:
		return componentVariantTypeUsable(t.Key(), seen) && componentVariantTypeUsable(t.Elem(), seen)
	case *types.Chan:
		return componentVariantTypeUsable(t.Elem(), seen)
	case *types.Named:
		if t.Obj() == nil {
			return false
		}
		for typeArg := range t.TypeArgs().Types() {
			if !componentVariantTypeUsable(typeArg, seen) {
				return false
			}
		}
		return true
	case *types.TypeParam:
		return t.Obj() != nil && t.Constraint() != nil && componentVariantTypeUsable(t.Constraint(), seen)
	case *types.Struct:
		for field := range t.Fields() {
			if !componentVariantTypeUsable(field.Type(), seen) {
				return false
			}
		}
		return true
	case *types.Tuple:
		return componentVariantTupleUsable(t, seen)
	case *types.Signature:
		return componentVariantSignatureUsableWithSeen(t, seen)
	case *types.Interface:
		t.Complete()
		for index := 0; index < t.NumExplicitMethods(); index++ {
			if !componentVariantTypeUsable(t.ExplicitMethod(index).Type(), seen) {
				return false
			}
		}
		for index := 0; index < t.NumEmbeddeds(); index++ {
			if !componentVariantTypeUsable(t.EmbeddedType(index), seen) {
				return false
			}
		}
		return true
	case *types.Union:
		for index := 0; index < t.Len(); index++ {
			if !componentVariantTypeUsable(t.Term(index).Type(), seen) {
				return false
			}
		}
		return true
	default:
		return false
	}
}

func componentVariantSignatureUsableWithSeen(signature *types.Signature, seen map[types.Type]bool) bool {
	if receiver := signature.Recv(); receiver != nil && !componentVariantTypeUsable(receiver.Type(), seen) {
		return false
	}
	return componentVariantTypeParamsUsable(signature.RecvTypeParams(), seen) &&
		componentVariantTypeParamsUsable(signature.TypeParams(), seen) &&
		componentVariantTupleUsable(signature.Params(), seen) &&
		componentVariantTupleUsable(signature.Results(), seen)
}

// identicalComponentVariantSignatures compares complete component signatures,
// including receiver shape. go/types deliberately ignores receivers in
// types.Identical(Signature, Signature), and standalone receiver types that use
// receiver parameters are not alpha-equivalent: Receiver[T] and Receiver[U]
// contain distinct *types.TypeParam objects. Variant identity needs both rules
// at once, so it substitutes the right signature's type parameters with the
// corresponding left parameters before comparing constraints, receiver,
// parameters, and results.
func identicalComponentVariantSignatures(left, right *types.Signature) bool {
	if left == nil || right == nil {
		return left == right
	}

	substitution := newComponentVariantTypeSubstitution()
	if !bindComponentVariantTypeParams(substitution, left.RecvTypeParams(), right.RecvTypeParams()) ||
		!bindComponentVariantTypeParams(substitution, left.TypeParams(), right.TypeParams()) {
		return false
	}
	if !identicalComponentVariantTypeParamConstraints(substitution, left.RecvTypeParams(), right.RecvTypeParams()) ||
		!identicalComponentVariantTypeParamConstraints(substitution, left.TypeParams(), right.TypeParams()) {
		return false
	}

	leftReceiver, rightReceiver := left.Recv(), right.Recv()
	if leftReceiver == nil || rightReceiver == nil {
		if leftReceiver != nil || rightReceiver != nil {
			return false
		}
	} else if !types.Identical(leftReceiver.Type(), substitution.typ(rightReceiver.Type())) {
		return false
	}
	if !substitution.valid || left.Variadic() != right.Variadic() {
		return false
	}
	return identicalComponentVariantTuples(substitution, left.Params(), right.Params()) &&
		identicalComponentVariantTuples(substitution, left.Results(), right.Results()) &&
		substitution.valid
}

func bindComponentVariantTypeParams(substitution *componentVariantTypeSubstitution, left, right *types.TypeParamList) bool {
	if left.Len() != right.Len() {
		return false
	}
	for index := 0; index < left.Len(); index++ {
		substitution.replacements[right.At(index)] = left.At(index)
	}
	return true
}

func identicalComponentVariantTypeParamConstraints(substitution *componentVariantTypeSubstitution, left, right *types.TypeParamList) bool {
	for index := 0; index < left.Len(); index++ {
		leftConstraint := left.At(index).Constraint()
		rightConstraint := right.At(index).Constraint()
		if leftConstraint == nil || rightConstraint == nil {
			if leftConstraint != nil || rightConstraint != nil {
				return false
			}
			continue
		}
		if !types.Identical(leftConstraint, substitution.typ(rightConstraint)) || !substitution.valid {
			return false
		}
	}
	return true
}

func identicalComponentVariantTuples(substitution *componentVariantTypeSubstitution, left, right *types.Tuple) bool {
	if left.Len() != right.Len() {
		return false
	}
	for index := 0; index < left.Len(); index++ {
		if !types.Identical(left.At(index).Type(), substitution.typ(right.At(index).Type())) || !substitution.valid {
			return false
		}
	}
	return true
}

// componentVariantTypeSubstitution is a type-complete substitution for type
// parameters appearing in package-level signatures. It follows the same shape
// as go/types substitution: composite types are rebuilt only when a child
// changes, instantiated named types are recreated from their origin, aliases
// are unaliased, and interface type sets are rebuilt from their explicit
// methods and embedded terms before types.Identical compares them.
//
// Generic signatures cannot be nested in a Go parameter, result, or constraint
// type. Top-level component signature parameters are handled by the caller, so
// encountering one while recursively substituting denotes a type universe that
// cannot come from checked Go source and fails closed via valid=false.
type componentVariantTypeSubstitution struct {
	replacements map[*types.TypeParam]types.Type
	cache        map[types.Type]types.Type
	context      *types.Context
	visiting     map[types.Type]bool
	valid        bool
}

func newComponentVariantTypeSubstitution() *componentVariantTypeSubstitution {
	return &componentVariantTypeSubstitution{
		replacements: map[*types.TypeParam]types.Type{},
		cache:        map[types.Type]types.Type{},
		context:      types.NewContext(),
		visiting:     map[types.Type]bool{},
		valid:        true,
	}
}

func (substitution *componentVariantTypeSubstitution) typ(t types.Type) types.Type {
	if t == nil {
		return nil
	}
	if replacement, ok := substitution.cache[t]; ok {
		return replacement
	}
	if unaliased := types.Unalias(t); unaliased != t {
		replacement := substitution.typ(unaliased)
		substitution.cache[t] = replacement
		return replacement
	}
	if substitution.visiting[t] {
		// Valid source-level cycles cross a named type, which is never expanded
		// here. A direct anonymous cycle is not representable by checked Go source.
		substitution.valid = false
		return t
	}
	substitution.visiting[t] = true
	defer delete(substitution.visiting, t)

	var result types.Type
	switch t := t.(type) {
	case *types.TypeParam:
		result = substitution.replacements[t]
		if result == nil {
			result = t
		}
	case *types.Basic:
		result = t
	case *types.Array:
		element := substitution.typ(t.Elem())
		if element == t.Elem() {
			result = t
		} else {
			result = types.NewArray(element, t.Len())
		}
	case *types.Slice:
		element := substitution.typ(t.Elem())
		if element == t.Elem() {
			result = t
		} else {
			result = types.NewSlice(element)
		}
	case *types.Pointer:
		element := substitution.typ(t.Elem())
		if element == t.Elem() {
			result = t
		} else {
			result = types.NewPointer(element)
		}
	case *types.Map:
		key := substitution.typ(t.Key())
		element := substitution.typ(t.Elem())
		if key == t.Key() && element == t.Elem() {
			result = t
		} else {
			result = types.NewMap(key, element)
		}
	case *types.Chan:
		element := substitution.typ(t.Elem())
		if element == t.Elem() {
			result = t
		} else {
			result = types.NewChan(t.Dir(), element)
		}
	case *types.Tuple:
		result = substitution.tuple(t)
	case *types.Struct:
		result = substitution.structure(t)
	case *types.Signature:
		result = substitution.signature(t)
	case *types.Union:
		result = substitution.union(t)
	case *types.Interface:
		result = substitution.interfaceType(t)
	case *types.Named:
		result = substitution.named(t)
	default:
		substitution.valid = false
		result = t
	}
	substitution.cache[t] = result
	return result
}

func (substitution *componentVariantTypeSubstitution) tuple(tuple *types.Tuple) *types.Tuple {
	if tuple == nil {
		return nil
	}
	var variables []*types.Var
	for index := 0; index < tuple.Len(); index++ {
		variable := tuple.At(index)
		typ := substitution.typ(variable.Type())
		if typ != variable.Type() && variables == nil {
			variables = make([]*types.Var, tuple.Len())
			for previous := 0; previous < index; previous++ {
				variables[previous] = tuple.At(previous)
			}
		}
		if variables != nil {
			if typ == variable.Type() {
				variables[index] = variable
			} else {
				variables[index] = types.NewVar(variable.Pos(), variable.Pkg(), variable.Name(), typ)
			}
		}
	}
	if variables == nil {
		return tuple
	}
	return types.NewTuple(variables...)
}

func (substitution *componentVariantTypeSubstitution) structure(structure *types.Struct) *types.Struct {
	var fields []*types.Var
	for index := 0; index < structure.NumFields(); index++ {
		field := structure.Field(index)
		typ := substitution.typ(field.Type())
		if typ != field.Type() && fields == nil {
			fields = make([]*types.Var, structure.NumFields())
			for previous := 0; previous < index; previous++ {
				fields[previous] = structure.Field(previous)
			}
		}
		if fields != nil {
			if typ == field.Type() {
				fields[index] = field
			} else {
				fields[index] = types.NewField(field.Pos(), field.Pkg(), field.Name(), typ, field.Embedded())
			}
		}
	}
	if fields == nil {
		return structure
	}
	tags := make([]string, structure.NumFields())
	for index := range tags {
		tags[index] = structure.Tag(index)
	}
	return types.NewStruct(fields, tags)
}

func (substitution *componentVariantTypeSubstitution) signature(signature *types.Signature) *types.Signature {
	if signature.TypeParams().Len() != 0 || signature.RecvTypeParams().Len() != 0 {
		substitution.valid = false
		return signature
	}
	parameters := substitution.tuple(signature.Params())
	results := substitution.tuple(signature.Results())
	if parameters == signature.Params() && results == signature.Results() {
		return signature
	}
	// A receiver is irrelevant to function-type identity and interface method
	// receivers are reconstructed by NewInterfaceType.
	return types.NewSignatureType(nil, nil, nil, parameters, results, signature.Variadic())
}

func (substitution *componentVariantTypeSubstitution) union(union *types.Union) *types.Union {
	var terms []*types.Term
	for index := 0; index < union.Len(); index++ {
		term := union.Term(index)
		typ := substitution.typ(term.Type())
		if typ != term.Type() && terms == nil {
			terms = make([]*types.Term, union.Len())
			for previous := 0; previous < index; previous++ {
				terms[previous] = union.Term(previous)
			}
		}
		if terms != nil {
			if typ == term.Type() {
				terms[index] = term
			} else {
				terms[index] = types.NewTerm(term.Tilde(), typ)
			}
		}
	}
	if terms == nil {
		return union
	}
	return types.NewUnion(terms)
}

func (substitution *componentVariantTypeSubstitution) interfaceType(iface *types.Interface) *types.Interface {
	methods := make([]*types.Func, iface.NumExplicitMethods())
	methodsChanged := false
	for index := 0; index < iface.NumExplicitMethods(); index++ {
		method := iface.ExplicitMethod(index)
		signature := method.Type().(*types.Signature)
		receiverless := types.NewSignatureType(nil, nil, nil, signature.Params(), signature.Results(), signature.Variadic())
		substituted := substitution.signature(receiverless)
		methods[index] = types.NewFunc(method.Pos(), method.Pkg(), method.Name(), substituted)
		methodsChanged = methodsChanged || substituted != receiverless
	}

	embeddeds := make([]types.Type, iface.NumEmbeddeds())
	embeddedsChanged := false
	for index := 0; index < iface.NumEmbeddeds(); index++ {
		embedded := iface.EmbeddedType(index)
		embeddeds[index] = substitution.typ(embedded)
		embeddedsChanged = embeddedsChanged || embeddeds[index] != embedded
	}
	if !methodsChanged && !embeddedsChanged {
		return iface
	}
	result := types.NewInterfaceType(methods, embeddeds)
	if iface.IsImplicit() {
		result.MarkImplicit()
	}
	return result.Complete()
}

func (substitution *componentVariantTypeSubstitution) named(named *types.Named) types.Type {
	arguments := named.TypeArgs()
	if arguments.Len() == 0 {
		return named
	}
	substituted := make([]types.Type, arguments.Len())
	changed := false
	for index := 0; index < arguments.Len(); index++ {
		substituted[index] = substitution.typ(arguments.At(index))
		changed = changed || substituted[index] != arguments.At(index)
	}
	if !changed {
		return named
	}
	instance, err := types.Instantiate(substitution.context, named.Origin(), substituted, false)
	if err != nil {
		substitution.valid = false
		return named
	}
	return instance
}
