package shorturl

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeStore is an in-memory Store that records analytics calls.
type fakeStore struct {
	links  map[string]Link       // key host + "/" + slug
	rules  map[string][]PathRule // same key
	err    error
	clicks []string // "host/slug" or "host/slug qr"
	qrs    []string
	paths  []string // "host/slug/ruleID"
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
func (f *fakeStore) RecordClick(_ context.Context, host, slug string, viaQR bool) error {
	k := key(host, slug)
	if viaQR {
		k += " qr"
	}
	f.clicks = append(f.clicks, k)
	return nil
}
func (f *fakeStore) RecordQRCreate(_ context.Context, host, slug string) error {
	f.qrs = append(f.qrs, key(host, slug))
	return nil
}
func (f *fakeStore) RecordPathMatch(_ context.Context, host, slug, ruleID string) error {
	f.paths = append(f.paths, key(host, slug)+"/"+ruleID)
	return nil
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
	return w
}

func TestRedirectDefaults(t *testing.T) {
	h, fs := newHandler(map[string]Link{"example.com/demo": {Destination: "https://example.org/x"}})
	w := do(h, "GET", "example.com", "/Demo?a=1", nil)
	if w.Code != 307 || w.Header().Get("Location") != "https://example.org/x" {
		t.Errorf("got %d %q", w.Code, w.Header().Get("Location"))
	}
	if len(fs.clicks) != 1 || fs.clicks[0] != "example.com/demo" {
		t.Errorf("clicks = %v", fs.clicks)
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
	if len(fs.clicks) != 0 {
		t.Errorf("not-found requests must not count clicks: %v", fs.clicks)
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
	if len(fs.qrs) != 1 || len(fs.clicks) != 0 {
		t.Errorf("qr creation should count qrCreate only: qrs=%v clicks=%v", fs.qrs, fs.clicks)
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
	if len(fs.clicks) != 1 || fs.clicks[0] != "example.com/demo qr" {
		t.Errorf("clicks = %v", fs.clicks)
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
	if len(fs.paths) != 1 || fs.paths[0] != "example.com/docs/r1" {
		t.Errorf("paths = %v", fs.paths)
	}
	w = do(h, "GET", "example.com", "/docs/other", nil)
	if w.Header().Get("Location") != "https://docs.example.org/" {
		t.Errorf("fallback: got %q", w.Header().Get("Location"))
	}
}

func TestPassthrough(t *testing.T) {
	var seen http.Header
	var seenMethod, seenBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Clone()
		seenMethod = r.Method
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
