# Comparisons

Choose by where templates live, when they load, and where rendering happens.

| tool | best fit | component typing | compile/load model | rendering model |
| --- | --- | --- | --- | --- |
| gsx | HTML-shaped Go views | Go parameters | generate before build | server HTML |
| templ | Go-first components | Go parameters | generate before build | server HTML |
| `html/template` | stdlib or dynamic templates | runtime data | parse embedded or runtime text | server HTML |
| client-side JSX | browser applications | JavaScript or TypeScript props | compile for the browser | browser UI |

## Choose gsx

Choose gsx when you want JSX-like calls and HTML-shaped component bodies while
keeping expressions, component inputs, and builds in Go. It is especially useful when
class, attribute, and contextual-escaping rules should be part of the template
language.

## Choose templ

Choose templ when you prefer its Go-first syntax or need its existing ecosystem.
Interop is structural: `gsx.Node` and `templ.Component` both expose
`Render(context.Context, io.Writer) error`, so gsx nodes work where templ
components are accepted. See the runnable [Interop examples](./syntax/interop.md).

## Choose `html/template`

Choose `html/template` when the standard library is the priority or templates
must be parsed or replaced at runtime. Choose gsx when templates can be compiled
with the application and component calls should be checked by Go.

## Choose client-side JSX

Choose React or another JSX-based client framework for browser-owned state and
rich client interaction. Choose gsx for server-rendered HTML, with JavaScript
islands where needed.
