package shorturl

import (
	"fmt"
	"net"
	"net/http"
	"regexp"
	"strings"
)

// Request is the parsed form of one incoming HTTP request. Every field is
// derived exactly the way the original Cloud Function derived it so that
// Firestore lookups and analytics land on the same documents.
type Request struct {
	// Hostname is the Firestore collection to look in: the original request
	// host with a leading "qr." removed, unless an override was supplied.
	Hostname string
	// QRHost is true when the request arrived on a "qr." subdomain. Such a
	// request returns a QR code image instead of following the link.
	QRHost bool
	// URL is the raw request path and query with a leading "/qr/" collapsed
	// to "/".
	URL string
	// ViaQR is true when the URL carried a "/qr/" prefix, meaning the visitor
	// scanned a QR code.
	ViaQR bool
	// Slug is the lowercased first path segment of URL. It is the Firestore
	// document ID. It is empty for "/" and for URLs whose first segment is
	// empty, such as "//foo".
	Slug string
	// Query is everything after the first "?" in URL, without the "?".
	Query string
	// Remainder is URL with the leading "/<slug>/" removed. Path rules are
	// matched against it. When URL has no second slash it is URL unchanged,
	// leading slash and query string included.
	Remainder string
}

var (
	qrPathPrefix    = regexp.MustCompile(`^/+qr/+`)
	slugAndSlash    = regexp.MustCompile(`^/[^/]+/`)
	qrHostPrefix    = regexp.MustCompile(`^qr\.`)
	slugTerminators = "/?"
)

// ParseRequest derives the routing fields from r. hostOverride, when not
// empty, replaces the request host as the collection name; it exists so a
// deployment on a non-production hostname can serve production links.
func ParseRequest(r *http.Request, hostOverride string) Request {
	requestHost := originalHost(r)
	hostname := qrHostPrefix.ReplaceAllString(requestHost, "")
	var req Request
	req.QRHost = hostname != requestHost
	req.Hostname = hostname
	if hostOverride != "" {
		req.Hostname = hostOverride
	}

	raw := r.URL.RequestURI()
	req.URL = qrPathPrefix.ReplaceAllString(raw, "/")
	req.ViaQR = req.URL != raw

	slug := strings.TrimPrefix(req.URL, "/")
	if i := strings.IndexAny(slug, slugTerminators); i >= 0 {
		slug = slug[:i]
	}
	req.Slug = strings.ToLower(slug)

	if i := strings.Index(req.URL, "?"); i >= 0 {
		req.Query = req.URL[i+1:]
	}

	req.Remainder = slugAndSlash.ReplaceAllString(req.URL, "")
	return req
}

// originalHost returns the host the visitor typed, preferring the
// X-Forwarded-Host header that Firebase Hosting sets, with any port removed.
func originalHost(r *http.Request) string {
	host := r.Header.Get("X-Forwarded-Host")
	if host == "" {
		host = r.Host
	}
	if i := strings.Index(host, ","); i >= 0 {
		host = host[:i]
	}
	host = strings.TrimSpace(host)
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	return host
}

// ParseHostAliases parses the SHORTURL_HOST_ALIASES format: comma separated
// "host:collection" pairs, for example "www.example.com:example.com". Blank
// entries are ignored; a malformed entry is an error.
func ParseHostAliases(s string) (map[string]string, error) {
	aliases := map[string]string{}
	for _, pair := range strings.Split(s, ",") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		from, to, ok := strings.Cut(pair, ":")
		from, to = strings.TrimSpace(from), strings.TrimSpace(to)
		if !ok || from == "" || to == "" {
			return nil, fmt.Errorf("host alias %q is not host:collection", pair)
		}
		aliases[from] = to
	}
	return aliases, nil
}
