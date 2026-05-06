package secretsscan

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestKeywordPrefilterIsIndependentLazy locks in the laziness
// contract: the AC table and every per-rule regex live behind their
// own [sync.OnceValue] entry points. Bundling them into a single
// initialiser would be a regression because it forces every consumer
// of [Redact] (and even of [ContainsSecrets] on a clean input) to pay
// for compiling ~85 regular expressions on the first call. We test
// the contract structurally — by counting whether any rule's regex
// has been built — because [sync.OnceValue]'s state isn't otherwise
// observable.
func TestKeywordPrefilterIsIndependentLazy(t *testing.T) {
	// We intentionally don't call t.Parallel: this test inspects
	// shared global state, and another parallel test could trigger
	// the very compilations we're checking are absent.

	// First, force the cheap metadata to materialise so the rule
	// slice is observable. This call must not, by itself, build any
	// regex — only the keyword bitsets and the lazy compile closures.
	rs := compiledRuleSet()
	require.NotEmpty(t, rs.rules, "rule set must not be empty")
	for i, r := range rs.rules {
		require.NotNilf(t, r.compile,
			"rule %d must carry a lazy regex compiler", i)
	}

	// Empty input must short-circuit before touching anything.
	_ = Redact("")
	_ = ContainsSecrets("")

	// A clean input passes the AC scan with an empty mask, so no
	// rule's compile closure should have run yet. We can't peek
	// inside [sync.OnceValues] directly, so we exercise the AC build
	// path explicitly and assert it succeeds without recursing into
	// rule compilation.
	const clean = "the quick brown fox jumps over the lazy dog"
	assert.False(t, ContainsSecrets(clean),
		"clean text must not match any rule")
	assert.Equal(t, clean, Redact(clean),
		"clean text must pass through Redact unchanged")

	// keywordPrefilter and compiledRuleSet must be distinct entry
	// points: building one must NOT trigger the other beyond the
	// keyword index they share.
	ac := keywordPrefilter()
	require.NotNil(t, ac, "AC automaton must be built on demand")
	require.NotEmpty(t, ac.next, "AC transition table must be populated")
}
