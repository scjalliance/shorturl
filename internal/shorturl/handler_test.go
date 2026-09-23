package shorturl

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeStore is an in-memory Store that sums analytics counts.
type fakeStore struct {
	links  map[string]Link       // key host + "/" + slug
	rules  map[string][]PathRule // same key
	err    error
	addErr func(CounterDoc) error // optional AddCounts failure

	mu     sync.Mutex
	adds   int                         // AddCounts calls, failed ones included
	counts map[string]map[string]Delta // CounterDoc.String() -> name -> total
}

func key(host, slug string) string { return host + "/" + slug }

func (f *fakeStore) GetLink(_ context.Context, host, slug string) (Link, error) {
	if f.err != nil {
		return Link{}, f.err
	}
	l, ok := f.links[key(host, slug)]
	if !ok {
		return Link{}, ErrNotFound
	}
	return l, nil
}
func (f *fakeStore) ListPathRules(_ context.Context, host, slug string) ([]PathRule, error) {
	return f.rules[key(host, slug)], nil
}
func (f *fakeStore) AddCounts(_ context.Context, doc CounterDoc, deltas map[string]Delta) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.adds++
	if f.addErr != nil {
		if err := f.addErr(doc); err != nil {
			return err
		}
	}
	if f.counts == nil {
		f.counts = map[string]map[string]Delta{}
	}
	stored := f.counts[doc.String()]
	if stored == nil {
		stored = map[string]Delta{}
		f.counts[doc.String()] = stored
	}
	for name, d := range deltas {
		cur := stored[name]
		cur.N += d.N
		cur.Last = d.Last
		stored[name] = cur
	}
	return nil
}

// count returns the stored total for one counter on one document.
func (f *fakeStore) count(doc, name string) int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.counts[doc][name].N
}

// last returns the stored Last time for one counter on one document.
func (f *fakeStore) last(doc, name string) time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.counts[doc][name].Last
}

func newHandler(links map[string]Link) (*Handler, *fakeStore) {
	fs := &fakeStore{links: links, rules: map[string][]PathRule{}}
	return &Handler{Store: fs, Version: "test", Logger: quiet}, fs
}

func do(h *Handler, method, host, uri string, hdr map[string]string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "http://placeholder"+uri, nil)
	r.Host = host
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	// Tests assert on stored counts right after the request, so write out
	// whatever the interval left pending.
	h.FlushCounters(context.Background())
	return w
}

func TestRedirectDefaults(t *testing.T) {
	h, fs := newHandler(map[string]Link{"example.com/demo": {Destination: "https://example.org/x"}})
	w := do(h, "GET", "example.com", "/Demo?a=1", nil)
	if w.Code != 307 || w.Header().Get("Location") != "https://example.org/x" {
		t.Errorf("got %d %q", w.Code, w.Header().Get("Location"))
	}
	if got := fs.count("example.com/demo", "click"); got != 1 {
		t.Errorf("click count = %d, want 1", got)
	}
}

func TestRedirectStatusAndQueryString(t *testing.T) {
	h, _ := newHandler(map[string]Link{
		"example.com/a": {Destination: "https://example.org/x", StatusCode: 301, PassQueryString: true},
		"example.com/b": {Destination: "https://example.org/x?k=v", PassQueryString: true},
	})
	w := do(h, "GET", "example.com", "/a?q=1&r=2", nil)
	if w.Code != 301 || w.Header().Get("Location") != "https://example.org/x?q=1&r=2" {
		t.Errorf("a: got %d %q", w.Code, w.Header().Get("Location"))
	}
	w = do(h, "GET", "example.com", "/b?q=1", nil)
	if w.Header().Get("Location") != "https://example.org/x?k=v&q=1" {
		t.Errorf("b: got %q", w.Header().Get("Location"))
	}
	w = do(h, "GET", "example.com", "/b", nil)
	if w.Header().Get("Location") != "https://example.org/x?k=v" {
		t.Errorf("b without query: got %q", w.Header().Get("Location"))
	}
}

