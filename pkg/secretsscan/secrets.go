package secretsscan

import (
	"strings"
)

// RedactionMarker replaces every detected secret span. Chosen so it
// doesn't match any rule's keyword pre-filter — see
// TestRedactionMarkerIsNotASecret for the safety property that makes
// [Redact] idempotent.
const RedactionMarker = "[REDACTED]"

// ContainsSecrets reports whether text matches any detection rule.
func ContainsSecrets(text string) bool {
	if text == "" {
		return false
	}
	lower := strings.ToLower(text)
	for _, r := range compiledRules() {
		if hasAnyKeyword(lower, r.keywords) && r.re.MatchString(text) {
			return true
		}
	}
	return false
}

// Redact returns a copy of text with every detected secret span
// replaced by [RedactionMarker]. When a rule defines a (?P<secret>…)
// named subgroup, only that span is replaced (so callers still see
// "AWS_SECRET_ACCESS_KEY=[REDACTED]"); otherwise the whole match is
// replaced.
//
// Idempotent: [RedactionMarker] does not match any rule, so calling
// Redact twice yields the same result.
func Redact(text string) string {
	if text == "" {
		return text
	}
	// Lower-case once outside the rule loop. The redaction can only
	// REMOVE keywords from the input (RedactionMarker contains none —
	// see TestRedactionMarkerIsNotASecret), so the keyword pre-filter
	// stays correct against the original lower-cased text even after
	// earlier rules have rewritten part of out: a false-positive on
	// the filter just means we run a regex that won't match.
	rules := compiledRules()
	lower := strings.ToLower(text)
	out := text
	for _, r := range rules {
		if !hasAnyKeyword(lower, r.keywords) {
			continue
		}
		out = redactWithRule(r, out)
	}
	return out
}

// hasAnyKeyword is the cheap pre-filter that lets us skip the regex
// hot path on inputs that don't even mention a rule's discriminating
// keyword. lower must already be lower-cased; rule keywords are
// stored lower-cased at compile time.
func hasAnyKeyword(lower string, keywords []string) bool {
	for _, kw := range keywords {
		if strings.Contains(lower, kw) {
			return true
		}
	}
	return false
}

// redactWithRule applies a single compiled rule to text. We can't use
// [regexp.Regexp.ReplaceAllStringFunc] directly because we need the
// match indices to slice out the "secret" subgroup while keeping the
// rest of the match intact.
func redactWithRule(r compiledRule, text string) string {
	matches := r.re.FindAllStringSubmatchIndex(text, -1)
	if len(matches) == 0 {
		return text
	}

	var b strings.Builder
	b.Grow(len(text))
	cursor := 0
	for _, m := range matches {
		start, end := m[0], m[1]
		if r.secretIdx >= 0 && m[2*r.secretIdx] >= 0 {
			start, end = m[2*r.secretIdx], m[2*r.secretIdx+1]
		}
		b.WriteString(text[cursor:start])
		b.WriteString(RedactionMarker)
		cursor = end
	}
	b.WriteString(text[cursor:])
	return b.String()
}
