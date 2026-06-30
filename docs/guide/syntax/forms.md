# Forms

HTML forms are plain gsx — `<form>`, `<input>`, `<button>`, `<label>` are ordinary elements. The patterns here cover one language feature that makes reusable form fields clean: forwarding undeclared attributes with `{ attrs... }`.

## A reusable form field

A `Field` component can wrap a `<label>` + `<input>` pair without needing to redeclare every HTML attribute the input might need. Pass `{ attrs... }` on the inner `<input>` and any attribute the caller supplies that is not a named param gets forwarded there directly.

<!--@include: ./_generated/forms/010-forms.md-->

`Field` declares only one param, `label string`. The `<input>` element carries a static `class="control"` and then `{ attrs... }`, which spreads the remaining caller-supplied attributes onto it at render time. The call `<Field label="Email" type="email" name="email" required/>` maps `label` to the named param; `type`, `name`, and `required` are undeclared so they go into `attrs` and are forwarded to `<input>`. The rendered output shows `<input class="control" name="email" required type="email"/>` — both the component's own `class` and the caller's attributes coexist on the element.

## Server-side validation is ordinary Go

gsx renders the form; reading and validating the submitted data is plain `net/http`. Use `r.FormValue`, `r.ParseMultipartForm`, or any form-decoding library in your handler. If validation fails, pass error state as params to a component and render the form again with inline error messages — that is standard Go, not a gsx language feature.

See the Go standard library documentation for [`net/http.Request`](https://pkg.go.dev/net/http#Request) for request parsing, and search for form validation libraries on [pkg.go.dev](https://pkg.go.dev/search?q=form+validation).