func TestNotFoundFlow(t *testing.T) {
	h, fs := newHandler(map[string]Link{})
	cases := map[string]string{
		"/missing?x=1": "/404/missing?x=1",
		"/404":         "/404.html",
		"/404/deep":    "/404.html",
		"/":            "/404/",
		"/qr/":         "/404/",
		"//foo":        "/404//foo",
		"/__x__":       "/404/__x__",
	}
	for uri, want := range cases {
		w := do(h, "GET", "example.com", uri, nil)
		if w.Code != 302 || w.Header().Get("Location") != want {
			t.Errorf("%s: got %d %q, want 302 %q", uri, w.Code, w.Header().Get("Location"), want)
		}
	}
	if len(fs.counts) != 0 {
		t.Errorf("not-found requests must not count: %v", fs.counts)
	}
}

func TestStoreErrorIs500(t *testing.T) {
	h, fs := newHandler(nil)
	fs.err = errors.New("boom")
	w := do(h, "GET", "example.com", "/demo", nil)
	if w.Code != 500 {
		t.Errorf("got %d, want 500", w.Code)
	}
}

func TestFrame(t *testing.T) {
	h, _ := newHandler(map[string]Link{"example.com/f": {Destination: "https://example.org/p?a=b", Frame: `Hi <"there">`}})
	w := do(h, "GET", "example.com", "/f", nil)
	body := w.Body.String()
	if w.Code != 200 || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/html") {
		t.Fatalf("got %d %q", w.Code, w.Header().Get("Content-Type"))
	}
	for _, want := range []string{
		"<title>Hi &lt;&#34;there&#34;&gt;</title>",
		`src="https://example.org/p?a=b"`,
		`replaceState(null,"","https://example.com/f")`,
		"</iframe>",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q:\n%s", want, body)
		}
	}
}

func TestQRHost(t *testing.T) {
	h, fs := newHandler(map[string]Link{"example.com/demo": {Destination: "https://example.org/"}})
	w := do(h, "GET", "qr.example.com", "/demo", nil)
	if w.Code != 200 || w.Header().Get("Content-Type") != "image/png" {
		t.Fatalf("got %d %q", w.Code, w.Header().Get("Content-Type"))
	}
	if got := decodeQR(t, w.Body.Bytes()); got != "https://example.com/qr/demo" {
		t.Errorf("QR code encodes %q, want the production /qr/ URL for the slug", got)
	}
	// A /qr/ path on the qr host encodes the collapsed URL, not /qr/qr/, and
	// keeps the query string.
	w = do(h, "GET", "qr.example.com", "/qr/demo?x=1", nil)
	if got := decodeQR(t, w.Body.Bytes()); got != "https://example.com/qr/demo?x=1" {
		t.Errorf("QR code for a /qr/ path encodes %q", got)
	}
	if fs.count("example.com/demo", "qrCreate") != 2 || fs.count("example.com/demo", "click") != 0 {
		t.Errorf("qr creation should count qrCreate only: %v", fs.counts)
	}
	// Missing slug on the qr host follows the normal 404 flow.
	w = do(h, "GET", "qr.example.com", "/nope", nil)
	if w.Code != 302 || w.Header().Get("Location") != "/404/nope" {
		t.Errorf("qr host miss: got %d %q", w.Code, w.Header().Get("Location"))
	}
}

func TestQRPathCountsScan(t *testing.T) {
	h, fs := newHandler(map[string]Link{"example.com/demo": {Destination: "https://example.org/"}})
	w := do(h, "GET", "example.com", "/qr/demo", nil)
	if w.Code != 307 || w.Header().Get("Location") != "https://example.org/" {
		t.Errorf("got %d %q", w.Code, w.Header().Get("Location"))
	}
	if fs.count("example.com/demo", "click") != 1 || fs.count("example.com/demo", "qrUse") != 1 {
		t.Errorf("a /qr/ visit counts click and qrUse: %v", fs.counts)
	}
}

