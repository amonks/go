package datagrid

import (
	"context"
	"html"

	"github.com/a-h/templ"
)

// nonceAttr is the request's CSP nonce as a script attribute when the
// context carries one (templ.WithNonce; serve.Mux stamps the proxy's),
// so on a page that renders the per-request nonce the proxy's script
// policy runs this library's script, which it would otherwise refuse
// ([proxy](../../specs/proxy/index.md) § Policy violation reports).
func nonceAttr(ctx context.Context) string {
	nonce := templ.GetNonce(ctx)
	if nonce == "" {
		return ""
	}
	return ` nonce="` + html.EscapeString(nonce) + `"`
}
