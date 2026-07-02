# Generic Tag Inference Design

## Goal

Allow a generic component tag to omit type arguments when Go can infer them from
the component props supplied at the tag site.

## Behavior

Explicit type arguments remain supported:

```gsx
<Greeting[string, string] v="hello" w="world" />
```

When omitted, gsx asks Go's type checker to infer the type arguments during the
analysis skeleton pass:

```gsx
<Greeting v="hello" w="world" />
```

If inference succeeds, generated `.x.go` still contains an explicit Go
instantiation:

```go
Greeting[string, string](GreetingProps[string, string]{V: "hello", W: "world"})
```

If inference fails, gsx reports a positioned diagnostic:

```text
type inference failed for <Greeting>; please instantiate with <Greeting[type, type] ...>
```

## Architecture

The final generated code must not contain inference helpers. During skeleton
analysis only, generated-props generic components expose an exported helper:

```go
func GsxInferGreeting[V ~string, W any](v V, w W) GreetingProps[V, W] {
	return GreetingProps[V, W]{}
}
```

For a tag with omitted type arguments, the skeleton probes:

```go
_ = Greeting(GsxInferGreeting("hello", "world"))
```

Go infers the helper's type arguments. Harvest reads the instantiated helper
call result from `go/types.Info.Types`, records the resulting props type on the
original tag element, and final emission prints the corresponding type argument
list on both the component call and props literal.

## Scope

This design covers generated-props generic function components and dotted
cross-package calls to those components. It does not implement a gsx-owned type
inference engine. Cases Go cannot infer from supplied props require explicit type
arguments.