func TestUsePaths(t *testing.T) {
	h, fs := newHandler(map[string]Link{"example.com/docs": {Destination: "https://docs.example.org/", UsePaths: true}})
	fs.rules["example.com/docs"] = []PathRule{
		{ID: "r1", Pattern: `^guide/(?<page>\w+)$`, Destination: "https://docs.example.org/g/${page}.html"},
	}
	w := do(h, "GET", "example.com", "/docs/guide/intro", nil)
	if w.Header().Get("Location") != "https://docs.example.org/g/intro.html" {
		t.Errorf("match: got %q", w.Header().Get("Location"))
	}
	if got := fs.count("example.com/docs/paths/r1", "match"); got != 1 {
		t.Errorf("match count = %d, want 1", got)
	}
	w = do(h, "GET", "example.com", "/docs/other", nil)
	if w.Header().Get("Location") != "https://docs.example.org/" {
		t.Errorf("fallback: got %q", w.Header().Get("Location"))
	}
}

func TestPassthrough(t *testing.T) {
	var seen http.Header
	var seenMethod, seenBody string
	var seenLength int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Clone()
		seenMethod = r.Method
		seenLength = r.ContentLength
		b, _ := io.ReadAll(r.Body)
		seenBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Secret", "no")
		if r.URL.Query().Get("fail") == "1" {
			w.WriteHeader(418)
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()

	h, _ := newHandler(map[string]Link{
		"example.com/p":   {Destination: upstream.URL + "/api", Passthrough: true, PassQueryString: true},
		"example.com/any": {Destination: upstream.URL + "/api?fail=1", Passthrough: true, PassthroughAnyStatus: true},
	})

	r := httptest.NewRequest("POST", "http://placeholder/p?x=1", strings.NewReader("payload"))
	r.Host = "example.com"
	r.Header.Set("Authorization", "Bearer t")
	r.Header.Set("X-Forwarded-For", "203.0.113.9")
	r.Header.Set("Cookie", "secret=1")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	if w.Code != 200 || w.Body.String() != `{"ok":true}` {
		t.Fatalf("got %d %q", w.Code, w.Body.String())
	}
	if w.Header().Get("Content-Type") != "application/json" || w.Header().Get("X-Secret") != "" {
		t.Errorf("response headers not filtered: %v", w.Header())
	}
	if w.Header().Get("X-ShortUrl-Ver") != "test" {
		t.Errorf("missing version header")
	}
	if seenMethod != "POST" || seenBody != "payload" {
		t.Errorf("method/body not forwarded: %s %q", seenMethod, seenBody)
	}
	if seenLength != int64(len("payload")) {
		t.Errorf("upstream Content-Length = %d, want %d", seenLength, len("payload"))
	}
	for k, want := range map[string]string{
		"Authorization":        "Bearer t",
		"X-Forwarded-For":      "203.0.113.9",
		"X-Passthrough-Domain": "example.com",
		"X-Passthrough-Slug":   "p",
		"X-Shorturl-Ver":       "test",
		"Accept":               "*/*",
		"Cookie":               "",
	} {
		if got := seen.Get(k); got != want {
			t.Errorf("upstream %s = %q, want %q", k, got, want)
		}
	}
	if !strings.HasSuffix(seen.Get("X-Passthrough-Slug"), "p") {
		t.Errorf("slug header")
	}

	w = do(h, "GET", "example.com", "/p?fail=1", nil)
	if w.Code != 500 {
		t.Errorf("non-2xx upstream should be 500, got %d", w.Code)
	}
	w = do(h, "GET", "example.com", "/any", nil)
	if w.Code != 418 {
		t.Errorf("passthroughAnyStatus should relay 418, got %d", w.Code)
	}
}

// TestPassthroughEmptyPost mirrors a Google push notification: a POST with
// no body. The upstream must see Content-Length: 0, not a chunked stream.
func TestPassthroughEmptyPost(t *testing.T) {
	var seenLength int64 = -2
	var seenTE []string
	var seenState string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenLength = r.ContentLength
		seenTE = r.TransferEncoding
		seenState = r.Header.Get("X-Goog-Resource-State")
		w.WriteHeader(200)
	}))
	defer upstream.Close()
	h, _ := newHandler(map[string]Link{"example.com/push": {Destination: upstream.URL + "/hook", Passthrough: true, PassthroughAnyStatus: true}})
	r := httptest.NewRequest("POST", "http://placeholder/push", nil)
	r.Host = "example.com"
	r.Header.Set("X-Goog-Resource-State", "sync")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("got %d", w.Code)
	}
	if seenLength != 0 || len(seenTE) != 0 {
		t.Errorf("upstream saw Content-Length %d, Transfer-Encoding %v; want 0 and none", seenLength, seenTE)
	}
	if seenState != "sync" {
		t.Errorf("X-Goog-Resource-State not forwarded: %q", seenState)
	}
}

