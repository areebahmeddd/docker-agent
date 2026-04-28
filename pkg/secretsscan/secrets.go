package secretsscan

import (
	"strings"
)

// RedactionMarker is the literal string that replaces every detected
// secret span. Exported so callers (tests, runtime hooks that want to
// double-check the result) can refer to it without hard-coding the
// literal.
const RedactionMarker = "[REDACTED]"

// ContainsSecrets reports whether text matches any detection rule.
// The keyword shortlist filter keeps the cost proportional to the
// number of rules whose keyword actually appears in text — for
// typical chat content that's zero.
func ContainsSecrets(text string) bool {
	if text == "" {
		return false
	}
	lower := strings.ToLower(text)
	for i := range compiledRules() {
		r := &compiledRules()[i]
		if !r.hasKeyword(lower) {
			continue
		}
		if r.re.MatchString(text) {
			return true
		}
	}
	return false
}

// Redact returns a copy of text with every detected secret span
// replaced by [RedactionMarker]. When a rule defines a (?P<secret>…)
// named subgroup, only the secret span is replaced (the surrounding
// keyword/connector context is preserved so the caller still sees
// "AWS_SECRET_ACCESS_KEY=[REDACTED]"); otherwise the entire match is
// replaced.
//
// Idempotent: [RedactionMarker] does not match any rule, so calling
// Redact twice yields the same result. Safe for concurrent use.
func Redact(text string) string {
	if text == "" {
		return text
	}
	out := text
	// Iterate via index to avoid copying compiledRule (it carries a
	// *regexp.Regexp that is safe to share but still cheaper to point at).
	for i := range compiledRules() {
		r := &compiledRules()[i]
		if !r.hasKeyword(strings.ToLower(out)) {
			continue
		}
		out = redactWithRule(r, out)
	}
	return out
}

// redactWithRule applies a single compiled rule to text, replacing
// either the "secret" subgroup span (when defined) or the entire
// match. Split out so the rule-iteration logic in [Redact] reads as
// a straight loop.
//
// We can't use [regexp.Regexp.ReplaceAllStringFunc] directly because
// it gives the function the matched substring (string), not the
// match indices — and we need indices to slice out the "secret"
// subgroup while keeping the rest of the match intact. Walking
// FindAllStringSubmatchIndex and rebuilding the string by hand is
// the standard pattern.
func redactWithRule(r *compiledRule, text string) string {
	idxs := r.re.FindAllStringSubmatchIndex(text, -1)
	if len(idxs) == 0 {
		return text
	}

	var b strings.Builder
	b.Grow(len(text))
	cursor := 0
	for _, m := range idxs {
		matchStart, matchEnd := m[0], m[1]
		var redactStart, redactEnd int
		if r.secretIdx >= 0 && 2*r.secretIdx+1 < len(m) && m[2*r.secretIdx] >= 0 {
			redactStart, redactEnd = m[2*r.secretIdx], m[2*r.secretIdx+1]
		} else {
			redactStart, redactEnd = matchStart, matchEnd
		}

		// Defensive: a malformed subgroup span (start>end) should
		// fall back to redacting the full match rather than producing
		// a corrupt output. Hard to construct in practice — the regex
		// engine guarantees ordered indices for successful submatches —
		// but the cost of guarding is nil.
		if redactStart < cursor || redactEnd < redactStart {
			redactStart, redactEnd = matchStart, matchEnd
		}

		b.WriteString(text[cursor:redactStart])
		b.WriteString(RedactionMarker)
		cursor = redactEnd
	}
	b.WriteString(text[cursor:])
	return b.String()
}
