// Package apidocs serves the OpenAPI specification and a Swagger UI for it.
//
// The specification in openapi.yaml is the committed source of truth for the
// HTTP API. It is hand-maintained; the drift test in internal/api walks the
// router and fails if the two disagree.
//
// The Swagger UI assets are vendored rather than pulled from a CDN because the
// service ships FROM scratch and is expected to run on a home lab host that may
// have no outbound internet — and because a page that handles credentials
// should not be loading executable JavaScript from a third party.
package apidocs

import (
	_ "embed"
	"net/http"

	"github.com/go-chi/chi/v5"
)

//go:embed openapi.yaml
var spec []byte

//go:embed index.html
var index []byte

//go:embed swagger-ui.css
var css []byte

//go:embed swagger-ui-bundle.js
var bundle []byte

// Spec returns the raw OpenAPI document.
func Spec() []byte { return spec }

// Mount registers the documentation routes on r.
//
// They are deliberately mounted outside the API token guard: the specification
// is documentation, not data. It exposes no balance and drives no state, and
// being able to read how to authenticate without first authenticating is the
// point of publishing it.
func Mount(r chi.Router) {
	r.Get("/api/docs", serve("text/html; charset=utf-8", index))
	r.Get("/api/docs/openapi.yaml", serve("application/yaml; charset=utf-8", spec))
	r.Get("/api/docs/swagger-ui.css", serve("text/css; charset=utf-8", css))
	r.Get("/api/docs/swagger-ui-bundle.js", serve("text/javascript; charset=utf-8", bundle))
}

func serve(contentType string, body []byte) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", contentType)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
	}
}
