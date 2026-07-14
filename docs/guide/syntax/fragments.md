# Fragments

Fragments group multiple sibling nodes without adding an HTML wrapper.

## Multiple roots

Write a fragment as `<>…</>`. Its children render in order with no surrounding tag.

<!--@include: ./_generated/fragments/010-fragments.md-->

Use a fragment whenever one markup value needs multiple roots. Fragments do not accept attributes because they have no element of their own.
