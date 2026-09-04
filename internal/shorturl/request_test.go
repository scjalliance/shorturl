package shorturl

import (
	"net/http/httptest"
	"testing"
)

func TestParseRequest(t *testing.T) {
	tests := []struct {
		name     string
		host     string
		uri      string
		override string
		want     Request
	}{
		{
			name: "plain slug",
			host: "example.com", uri: "/Demo",
			want: Request{Hostname: "example.com", URL: "/Demo", Slug: "demo", Remainder: "/Demo"},
		},
		{
			name: "query is split off and slug stops at ?",
			host: "example.com", uri: "/demo?x=1&y=2",
			want: Request{Hostname: "example.com", URL: "/demo?x=1&y=2", Slug: "demo", Query: "x=1&y=2", Remainder: "/demo?x=1&y=2"},
		},
		{
			name: "remainder drops slug when a second slash exists",
			host: "example.com", uri: "/docs/guide/intro?v=2",
			want: Request{Hostname: "example.com", URL: "/docs/guide/intro?v=2", Slug: "docs", Query: "v=2", Remainder: "guide/intro?v=2"},
		},
		{
			name: "qr path prefix collapses and marks ViaQR",
			host: "example.com", uri: "/qr/demo",
			want: Request{Hostname: "example.com", URL: "/demo", ViaQR: true, Slug: "demo", Remainder: "/demo"},
		},
		{
			name: "repeated slashes around qr collapse too",
			host: "example.com", uri: "//qr//demo",
			want: Request{Hostname: "example.com", URL: "/demo", ViaQR: true, Slug: "demo", Remainder: "/demo"},
		},
		{
			name: "qr host is stripped and flagged",
			host: "qr.example.com", uri: "/demo",
			want: Request{Hostname: "example.com", QRHost: true, URL: "/demo", Slug: "demo", Remainder: "/demo"},
		},
		{
			name: "override replaces hostname but qr flag still comes from the real host",
			host: "qr.preview.test", uri: "/demo", override: "example.com",
			want: Request{Hostname: "example.com", QRHost: true, URL: "/demo", Slug: "demo", Remainder: "/demo"},
		},
		{
			name: "root has empty slug",
			host: "example.com", uri: "/",
			want: Request{Hostname: "example.com", URL: "/", Remainder: "/"},
		},
		{
			name: "qr root has empty slug",
			host: "example.com", uri: "/qr/",
			want: Request{Hostname: "example.com", URL: "/", ViaQR: true, Remainder: "/"},
		},
		{
			name: "double leading slash has empty slug",
			host: "example.com", uri: "//foo",
			want: Request{Hostname: "example.com", URL: "//foo", Remainder: "//foo"},
		},
		{
			name: "port is removed from host",
			host: "example.com:8080", uri: "/demo",
			want: Request{Hostname: "example.com", URL: "/demo", Slug: "demo", Remainder: "/demo"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "http://placeholder"+tt.uri, nil)
			r.Host = tt.host
			got := ParseRequest(r, tt.override)
			if got != tt.want {
				t.Errorf("ParseRequest(%q %q)\n got %+v\nwant %+v", tt.host, tt.uri, got, tt.want)
			}
		})
	}
}

func TestParseRequestPrefersForwardedHost(t *testing.T) {
	r := httptest.NewRequest("GET", "http://placeholder/demo", nil)
	r.Host = "service-abc.a.run.app"
	r.Header.Set("X-Forwarded-Host", "qr.example.com")
	got := ParseRequest(r, "")
	if got.Hostname != "example.com" || !got.QRHost {
		t.Errorf("got %+v, want Hostname example.com and QRHost true", got)
	}
}
