package app

import (
	"log/slog"
	"net/http"
	"strings"

	"github.com/moriyoshi/oganesson/runtime/imageopt"
)

// imagesHandler serves Next's image optimizer in front of the application,
// as next-server's handleNextImageRequest does.
type imagesHandler struct {
	path   string
	images *imageopt.Handler
	next   http.Handler
}

func withImages(next http.Handler, meta *Meta, logger *slog.Logger) http.Handler {
	// The optimizer answers at /_next/image under basePath whatever
	// images.path is: that is the loader's URL, which next.config's loading
	// already prefixes with basePath (router-server strips basePath, then
	// next-server matches /_next/image).
	return imagesHandler{
		path: strings.TrimSuffix(meta.BasePath, "/") + "/_next/image",
		next: next,
		images: &imageopt.Handler{
			Config: *meta.Images, BasePath: meta.BasePath,
			// Local images come from the application itself; with the default
			// loader disabled, the request falls through to it (and 404s).
			App: next, Fallback: next, Logger: logger,
		},
	}
}

func (h imagesHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, h.path) {
		h.images.ServeHTTP(w, r)
		return
	}
	h.next.ServeHTTP(w, r)
}

// unwrap returns the static handler beneath optional wrappers.
func unwrap(h http.Handler) staticHandler {
	if ih, ok := h.(imagesHandler); ok {
		h = ih.next
	}
	return h.(staticHandler)
}
