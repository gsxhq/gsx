package gsx

// RawCSS is a string the template author vouches for as safe CSS. In a CSS
// context — inside a <style> block or a style= attribute — a RawCSS value is
// emitted verbatim, bypassing the gw.CSS value-filter (the CSS analogue of
// trusting raw HTML via Raw). Use it only for CSS you control, never for
// untrusted data.
type RawCSS string
