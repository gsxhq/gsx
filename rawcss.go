package gsx

// RawCSS is a string the template author vouches for as safe CSS. In a CSS
// context — inside a <style> block or a style= attribute — a RawCSS value is
// emitted verbatim, bypassing the gw.CSS value-filter (the CSS analogue of
// trusting raw HTML via Raw). Use it only for CSS you control, never for
// untrusted data.
type RawCSS string

// StyleValue renders a composed-style part's value: a gsx.RawCSS value is the
// author's vouch and is emitted verbatim; any other value is CSS-value-filtered
// (cssValueFilter) so untrusted data cannot inject declarations or break out.
// Used by generated code for the dynamic parts of a composed style={ … }.
func StyleValue(v any) string {
	if rc, ok := v.(RawCSS); ok {
		return string(rc)
	}
	return cssValueFilter(toStr(v))
}
