package gsx

// Toggle forces boolean-attribute (presence) semantics on any attribute name,
// bypassing the name tables: Toggle(true) writes a bare ` name`, Toggle(false)
// writes nothing. Its remaining use is the names where the platform DOES define
// a value vocabulary but the author wants presence anyway — a plain bool on a
// name the platform never defined already toggles on its own.
//
// It is a value, not syntax, so the same expression works on an element, as a
// component prop, and in a hand-written bag: gsx.Toggle(b) travels to the leaf
// where the presence decision is actually made.
//
// gsx also uses Toggle internally when a syntactically bare attribute must
// travel through an Attrs bag, carrying its authored presence to the leaf
// independent of what the name tables say.
type Toggle bool
