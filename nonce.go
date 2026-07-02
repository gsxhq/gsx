package gsx

import "context"

// nonceKey is the context key for the per-request CSP nonce (see WithNonce).
type nonceKey struct{}

// WithNonce returns a context carrying the per-request CSP nonce. Generated
// code adds nonce="<value>" to every <script> and <style> open tag rendered
// with the returned context; an author-written nonce attribute (or a spread
// bag carrying a "nonce" key) wins and suppresses the automatic one. gsx does
// not generate nonce values and does not build the Content-Security-Policy
// header — both remain the server's job.
func WithNonce(ctx context.Context, nonce string) context.Context {
	return context.WithValue(ctx, nonceKey{}, nonce)
}

// NonceFromContext returns the nonce stored by WithNonce, or "" when absent.
func NonceFromContext(ctx context.Context) string {
	nonce, _ := ctx.Value(nonceKey{}).(string)
	return nonce
}

// Nonce writes ` nonce="<value>"` (attribute-escaped) when ctx carries a
// non-empty nonce (WithNonce), and nothing otherwise. Generated code calls it
// at the end of every <script>/<style> open tag that has no author-written
// nonce attribute.
func (gw *Writer) Nonce(ctx context.Context) {
	if gw.err != nil {
		return
	}
	nonce := NonceFromContext(ctx)
	if nonce == "" {
		return
	}
	gw.writeStr(` nonce="`)
	gw.AttrValue(nonce)
	gw.writeStr(`"`)
}
