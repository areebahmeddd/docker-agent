package builtins

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/docker/docker-agent/pkg/hooks"
	"github.com/docker/docker-agent/pkg/secretsscan"
)

// TestRedactSecretsScrubsTopLevelStringValue: a recognised secret in
// a top-level string argument is replaced and ONLY the rewritten key
// is emitted in UpdatedInput. The latter is critical because
// pre_tool_use hooks aggregate via shallow maps.Copy in config order
// — returning unchanged keys would clobber concurrent hooks'
// modifications.
func TestRedactSecretsScrubsTopLevelStringValue(t *testing.T) {
	t.Parallel()

	const secret = "ghp_cxLeRrvbJfmYdUtr70xnNE3Q7Gvli43s19PD"

	in := &hooks.Input{
		HookEventName: hooks.EventPreToolUse,
		ToolName:      "shell",
		ToolInput: map[string]any{
			"command": "curl -H 'Authorization: token " + secret + "' https://api.github.com",
			"timeout": 30,
		},
	}

	out, err := redactSecrets(t.Context(), in, nil)
	require.NoError(t, err)
	require.NotNil(t, out, "must return Output when redaction happened")
	require.NotNil(t, out.HookSpecificOutput)

	updated := out.HookSpecificOutput.UpdatedInput
	cmd, ok := updated["command"].(string)
	require.True(t, ok, "changed key must appear in UpdatedInput")
	assert.NotContains(t, cmd, secret, "raw secret must be gone")
	assert.Contains(t, cmd, secretsscan.RedactionMarker)
	assert.NotContains(t, updated, "timeout",
		"unchanged keys must NOT appear in UpdatedInput (would clobber concurrent hooks)")
	assert.Equal(t, hooks.EventPreToolUse, out.HookSpecificOutput.HookEventName)
}

// TestRedactSecretsReturnsNilWhenNothingChanged: clean tool calls
// take the no-overhead path (executor treats nil Output as "this
// builtin contributed nothing").
func TestRedactSecretsReturnsNilWhenNothingChanged(t *testing.T) {
	t.Parallel()

	out, err := redactSecrets(t.Context(), &hooks.Input{
		HookEventName: hooks.EventPreToolUse,
		ToolName:      "shell",
		ToolInput:     map[string]any{"command": "ls -la", "timeout": 30},
	}, nil)
	require.NoError(t, err)
	assert.Nil(t, out, "no secrets ⇒ no Output")
}

// TestRedactSecretsHandlesNilAndEmptyInputs: missing/empty ToolInput
// must not panic and must not fabricate an UpdatedInput.
func TestRedactSecretsHandlesNilAndEmptyInputs(t *testing.T) {
	t.Parallel()

	for _, in := range []*hooks.Input{
		nil,
		{HookEventName: hooks.EventPreToolUse},
		{HookEventName: hooks.EventPreToolUse, ToolInput: map[string]any{}},
	} {
		out, err := redactSecrets(t.Context(), in, nil)
		require.NoError(t, err)
		assert.Nil(t, out)
	}
}

// TestRedactSecretsWalksNestedStructures: secrets nested inside
// map[string]any / []any payloads (the shape MCP and OpenAPI bridges
// pass through) are caught alongside top-level values. The rebuilt
// nested container preserves unchanged sibling values — the
// "only changed keys" rule applies at the TOP level only, since the
// executor's maps.Copy is shallow.
func TestRedactSecretsWalksNestedStructures(t *testing.T) {
	t.Parallel()

	const secret = "dckr_pat_" + "AAAAAAAAAAAAAAAAAAAAAAAAAAA"

	in := &hooks.Input{
		HookEventName: hooks.EventPreToolUse,
		ToolName:      "http_request",
		ToolInput: map[string]any{
			"headers": map[string]any{
				"Authorization": "Bearer " + secret,
				"Accept":        "application/json",
			},
			"tags": []any{"prod", secret, 42},
		},
	}

	out, err := redactSecrets(t.Context(), in, nil)
	require.NoError(t, err)
	require.NotNil(t, out)
	updated := out.HookSpecificOutput.UpdatedInput

	headers := updated["headers"].(map[string]any)
	auth, _ := headers["Authorization"].(string)
	assert.NotContains(t, auth, secret)
	assert.Contains(t, auth, secretsscan.RedactionMarker)
	assert.Equal(t, "application/json", headers["Accept"], "non-secret header preserved")

	tags := updated["tags"].([]any)
	require.Len(t, tags, 3)
	assert.Equal(t, "prod", tags[0])
	tag1, _ := tags[1].(string)
	assert.NotContains(t, tag1, secret)
	assert.Contains(t, tag1, secretsscan.RedactionMarker)
	assert.Equal(t, 42, tags[2])
}

// TestRedactSecretsIsRegistered: the builtin is reachable via
// hooks.Registry (the path YAML config takes), not just by direct
// call. Regressions usually mean a missing RegisterBuiltin line.
func TestRedactSecretsIsRegistered(t *testing.T) {
	t.Parallel()

	reg := hooks.NewRegistry()
	state, err := Register(reg)
	require.NoError(t, err)
	t.Cleanup(func() { state.ClearSession("") })

	handler, ok := reg.LookupBuiltin(RedactSecrets)
	require.Truef(t, ok, "builtin %q must be registered", RedactSecrets)

	const secret = "ghp_cxLeRrvbJfmYdUtr70xnNE3Q7Gvli43s19PD"
	out, err := handler(t.Context(), &hooks.Input{
		HookEventName: hooks.EventPreToolUse,
		ToolName:      "shell",
		ToolInput:     map[string]any{"cmd": secret},
	}, nil)
	require.NoError(t, err)
	require.NotNil(t, out)
	cmd, _ := out.HookSpecificOutput.UpdatedInput["cmd"].(string)
	assert.NotContains(t, cmd, secret)
}

// TestApplyAgentDefaultsInjectsRedactSecrets: setting the agent flag
// must materialise a wildcard pre_tool_use matcher pointing at the
// redact_secrets builtin.
func TestApplyAgentDefaultsInjectsRedactSecrets(t *testing.T) {
	t.Parallel()

	cfg := ApplyAgentDefaults(nil, AgentDefaults{RedactSecrets: true})
	require.NotNil(t, cfg)
	require.Len(t, cfg.PreToolUse, 1)
	matcher := cfg.PreToolUse[0]
	assert.Equal(t, "*", matcher.Matcher)
	require.Len(t, matcher.Hooks, 1)
	assert.Equal(t, hooks.HookTypeBuiltin, matcher.Hooks[0].Type)
	assert.Equal(t, RedactSecrets, matcher.Hooks[0].Command)
}
