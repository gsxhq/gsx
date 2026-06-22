package gsx

// RawJS is a string the template author vouches for as safe JavaScript. In a JS
// value context — inside a <script> block or an event-handler attribute — a
// RawJS value passed to gw.JSVal is emitted verbatim, bypassing JSON marshaling
// and escaping (the JS analogue of trusting raw HTML via Raw, or template.JS in
// html/template). Use it only for JavaScript you control, never for untrusted
// data.
type RawJS string
