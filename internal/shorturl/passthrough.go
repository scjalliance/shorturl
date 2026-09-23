package shorturl

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"

	"golang.org/x/sync/semaphore"
)

// maxPassthroughBody caps one buffered request body at 10 MiB, about the
// 10 MB request limit of the Cloud Function this service replaced.
const maxPassthroughBody = 10 << 20

// passthroughBodyBudget bounds the request bodies buffered at once across
// the instance, so concurrent large bodies cannot exhaust its memory. A
// request that does not fit gets a 503.
var passthroughBodyBudget = semaphore.NewWeighted(64 << 20)

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
	w.Header().Set("X-ShortUrl-Ver", h.Version)

	// The body is buffered so the request has a GetBody. The client needs it
	// to follow a 307 or 308 and to retry after an HTTP/2 GOAWAY.
	var body io.Reader
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		weight := int64(maxPassthroughBody)
		if r.ContentLength > maxPassthroughBody {
			h.logger().Warn("passthrough body too large", "destination", destination, "length", r.ContentLength)
			http.Error(w, "Request Entity Too Large", http.StatusRequestEntityTooLarge)
			return
		} else if r.ContentLength >= 0 {
			weight = r.ContentLength
		}
		if !passthroughBodyBudget.TryAcquire(weight) {
			h.logger().Warn("passthrough body budget exhausted", "destination", destination, "length", r.ContentLength)
			w.Header().Set("Retry-After", "1")
			http.Error(w, "Service Unavailable", http.StatusServiceUnavailable)
			return
		}
		b, err := readPassthroughBody(w, r)
		// A chunked body reserved the full cap; return what it did not use.
		if n := int64(len(b)); err == nil && n < weight {
			passthroughBodyBudget.Release(weight - n)
			weight = n
		}
		// Held until return: res.Request.GetBody keeps the buffer reachable
		// while the response streams, for at most the 30 s client timeout.
		defer passthroughBodyBudget.Release(weight)
		if err != nil {
			h.logger().Warn("passthrough read body", "destination", destination, "err", err)
			if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
				http.Error(w, "Request Entity Too Large", http.StatusRequestEntityTooLarge)
				return
			}
			http.Error(w, "Bad Request", http.StatusBadRequest)
			return
		}
		body = bytes.NewReader(b)
	}
	up, err := http.NewRequestWithContext(ctx, r.Method, destination, body)
	if err != nil {
		h.logger().Warn("passthrough request", "destination", destination, "err", err)
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
		h.logger().Warn("passthrough upstream", "destination", destination, "err", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	defer res.Body.Close()

	if (res.StatusCode < 200 || res.StatusCode > 299) && !link.PassthroughAnyStatus {
		h.logger().Warn("passthrough upstream status", "destination", destination, "status", res.StatusCode)
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
		h.logger().Warn("passthrough copy", "destination", destination, "err", err)
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

// readPassthroughBody reads the visitor's body, which must be at most
// maxPassthroughBody bytes. A known length is read into one allocation.
func readPassthroughBody(w http.ResponseWriter, r *http.Request) ([]byte, error) {
	if r.ContentLength >= 0 {
		b := make([]byte, r.ContentLength)
		_, err := io.ReadFull(r.Body, b)
		return b, err
	}
	return io.ReadAll(http.MaxBytesReader(w, r.Body, maxPassthroughBody))
}
