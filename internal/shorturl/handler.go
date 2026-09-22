package shorturl

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Handler serves short links. All fields except Store are optional.
type Handler struct {
	Store Store
	// Version is reported in the X-ShortUrl-Ver header on passthrough
	// responses.
	Version string
	// HostOverride, when set, replaces the request host as the Firestore
	// collection name. It maps to the SHORTURL_HOSTNAME environment variable.
	HostOverride string
	// HostAliases maps a request host to the collection that serves it, for
	// example "www.example.com" to "example.com". It maps to the
	// SHORTURL_HOST_ALIASES environment variable. Applied after HostOverride.
	HostAliases map[string]string
	// Client performs passthrough requests. Defaults to a client with a 30s
	// timeout.
	Client *http.Client
	// Logger receives operational logs. Defaults to slog.Default().
	Logger *slog.Logger
	// CounterInterval is the minimum time between analytics flushes on one
	// instance. Defaults to 5s.
	CounterInterval time.Duration
	// CounterTimeout bounds each analytics write. Defaults to 1s.
	CounterTimeout time.Duration

	countersOnce sync.Once
	ctr          *counters
}

const defaultRedirectStatus = http.StatusTemporaryRedirect

// ServeHTTP routes one request: QR image, passthrough, path rule, frame, or
// redirect, with a 404 flow for unknown slugs.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	// Start any due analytics flush now so it overlaps this request's work,
	// and wait for it before returning, while the instance still has CPU.
	defer h.counters().start(ctx)()
	req := ParseRequest(r, h.HostOverride)
	if alias, ok := h.HostAliases[req.Hostname]; ok {
		req.Hostname = alias
	}

	if !ValidID(req.Hostname) || !ValidID(req.Slug) {
		h.notFound(w, req)
		return
	}
	link, err := h.Store.GetLink(ctx, req.Hostname, req.Slug)
	if errors.Is(err, ErrNotFound) {
		h.notFound(w, req)
		return
	}
	if err != nil {
		h.logger().Error("get link", "host", req.Hostname, "slug", req.Slug, "err", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	if req.QRHost && req.Slug != "404" {
		h.counters().add(CounterDoc{Host: req.Hostname, Slug: req.Slug}, counterQRCreate)
		png, err := QRPNG("https://" + req.Hostname + "/qr" + req.URL)
		if err != nil {
			h.logger().Error("qr render", "host", req.Hostname, "slug", req.Slug, "err", err)
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Cache-Control", "public, max-age=86400")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(png)
		return
	}

	if req.ViaQR {
		h.counters().add(CounterDoc{Host: req.Hostname, Slug: req.Slug}, counterClick, counterQRUse)
	} else {
		h.counters().add(CounterDoc{Host: req.Hostname, Slug: req.Slug}, counterClick)
	}

	switch {
	case link.Passthrough:
		h.passthrough(ctx, w, r, req, link, withQuery(link, req, link.Destination))
	case link.UsePaths:
		rules, err := h.Store.ListPathRules(ctx, req.Hostname, req.Slug)
		if err != nil {
			h.logger().Error("list path rules", "host", req.Hostname, "slug", req.Slug, "err", err)
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			return
		}
		destination := link.Destination
		if dest, rule, ok := ResolvePath(h.logger(), rules, req.Remainder); ok {
			h.counters().add(CounterDoc{Host: req.Hostname, Slug: req.Slug, Rule: rule.ID}, counterMatch)
			destination = dest
		}
		h.redirect(w, req, link, destination)
	default:
		h.redirect(w, req, link, link.Destination)
	}
}

// redirect sends the visitor to destination as a frame page or a Location
// redirect. An empty destination is treated as not found.
func (h *Handler) redirect(w http.ResponseWriter, req Request, link Link, destination string) {
	if destination == "" {
		h.notFound(w, req)
		return
	}
	destination = withQuery(link, req, destination)
	if link.Frame != "" {
		var buf bytes.Buffer
		if err := writeFrame(&buf, link.Frame, "https://"+req.Hostname+req.URL, destination); err != nil {
			h.logger().Error("frame render", "host", req.Hostname, "slug", req.Slug, "err", err)
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = buf.WriteTo(w)
		return
	}
	status := defaultRedirectStatus
	if link.StatusCode != 0 {
		status = link.StatusCode
	}
	location(w, status, destination)
}

// notFound implements the 404 flow: a missing "404" slug goes to the static
// /404.html page; any other missing slug is retried under the "404" slug so
// a per-host 404 link can decide where to send the visitor.
func (h *Handler) notFound(w http.ResponseWriter, req Request) {
	if req.Slug == "404" {
		location(w, http.StatusFound, "/404.html")
		return
	}
	location(w, http.StatusFound, "/404"+req.URL)
}

// withQuery appends the visitor's query string when the link asks for it.
func withQuery(link Link, req Request, destination string) string {
	if !link.PassQueryString || req.Query == "" {
		return destination
	}
	sep := "?"
	if strings.Contains(destination, "?") {
		sep = "&"
	}
	return destination + sep + req.Query
}

// location writes a redirect without resolving destination against the
// request path, matching Express's res.redirect.
func location(w http.ResponseWriter, status int, destination string) {
	w.Header().Set("Location", destination)
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(http.StatusText(status) + ". Redirecting to " + destination + "\n"))
}

// FlushCounters writes every pending analytics count. Call it at shutdown
// after the HTTP server has stopped, so counts batched since the last flush
// are not lost when the instance exits.
func (h *Handler) FlushCounters(ctx context.Context) {
	h.counters().flush(ctx)
}

// counters returns the handler's analytics batcher, building it from the
// handler's settings on first use.
func (h *Handler) counters() *counters {
	h.countersOnce.Do(func() {
		h.ctr = &counters{
			store:    h.Store,
			logger:   h.logger(),
			interval: cmp.Or(h.CounterInterval, defaultCounterInterval),
			timeout:  cmp.Or(h.CounterTimeout, defaultCounterTimeout),
			now:      time.Now,
		}
	})
	return h.ctr
}

func (h *Handler) logger() *slog.Logger {
	if h.Logger != nil {
		return h.Logger
	}
	return slog.Default()
}

func (h *Handler) client() *http.Client {
	if h.Client != nil {
		return h.Client
	}
	return &http.Client{Timeout: 30 * time.Second}
}
