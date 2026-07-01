# Fragments

**Fragments** group multiple sibling elements without adding a wrapper element.

## Multiple roots

The fragment syntax is `<> … </>` — an open angle-bracket pair with no tag name, and a matching close. The children of a fragment are rendered as a flat sequence with nothing wrapping them.

<!--@include: ./_generated/fragments/010-fragments.md-->

Fragments are useful when a nested expression needs one syntactic child but the
rendered output should stay flat — for example, a pair of `<dt>`/`<dd>` rows for
a description list or a run of table cells.
