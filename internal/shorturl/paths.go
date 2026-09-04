package shorturl

import (
	"log/slog"
	"regexp"
	"strings"
)

// ResolvePath finds the first rule whose pattern matches remainder and
// substitutes each named capture group into the rule's destination where it
// appears as "${name}". A group that did not participate in the match
// substitutes as "". A rule with no named groups uses its destination
// verbatim. Rules whose pattern does not compile are logged and skipped.
// ok is false when no rule matched.
//
// The original function required at least one named group for a match,
// which silently disabled every exact-match rule such as "^file\.zip$".
// That requirement is dropped here on purpose.
//
// Patterns are compiled with Go's regexp package (RE2 syntax). Go 1.22 and
// later accept the JavaScript named-group form "(?<name>...)". Lookaround
// and backreferences are not supported and such rules never match.
func ResolvePath(logger *slog.Logger, rules []PathRule, remainder string) (dest string, matched PathRule, ok bool) {
	for _, rule := range rules {
		re, err := regexp.Compile(rule.Pattern)
		if err != nil {
			logger.Warn("path rule pattern does not compile", "rule", rule.ID, "pattern", rule.Pattern, "err", err)
			continue
		}
		m := re.FindStringSubmatch(remainder)
		if m == nil {
			continue
		}
		dest = rule.Destination
		for i, n := range re.SubexpNames() {
			if i == 0 || n == "" {
				continue
			}
			dest = strings.ReplaceAll(dest, "${"+n+"}", m[i])
		}
		return dest, rule, true
	}
	return "", PathRule{}, false
}
