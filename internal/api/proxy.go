package api

import (
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/ricardo1slas/private_relayer/internal/relay"
)

// proxyHandler serves /api/v1/proxy/{target}/*. The upstream path is cut from the *escaped*
// request path, not chi's wildcard, so encodings like %2e reach the policy check exactly as sent.
func proxyHandler(rh *relay.Handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name := chi.URLParam(r, "target")
		_, rest, ok := strings.Cut(r.URL.EscapedPath(), "/proxy/"+name+"/")
		if !ok || name == "" {
			writeError(w, http.StatusBadRequest, "malformed proxy path")
			return
		}
		rh.Proxy(w, r, name, "/"+rest)
	}
}
