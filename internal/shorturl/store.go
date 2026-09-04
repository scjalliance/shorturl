package shorturl

import (
	"context"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strings"
)

// ErrNotFound reports that no link document exists for a host and slug, or
// that the slug can never be a Firestore document ID.
var ErrNotFound = errors.New("link not found")

// Link is one short link document. Field names mirror the Firestore fields
// documented in the README; the zero value means the field was absent.
type Link struct {
	Destination          string
	Frame                string
	Passthrough          bool
	PassthroughAnyStatus bool
	PassQueryString      bool
	UsePaths             bool
	StatusCode           int
}

// PathRule is one document in a link's "paths" subcollection.
type PathRule struct {
	ID          string
	Pattern     string
	Destination string
}

// Store reads links and records analytics. Record methods are best effort:
// the handler calls them with a short timeout and only logs failures.
type Store interface {
	// GetLink returns the link for slug under host. It returns ErrNotFound
	// when the document does not exist.
	GetLink(ctx context.Context, host, slug string) (Link, error)
	// ListPathRules returns the link's path rules in document ID order.
	ListPathRules(ctx context.Context, host, slug string) ([]PathRule, error)
	// RecordClick increments clickCount and, when viaQR is set, qrUseCount.
	RecordClick(ctx context.Context, host, slug string, viaQR bool) error
	// RecordQRCreate increments qrCreateCount.
	RecordQRCreate(ctx context.Context, host, slug string) error
	// RecordPathMatch increments matchCount on one path rule.
	RecordPathMatch(ctx context.Context, host, slug, ruleID string) error
}

// LinkFromMap converts a raw Firestore document into a Link using JavaScript
// truthiness for the flag fields, because the original implementation tested
// them with plain `if (data.flag)` and existing documents rely on that.
func LinkFromMap(m map[string]any) Link {
	var l Link
	if v, ok := m["destination"]; ok {
		l.Destination = stringOf(v)
	}
	if v, ok := m["frame"]; ok && truthy(v) {
		l.Frame = stringOf(v)
	}
	l.Passthrough = truthy(m["passthrough"])
	l.PassthroughAnyStatus = truthy(m["passthroughAnyStatus"])
	l.PassQueryString = truthy(m["passQueryString"])
	l.UsePaths = truthy(m["usePaths"])
	if n, ok := numberOf(m["statusCode"]); ok && n >= 200 && n <= 599 {
		l.StatusCode = n
	}
	return l
}

// PathRuleFromMap converts a raw Firestore path document into a PathRule.
func PathRuleFromMap(id string, m map[string]any) PathRule {
	return PathRule{ID: id, Pattern: stringOf(m["pattern"]), Destination: stringOf(m["destination"])}
}

// truthy reports whether v would be truthy in JavaScript.
func truthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case string:
		return x != ""
	case int64:
		return x != 0
	case float64:
		return x != 0 && !math.IsNaN(x)
	default:
		return true
	}
}

// stringOf renders v the way JavaScript string interpolation would for the
// scalar types Firestore returns. Missing values render as "".
func stringOf(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	default:
		return fmt.Sprint(x)
	}
}

// numberOf returns v as an int when Firestore stored it as a number.
func numberOf(v any) (int, bool) {
	switch x := v.(type) {
	case int64:
		return int(x), true
	case float64:
		if math.IsNaN(x) || math.IsInf(x, 0) || math.Trunc(x) != x {
			return 0, false
		}
		return int(x), true
	default:
		return 0, false
	}
}

var reservedID = regexp.MustCompile(`^__.*__$`)

// ValidID reports whether s can be a Firestore collection or document ID.
// Invalid IDs are treated as not found so that odd URLs get the normal 404
// flow instead of an error from the Firestore client.
func ValidID(s string) bool {
	switch {
	case s == "", s == ".", s == "..":
		return false
	case len(s) > 1500:
		return false
	case strings.Contains(s, "/"):
		return false
	case reservedID.MatchString(s):
		return false
	}
	return true
}
