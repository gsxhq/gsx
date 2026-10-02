# CSP nonce

Send a Content Security Policy with a fresh nonce on every request, and let gsx
stamp that nonce on every `<script>`, `<style>` and `<link>` it renders.

## Copy the middleware

```go
package app

import (
	"crypto/rand"
	"net/http"

	"github.com/gsxhq/gsx"
)

func CSP(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nonce := rand.Text()
		w.Header().Set("Content-Security-Policy", "default-src 'self'; "+
			"script-src 'nonce-"+nonce+"'; "+
			"style-src 'nonce-"+nonce+"'; "+
			"object-src 'none'; base-uri 'none'")
		next.ServeHTTP(w, r.WithContext(gsx.WithNonce(r.Context(), nonce)))
	})
}
```

Wrap your router once, then render with the request context:

```go
mux := http.NewServeMux()
mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
	ui.Home().Render(r.Context(), w)
})
http.ListenAndServe(":8080", app.CSP(mux))
```

Inline and external scripts, `<style>` blocks, stylesheet and preload links,
and JSON data islands now carry the nonce, so `script-src` and `style-src` need
no host list.

## Adjust the policy

Add only what the page uses:

| The page uses | Add |
| --- | --- |
| `style` attributes, including `style={...}` | `style-src-attr 'unsafe-inline'` |
| Alpine or htmx expressions (`@click`, `x-data`, `hx-on`) | Alpine's CSP build, or `'unsafe-eval'` in `script-src` |
| Native handler attributes (`onclick`) | Move the code into a nonced `<script>` |
| Scripts or stylesheets that JavaScript inserts | `'self'` or their host in `script-src` / `style-src` |

Markup that gsx does not render, such as an HTML template or a third-party
widget, can read the value with `gsx.NonceFromContext(ctx)`.

See [CSP nonces](../syntax/escaping.md#csp-nonces) for how an authored
`nonce` attribute overrides the automatic one.
