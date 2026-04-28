package secretsscan_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/docker/docker-agent/pkg/secretsscan"
)

// TestContainsSecretsRecognisesKnownTokens locks in the parity
// guarantee with the upstream
// github.com/docker/mcp-gateway/pkg/secretsscan tests: the well-known
// GitHub PAT and Docker Hub PAT shapes that the upstream package
// detects must keep being detected here. Failing this test means we
// either dropped a rule or broke the keyword pre-filter.
func TestContainsSecretsRecognisesKnownTokens(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		text string
	}{
		{"github_pat", "ghp_cxLeRrvbJfmYdUtr70xnNE3Q7Gvli43s19PD"},
		{"docker_pat", "dckr_pat_" + "AAAAAAAAAAAAAAAAAAAAAAAAAAA"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Truef(t, secretsscan.ContainsSecrets(tc.text), "must detect %s", tc.name)
		})
	}
}

// TestContainsSecretsIgnoresHarmlessText documents the false-positive
// floor: pure digit strings, plain English, and the empty string must
// never trip detection. Two-step keyword + regex filtering makes this
// cheap; the assertion exists so a future "be more aggressive" change
// can't quietly start flagging invoice numbers.
func TestContainsSecretsIgnoresHarmlessText(t *testing.T) {
	t.Parallel()

	cases := []string{
		"",
		"1234567890",
		"hello world",
		"please summarise the README",
		// "key" is a keyword for aws-secret-access-key but the regex
		// requires a 40-char base64-ish span next to "aws_*key=" so a
		// bare mention must not trip.
		"the api key is documented in README",
	}
	for _, in := range cases {
		assert.Falsef(t, secretsscan.ContainsSecrets(in), "must not flag %q", in)
	}
}

// TestRedactReplacesSecretSpan pins the headline behavior of the
// redactor: the secret material is replaced by [secretsscan.RedactionMarker]
// while the surrounding text (including the keyword that triggered
// the match) is preserved. We don't assert the exact match boundary
// because the rule's leading-context group ([^0-9a-zA-Z]|^) may
// consume the preceding space — only the secret value must
// disappear.
func TestRedactReplacesSecretSpan(t *testing.T) {
	t.Parallel()

	const ghp = "ghp_cxLeRrvbJfmYdUtr70xnNE3Q7Gvli43s19PD"
	in := "Run this with token=" + ghp + " and you're set"

	out := secretsscan.Redact(in)

	assert.Containsf(t, out, secretsscan.RedactionMarker, "redaction marker must appear: %q", out)
	assert.NotContainsf(t, out, ghp, "raw secret must be gone: %q", out)
	assert.Contains(t, out, "Run this with token=", "non-secret prefix preserved")
	assert.Contains(t, out, "and you're set", "non-secret suffix preserved")
}

// TestRedactIsIdempotent locks in the safety property the runtime
// transforms rely on: passing already-redacted text through Redact
// again leaves it untouched. Without this we'd risk amplification
// (e.g. the marker overlapping a future, broader rule) when both the
// pre_tool_use builtin and the before_llm_call transform fire on the
// same content.
func TestRedactIsIdempotent(t *testing.T) {
	t.Parallel()

	once := secretsscan.Redact("dckr_pat_" + "AAAAAAAAAAAAAAAAAAAAAAAAAAA in logs")
	twice := secretsscan.Redact(once)

	assert.Equal(t, once, twice)
	assert.False(t, secretsscan.ContainsSecrets(once),
		"redacted output must no longer trip ContainsSecrets")
}

// TestRedactPreservesNonMatchingText is the negative-symmetry of
// [TestContainsSecretsIgnoresHarmlessText]: text without secrets must
// pass through untouched. Equality (not "Contains marker") catches a
// regression where a too-broad rule inserts a marker into innocent
// content.
func TestRedactPreservesNonMatchingText(t *testing.T) {
	t.Parallel()

	cases := []string{
		"",
		"1234567890",
		"please refactor the helper into its own file",
	}
	for _, in := range cases {
		assert.Equalf(t, in, secretsscan.Redact(in), "must not modify %q", in)
	}
}

// TestRedactHandlesMultipleSecretsInOneInput verifies the
// FindAllStringSubmatchIndex loop: two distinct secrets in the same
// string must both be replaced, and nothing in between should leak
// out. This is the regression test for the rebuild-by-cursor logic
// in redactWithRule.
func TestRedactHandlesMultipleSecretsInOneInput(t *testing.T) {
	t.Parallel()

	const a = "ghp_cxLeRrvbJfmYdUtr70xnNE3Q7Gvli43s19PD"
	const b = "ghp_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	in := "first " + a + " and second " + b + " end"

	out := secretsscan.Redact(in)

	require.NotContains(t, out, a)
	require.NotContains(t, out, b)
	assert.Equal(t, 2, strings.Count(out, secretsscan.RedactionMarker),
		"both secrets must be redacted: %q", out)
	assert.Contains(t, out, "first ")
	assert.Contains(t, out, " and second ")
	assert.Contains(t, out, " end")
}
