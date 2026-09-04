package shorturl

import (
	"io"
	"log/slog"
	"testing"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

func TestResolvePath(t *testing.T) {
	rules := []PathRule{
		{ID: "a", Pattern: `^broken(`, Destination: "https://never/"},
		{ID: "b", Pattern: `^exact\.zip$`, Destination: "https://files.example.org/exact.zip"},
		{ID: "c", Pattern: `^docs/(?<page>[^?]+)(\?(?<qs>.*))?$`, Destination: "https://docs.example.org/${page}?${qs}"},
		{ID: "d", Pattern: `^(?P<rest>.*)$`, Destination: "https://fallback.example.org/${rest}"},
	}

	dest, rule, ok := ResolvePath(quiet, rules, "docs/intro")
	if !ok || rule.ID != "c" || dest != "https://docs.example.org/intro?" {
		t.Errorf("docs/intro: got %q rule %q ok %v", dest, rule.ID, ok)
	}

	dest, rule, ok = ResolvePath(quiet, rules, "docs/intro?v=2")
	if !ok || rule.ID != "c" || dest != "https://docs.example.org/intro?v=2" {
		t.Errorf("docs/intro?v=2: got %q rule %q ok %v", dest, rule.ID, ok)
	}

	dest, rule, ok = ResolvePath(quiet, rules, "exact.zip")
	if !ok || rule.ID != "b" || dest != "https://files.example.org/exact.zip" {
		t.Errorf("rule without named groups should match verbatim: got %q rule %q ok %v", dest, rule.ID, ok)
	}

	dest, rule, ok = ResolvePath(quiet, rules, "other/thing")
	if !ok || rule.ID != "d" || dest != "https://fallback.example.org/other/thing" {
		t.Errorf("fallthrough: got %q rule %q ok %v", dest, rule.ID, ok)
	}

	_, _, ok = ResolvePath(quiet, rules[:2], "anything")
	if ok {
		t.Error("no matching rule should report ok=false")
	}
}
