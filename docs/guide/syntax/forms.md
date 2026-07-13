# Forms

Forms use ordinary HTML elements. A small field component can own its label and
layout while forwarding input-specific attributes from each call site.

## A reusable form field

Place `{ attrs... }` on the inner `<input>` so undeclared caller attributes such
as `type`, `name`, and `required` reach that element.

<!--@include: ./_generated/forms/010-forms.md-->

`label` binds to the component param; the remaining attributes form the bag that
`Field` forwards. Its own `class="control"` stays in place, and a caller-supplied
class would merge at the spread position. See [Attributes](./attributes.md) for
spread ordering and [Styling](./styling.md#class-style-merging) for class merging.

## Server-side validation is ordinary Go

Parse and validate the request in the HTTP handler, then pass submitted values
and field errors back to the form component as typed params. With `net/http`, use
[`Request.ParseForm`](https://pkg.go.dev/net/http#Request.ParseForm); with a web
framework, use its normal binding and validation support. gsx only renders the
resulting form state.
