# Fragments

A gsx component normally has a single root element. **Fragments** let a component return multiple sibling elements with no wrapper element in between.

## Multiple roots

The fragment syntax is `<> … </>` — an open angle-bracket pair with no tag name, and a matching close. The children of a fragment are rendered as a flat sequence with nothing wrapping them.

<!--@include: ./_generated/fragments/010-fragments.md-->

Fragments are useful when a component is designed to be slotted into a parent that controls the layout — for example, a pair of `<dt>`/`<dd>` rows for a description list, or a run of table cells. Wrapping them in a `<div>` would break the DOM structure; a fragment returns them as bare siblings.
