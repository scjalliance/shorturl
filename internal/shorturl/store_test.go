package shorturl

import "testing"

func TestLinkFromMapUsesJavaScriptTruthiness(t *testing.T) {
	l := LinkFromMap(map[string]any{
		"destination":     "https://example.org/",
		"frame":           "Title",
		"passthrough":     "yes",
		"passQueryString": int64(1),
		"usePaths":        false,
		"statusCode":      float64(301),
	})
	want := Link{Destination: "https://example.org/", Frame: "Title", Passthrough: true, PassQueryString: true, StatusCode: 301}
	if l != want {
		t.Errorf("got %+v, want %+v", l, want)
	}
}

func TestLinkFromMapIgnoresFalsyFrameAndBadStatus(t *testing.T) {
	l := LinkFromMap(map[string]any{"frame": "", "statusCode": "301"})
	if l.Frame != "" || l.StatusCode != 0 {
		t.Errorf("got %+v, want empty Frame and zero StatusCode", l)
	}
	l = LinkFromMap(map[string]any{"statusCode": int64(999)})
	if l.StatusCode != 0 {
		t.Errorf("out of range status accepted: %+v", l)
	}
	l = LinkFromMap(map[string]any{"statusCode": float64(302.5)})
	if l.StatusCode != 0 {
		t.Errorf("fractional status accepted: %+v", l)
	}
}

func TestValidID(t *testing.T) {
	for _, bad := range []string{"", ".", "..", "a/b", "__x__", string(make([]byte, 1501))} {
		if ValidID(bad) {
			t.Errorf("ValidID(%q) = true, want false", bad)
		}
	}
	for _, good := range []string{"demo", "404", "_x_", "a.b", "%2f", "__x"} {
		if !ValidID(good) {
			t.Errorf("ValidID(%q) = false, want true", good)
		}
	}
}
