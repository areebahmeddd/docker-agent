package runtime

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/docker/docker-agent/pkg/agent"
	"github.com/docker/docker-agent/pkg/chat"
	"github.com/docker/docker-agent/pkg/hooks"
	"github.com/docker/docker-agent/pkg/secretsscan"
	"github.com/docker/docker-agent/pkg/team"
	"github.com/docker/docker-agent/pkg/tools"
)

// secretGitHubPAT is a syntactically-valid (but obviously fake) GitHub
// personal access token shape that the secretsscan ruleset detects.
// Centralising it makes the assertions below easier to read and keeps
// the secret value out of every individual test body.
const secretGitHubPAT = "ghp_cxLeRrvbJfmYdUtr70xnNE3Q7Gvli43s19PD"

// newRedactRuntime returns a [LocalRuntime] hosting a single agent
// with redactSecrets toggled per the flag. It exists so the per-case
// setup in [TestRedactSecretsTransform_GatedOnAgentFlag] stays a
// one-liner; tests need a real runtime because the transform looks
// the agent up via r.team.Agent.
func newRedactRuntime(t *testing.T, redact bool) *LocalRuntime {
	t.Helper()
	prov := &mockProvider{id: "test/mock-model", stream: &mockStream{}}
	a := agent.New("root", "instructions",
		agent.WithModel(prov),
		agent.WithRedactSecrets(redact),
	)
	tm := team.New(team.WithAgents(a))
	r, err := NewLocalRuntime(tm, WithModelStore(mockModelStore{}))
	require.NoError(t, err)
	return r
}

// TestRedactSecretsTransform_GatedOnAgentFlag pins the central design
// invariant: the LLM-side scrubbing only fires for agents that opted
// in via [agent.WithRedactSecrets] (a.k.a. AgentConfig.RedactSecrets:
// true). When the flag is off the message slice must reach the
// provider unchanged — no allocations beyond the slice header copy.
func TestRedactSecretsTransform_GatedOnAgentFlag(t *testing.T) {
	t.Parallel()

	dirty := []chat.Message{{
		Role:    chat.MessageRoleUser,
		Content: "use this token: " + secretGitHubPAT,
	}}

	t.Run("flag on → redacts", func(t *testing.T) {
		t.Parallel()
		r := newRedactRuntime(t, true)
		got, err := r.redactSecretsTransform(t.Context(),
			&hooks.Input{AgentName: "root"}, dirty)
		require.NoError(t, err)
		require.Len(t, got, 1)
		assert.NotContains(t, got[0].Content, secretGitHubPAT,
			"flag-on agent must scrub Content")
		assert.Contains(t, got[0].Content, secretsscan.RedactionMarker)
	})

	t.Run("flag off → passthrough", func(t *testing.T) {
		t.Parallel()
		r := newRedactRuntime(t, false)
		got, err := r.redactSecretsTransform(t.Context(),
			&hooks.Input{AgentName: "root"}, dirty)
		require.NoError(t, err)
		assert.Equal(t, dirty, got, "flag-off agent must passthrough untouched")
	})

	t.Run("missing agent → passthrough", func(t *testing.T) {
		t.Parallel()
		r := newRedactRuntime(t, true)
		got, err := r.redactSecretsTransform(t.Context(),
			&hooks.Input{AgentName: "no-such-agent"}, dirty)
		require.NoError(t, err)
		assert.Equal(t, dirty, got,
			"unknown agent must not panic or modify the slice")
	})

	t.Run("nil input → passthrough", func(t *testing.T) {
		t.Parallel()
		r := newRedactRuntime(t, true)
		got, err := r.redactSecretsTransform(t.Context(), nil, dirty)
		require.NoError(t, err)
		assert.Equal(t, dirty, got)
	})
}

// TestRedactSecretsTransform_ScrubsAllSurfaces locks in the field
// coverage: the transform must scrub Content, MultiContent text
// parts, AND the JSON-encoded ToolCall arguments — that's the full
// set of text-bearing fields a future model call can read. A miss
// on any of these surfaces is a real leak (a previous turn's tool
// call with a secret in its arguments would otherwise round-trip
// to the LLM unchanged).
func TestRedactSecretsTransform_ScrubsAllSurfaces(t *testing.T) {
	t.Parallel()

	r := newRedactRuntime(t, true)

	in := []chat.Message{{
		Role:    chat.MessageRoleAssistant,
		Content: "primary content with " + secretGitHubPAT,
		MultiContent: []chat.MessagePart{
			{Type: chat.MessagePartTypeText, Text: "part with " + secretGitHubPAT},
			{Type: chat.MessagePartTypeText, Text: "clean part"},
		},
		ToolCalls: []tools.ToolCall{{
			ID:   "call_1",
			Type: "function",
			Function: tools.FunctionCall{
				Name:      "shell",
				Arguments: `{"cmd":"curl -H 'Authorization: token ` + secretGitHubPAT + `' https://api.github.com"}`,
			},
		}},
	}}

	out, err := r.redactSecretsTransform(t.Context(),
		&hooks.Input{AgentName: "root"}, in)
	require.NoError(t, err)
	require.Len(t, out, 1)

	assert.NotContains(t, out[0].Content, secretGitHubPAT, "Content must be scrubbed")
	require.Len(t, out[0].MultiContent, 2)
	assert.NotContains(t, out[0].MultiContent[0].Text, secretGitHubPAT,
		"MultiContent text must be scrubbed")
	assert.Equal(t, "clean part", out[0].MultiContent[1].Text,
		"clean parts must remain identity-equal so downstream caches don't churn")
	require.Len(t, out[0].ToolCalls, 1)
	assert.NotContains(t, out[0].ToolCalls[0].Function.Arguments, secretGitHubPAT,
		"ToolCall.Arguments must be scrubbed")
}

// TestRedactSecretsTransform_PreservesIdentityWhenClean is the
// negative-symmetry test for the copy-on-write contract: a fully
// clean conversation must reach the provider as the SAME slice
// values (the loop allocates a fresh slice header but per-message
// values are passed by value via the loop, so equality is the
// observable check). A regression here — e.g. a future "always copy
// MultiContent" change — would silently double allocations on every
// LLM call for the 99% case.
func TestRedactSecretsTransform_PreservesIdentityWhenClean(t *testing.T) {
	t.Parallel()

	r := newRedactRuntime(t, true)

	clean := []chat.Message{{
		Role:    chat.MessageRoleUser,
		Content: "summarise the README please",
		MultiContent: []chat.MessagePart{
			{Type: chat.MessagePartTypeText, Text: "no secrets here"},
		},
	}}

	out, err := r.redactSecretsTransform(t.Context(),
		&hooks.Input{AgentName: "root"}, clean)
	require.NoError(t, err)
	require.Len(t, out, 1)
	assert.Equal(t, clean[0], out[0],
		"clean messages must reach the provider value-equal")
}