func TestHostAlias(t *testing.T) {
	h, fs := newHandler(map[string]Link{"example.com/demo": {Destination: "https://example.org/"}})
	h.HostAliases = map[string]string{"www.example.com": "example.com"}
	w := do(h, "GET", "www.example.com", "/demo", nil)
	if w.Code != 307 || w.Header().Get("Location") != "https://example.org/" {
		t.Errorf("aliased host: got %d %q", w.Code, w.Header().Get("Location"))
	}
	if got := fs.count("example.com/demo", "click"); got != 1 {
		t.Errorf("click should land on the aliased collection: %v", fs.counts)
	}
	// The QR host form of an aliased host also resolves.
	w = do(h, "GET", "qr.www.example.com", "/demo", nil)
	if w.Code != 200 || w.Header().Get("Content-Type") != "image/png" {
		t.Errorf("qr on aliased host: got %d %q", w.Code, w.Header().Get("Content-Type"))
	}
	// Unaliased hosts are untouched.
	w = do(h, "GET", "other.example.com", "/demo", nil)
	if w.Code != 302 || w.Header().Get("Location") != "/404/demo" {
		t.Errorf("unaliased host: got %d %q", w.Code, w.Header().Get("Location"))
	}
}

// TestPassthroughBodyReplay checks that a buffered body survives a 307 and
// a 308 from the upstream. Both need GetBody, which the HTTP/2 GOAWAY retry
// also uses.
func TestPassthroughBodyReplay(t *testing.T) {
	var seenBody, seenMethod string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/307":
			http.Redirect(w, r, "/308", http.StatusTemporaryRedirect)
		case "/308":
			http.Redirect(w, r, "/final", http.StatusPermanentRedirect)
		default:
			b, _ := io.ReadAll(r.Body)
			seenBody, seenMethod = string(b), r.Method
			w.WriteHeader(200)
		}
	}))
	defer upstream.Close()
	h, _ := newHandler(map[string]Link{"example.com/p": {Destination: upstream.URL + "/307", Passthrough: true}})
	r := httptest.NewRequest("PUT", "http://placeholder/p", strings.NewReader("payload"))
	r.Host = "example.com"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("got %d", w.Code)
	}
	if seenMethod != "PUT" || seenBody != "payload" {
		t.Errorf("after redirects upstream saw %s %q, want PUT %q", seenMethod, seenBody, "payload")
	}
}

func TestPassthroughBodyTooLarge(t *testing.T) {
	called := false
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	defer upstream.Close()
	h, _ := newHandler(map[string]Link{"example.com/p": {Destination: upstream.URL, Passthrough: true}})
	r := httptest.NewRequest("POST", "http://placeholder/p", strings.NewReader(strings.Repeat("x", maxPassthroughBody+1)))
	r.Host = "example.com"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("got %d, want 413", w.Code)
	}
	if called {
		t.Errorf("upstream was called for an oversized body")
	}
}
