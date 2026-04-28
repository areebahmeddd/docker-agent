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
const secretGitHubPAT = "ghp_cxLeRrvbJfmYdUtr70xnNE3Q7Gvli43s19PD"

// newRedactRuntime returns a [LocalRuntime] hosting a single agent
// with redactSecrets toggled per the flag. Tests need a real runtime
// because the transform looks the agent up via r.team.Agent.
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

// TestRedactSecretsTransform_GatedOnAgentFlag: scrubbing only fires
// for agents that opted in via the redact_secrets flag; flag-off,
// missing, and nil-input cases must passthrough untouched.
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
		assert.NotContains(t, got[0].Content, secretGitHubPAT)
		assert.Contains(t, got[0].Content, secretsscan.RedactionMarker)
	})

	t.Run("flag off → passthrough", func(t *testing.T) {
		t.Parallel()
		r := newRedactRuntime(t, false)
		got, err := r.redactSecretsTransform(t.Context(),
			&hooks.Input{AgentName: "root"}, dirty)
		require.NoError(t, err)
		assert.Equal(t, dirty, got)
	})

	t.Run("missing agent → passthrough", func(t *testing.T) {
		t.Parallel()
		r := newRedactRuntime(t, true)
		got, err := r.redactSecretsTransform(t.Context(),
			&hooks.Input{AgentName: "no-such-agent"}, dirty)
		require.NoError(t, err)
		assert.Equal(t, dirty, got)
	})

	t.Run("nil input → passthrough", func(t *testing.T) {
		t.Parallel()
		r := newRedactRuntime(t, true)
		got, err := r.redactSecretsTransform(t.Context(), nil, dirty)
		require.NoError(t, err)
		assert.Equal(t, dirty, got)
	})
}

// TestRedactSecretsTransform_ScrubsAllSurfaces: every text-bearing
// field a model can read on the wire (Content, ReasoningContent,
// MultiContent text, the legacy singular FunctionCall.Arguments, and
// each ToolCall.Function.Arguments) must be scrubbed. A miss on any
// of these is a real leak — e.g. a previous turn's reasoning trace
// or tool call carrying a secret would otherwise round-trip to the
// next LLM call.
func TestRedactSecretsTransform_ScrubsAllSurfaces(t *testing.T) {
	t.Parallel()

	r := newRedactRuntime(t, true)

	in := []chat.Message{{
		Role:             chat.MessageRoleAssistant,
		Content:          "primary content with " + secretGitHubPAT,
		ReasoningContent: "thinking about " + secretGitHubPAT,
		MultiContent: []chat.MessagePart{
			{Type: chat.MessagePartTypeText, Text: "part with " + secretGitHubPAT},
			{Type: chat.MessagePartTypeText, Text: "clean part"},
		},
		FunctionCall: &tools.FunctionCall{
			Name:      "legacy_call",
			Arguments: `{"token":"` + secretGitHubPAT + `"}`,
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
	assert.NotContains(t, out[0].ReasoningContent, secretGitHubPAT, "ReasoningContent must be scrubbed")
	require.Len(t, out[0].MultiContent, 2)
	assert.NotContains(t, out[0].MultiContent[0].Text, secretGitHubPAT)
	assert.Equal(t, "clean part", out[0].MultiContent[1].Text, "clean parts pass through")
	require.NotNil(t, out[0].FunctionCall)
	assert.NotContains(t, out[0].FunctionCall.Arguments, secretGitHubPAT,
		"legacy singular FunctionCall.Arguments must be scrubbed")
	require.Len(t, out[0].ToolCalls, 1)
	assert.NotContains(t, out[0].ToolCalls[0].Function.Arguments, secretGitHubPAT)
}

// TestRedactSecretsTransform_PreservesCleanContent: a fully clean
// conversation must reach the provider with values equal to the
// originals.
func TestRedactSecretsTransform_PreservesCleanContent(t *testing.T) {
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
	assert.Equal(t, clean[0], out[0])
}

// TestRedactSecretsTransform_DoesNotMutateInput: scrubbing produces a
// fresh slice/MultiContent/FunctionCall/ToolCalls — the caller's
// history is left unchanged. Critical because callers (history
// compactors, cache stores) keep references to the pre-transform
// values; mutating them would corrupt persisted state.
func TestRedactSecretsTransform_DoesNotMutateInput(t *testing.T) {
	t.Parallel()

	r := newRedactRuntime(t, true)

	in := []chat.Message{{
		Role:             chat.MessageRoleAssistant,
		Content:          "with " + secretGitHubPAT,
		ReasoningContent: "thinking with " + secretGitHubPAT,
		MultiContent: []chat.MessagePart{
			{Type: chat.MessagePartTypeText, Text: "part with " + secretGitHubPAT},
		},
		FunctionCall: &tools.FunctionCall{Arguments: secretGitHubPAT},
		ToolCalls: []tools.ToolCall{{
			Function: tools.FunctionCall{Arguments: secretGitHubPAT},
		}},
	}}

	_, err := r.redactSecretsTransform(t.Context(),
		&hooks.Input{AgentName: "root"}, in)
	require.NoError(t, err)

	assert.Contains(t, in[0].Content, secretGitHubPAT, "input.Content must be untouched")
	assert.Contains(t, in[0].ReasoningContent, secretGitHubPAT,
		"input.ReasoningContent must be untouched")
	assert.Contains(t, in[0].MultiContent[0].Text, secretGitHubPAT,
		"input.MultiContent must be untouched")
	assert.Contains(t, in[0].FunctionCall.Arguments, secretGitHubPAT,
		"input.FunctionCall must be untouched (deep copy needed)")
	assert.Contains(t, in[0].ToolCalls[0].Function.Arguments, secretGitHubPAT,
		"input.ToolCalls must be untouched")
}
