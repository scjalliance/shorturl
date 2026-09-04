package shorturl

import (
	"context"
	"io"
	"net"
	"net/http"
	"strings"
)

// passthroughRequestHeaders are copied from the visitor's request to the
// upstream request when present. The X-Goog-* headers let Google push
// notifications be proxied to a destination.
var passthroughRequestHeaders = []string{
	"Accept-Encoding",
	"Accept-Language",
	"Cache-Control",
	"Pragma",
	"Authorization",
	"User-Agent",
	"Content-Type",
	"X-Goog-Channel-ID",
	"X-Goog-Channel-Token",
	"X-Goog-Channel-Expiration",
	"X-Goog-Resource-ID",
	"X-Goog-Resource-URI",
	"X-Goog-Resource-State",
	"X-Goog-Message-Number",
}

// passthroughResponseHeaders are copied from the upstream response to the
// visitor. Content-Encoding is included because the body is streamed as
// received; when the visitor sent no Accept-Encoding the Go transport
// decompresses and removes the header itself.
var passthroughResponseHeaders = []string{
	"Access-Control-Allow-Origin",
	"Cache-Control",
	"Content-Encoding",
	"Content-Type",
	"Pragma",
}

// passthrough proxies the visitor's request to destination and streams the
// response back. Non-2xx upstream responses become a 500 unless the link
// sets passthroughAnyStatus.
func (h *Handler) passthrough(ctx context.Context, w http.ResponseWriter, r *http.Request, req Request, link Link, destination string) {
	var body io.Reader
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		body = r.Body
	}
	up, err := http.NewRequestWithContext(ctx, r.Method, destination, body)
	if err != nil {
		h.Logger.Warn("passthrough request", "destination", destination, "err", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	up.Header.Set("Accept", "*/*")
	if v := r.Header.Get("Accept"); v != "" {
		up.Header.Set("Accept", v)
	}
	for _, name := range passthroughRequestHeaders {
		if v := r.Header.Get(name); v != "" {
			up.Header.Set(name, v)
		}
	}
	up.Header.Set("X-Forwarded-For", clientIP(r))
	up.Header.Set("X-Passthrough-Domain", req.Hostname)
	up.Header.Set("X-Passthrough-Slug", req.Slug)
	up.Header.Set("X-ShortUrl-Ver", h.Version)

	res, err := h.client().Do(up)
	if err != nil {
		h.Logger.Warn("passthrough upstream", "destination", destination, "err", err)
		w.Header().Set("X-ShortUrl-Ver", h.Version)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	defer res.Body.Close()

	w.Header().Set("X-ShortUrl-Ver", h.Version)
	if (res.StatusCode < 200 || res.StatusCode > 299) && !link.PassthroughAnyStatus {
		h.Logger.Warn("passthrough upstream status", "destination", destination, "status", res.StatusCode)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	for _, name := range passthroughResponseHeaders {
		if v := res.Header.Get(name); v != "" {
			w.Header().Set(name, v)
		}
	}
	w.WriteHeader(res.StatusCode)
	if _, err := io.Copy(w, res.Body); err != nil {
		h.Logger.Warn("passthrough copy", "destination", destination, "err", err)
	}
}

// clientIP returns the visitor's address: the incoming X-Forwarded-For when
// present, otherwise the connection's remote address.
func clientIP(r *http.Request) string {
	if v := r.Header.Get("X-Forwarded-For"); v != "" {
		return v
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return strings.TrimSpace(r.RemoteAddr)
	}
	return host
}
