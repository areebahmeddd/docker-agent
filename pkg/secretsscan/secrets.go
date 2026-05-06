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
	found := keywordPrefilter().scan(text)
	if found[0]|found[1] == 0 {
		return false
	}
	rs := compiledRuleSet()
	for i := range rs.rules {
		r := &rs.rules[i]
		if found[0]&r.kwBits[0]|found[1]&r.kwBits[1] == 0 {
			continue
		}
		re, _ := r.compile()
		if re.MatchString(text) {
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
	// One Aho–Corasick pass over the input gives us a bitset of every
	// keyword present, so a rule's keyword check collapses to two AND
	// instructions. The mask is taken from the original input: the
	// redaction can only REMOVE keywords (RedactionMarker contains
	// none — see TestRedactionMarkerIsNotASecret) so a stale "yes" on
	// a rewritten string just means we run a regex that won't match.
	//
	// On a clean input the AC scan returns an empty mask and we
	// short-circuit before touching [compiledRuleSet] — no rule
	// regexes are compiled, ever, in a process that only sees clean
	// text.
	found := keywordPrefilter().scan(text)
	if found[0]|found[1] == 0 {
		return text
	}
	rs := compiledRuleSet()
	out := text
	for i := range rs.rules {
		r := &rs.rules[i]
		if found[0]&r.kwBits[0]|found[1]&r.kwBits[1] == 0 {
			continue
		}
		out = redactWithRule(r, out)
	}
	return out
}

// redactWithRule applies a single compiled rule to text. We can't use
// [regexp.Regexp.ReplaceAllStringFunc] directly because we need the
// match indices to slice out the "secret" subgroup while keeping the
// rest of the match intact.
func redactWithRule(r *compiledRule, text string) string {
	re, secretIdx := r.compile()
	matches := re.FindAllStringSubmatchIndex(text, -1)
	if len(matches) == 0 {
		return text
	}

	var b strings.Builder
	b.Grow(len(text))
	cursor := 0
	for _, m := range matches {
		start, end := m[0], m[1]
		if secretIdx >= 0 && m[2*secretIdx] >= 0 {
			start, end = m[2*secretIdx], m[2*secretIdx+1]
		}
		b.WriteString(text[cursor:start])
		b.WriteString(RedactionMarker)
		cursor = end
	}
	b.WriteString(text[cursor:])
	return b.String()
}
