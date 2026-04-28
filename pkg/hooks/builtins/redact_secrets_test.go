package builtins

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/docker/docker-agent/pkg/hooks"
	"github.com/docker/docker-agent/pkg/secretsscan"
)

// TestRedactSecretsScrubsTopLevelStringValue locks in the headline
// behavior: a string tool argument carrying a recognised secret is
// replaced with [secretsscan.RedactionMarker] before the runtime
// hands it off to the tool.
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
	require.Contains(t, updated, "command")

	cmd, ok := updated["command"].(string)
	require.True(t, ok)
	assert.NotContains(t, cmd, secret, "raw secret must be gone")
	assert.Contains(t, cmd, secretsscan.RedactionMarker)
	assert.Equal(t, 30, updated["timeout"], "non-string scalars pass through unchanged")
	assert.Equal(t, hooks.EventPreToolUse, out.HookSpecificOutput.HookEventName)
}

// TestRedactSecretsReturnsNilWhenNothingChanged keeps the
// no-overhead path explicit. The executor treats nil Output as
// "this builtin contributed nothing" (no system message, no
// UpdatedInput merge), which is the cheapest possible flow for the
// 99% case where a tool call carries no secrets.
func TestRedactSecretsReturnsNilWhenNothingChanged(t *testing.T) {
	t.Parallel()

	in := &hooks.Input{
		HookEventName: hooks.EventPreToolUse,
		ToolName:      "shell",
		ToolInput: map[string]any{
			"command": "ls -la",
			"timeout": 30,
		},
	}

	out, err := redactSecrets(t.Context(), in, nil)
	require.NoError(t, err)
	assert.Nil(t, out, "no secrets ⇒ no Output")
}

// TestRedactSecretsHandlesNilAndEmptyInputs documents the safety
// floor: a missing or empty ToolInput must not panic and must not
// fabricate an UpdatedInput. The executor calls redactSecrets on
// every pre_tool_use match, including for tools that take no
// arguments.
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

// TestRedactSecretsWalksNestedStructures pins the recursion contract.
// Tool args produced by an MCP server / OpenAPI bridge often arrive
// as map[string]any / []any payloads (json.Unmarshal-style); we must
// dive into them so a token nested two levels deep is still caught.
// Without this, a bash heredoc, a JSON --config payload, or a list
// of HTTP headers becomes a backdoor.
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
	require.NotNil(t, out.HookSpecificOutput)

	updated := out.HookSpecificOutput.UpdatedInput

	headers, ok := updated["headers"].(map[string]any)
	require.True(t, ok, "nested map should be walked & rebuilt")
	auth, _ := headers["Authorization"].(string)
	assert.NotContains(t, auth, secret)
	assert.Contains(t, auth, secretsscan.RedactionMarker)
	assert.Equal(t, "application/json", headers["Accept"], "non-secret header preserved")

	tags, ok := updated["tags"].([]any)
	require.True(t, ok, "nested slice should be walked & rebuilt")
	require.Len(t, tags, 3)
	assert.Equal(t, "prod", tags[0])
	tag1, _ := tags[1].(string)
	assert.NotContains(t, tag1, secret)
	assert.Contains(t, tag1, secretsscan.RedactionMarker)
	assert.Equal(t, 42, tags[2])
}

// TestRedactSecretsIsRegistered is the round-trip guard: invoking the
// builtin via [hooks.Registry] (the path the YAML config takes) must
// produce the same UpdatedInput as a direct call. A regression here
// usually means we forgot the [hooks.Registry.RegisterBuiltin] line.
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
	require.NotNil(t, out.HookSpecificOutput)

	cmd, _ := out.HookSpecificOutput.UpdatedInput["cmd"].(string)
	assert.NotContains(t, cmd, secret)
}

// TestApplyAgentDefaultsInjectsRedactSecrets covers the agent-flag
// wiring: setting AgentDefaults.RedactSecrets must materialise a
// pre_tool_use matcher targeting the redact_secrets builtin with a
// wildcard tool match. This is the runtime behavior for the
// agent-level "redact_secrets: true" flag; without this test a future
// refactor of ApplyAgentDefaults could silently drop it.
func TestApplyAgentDefaultsInjectsRedactSecrets(t *testing.T) {
	t.Parallel()

	cfg := ApplyAgentDefaults(nil, AgentDefaults{RedactSecrets: true})
	require.NotNil(t, cfg, "redact_secrets alone should keep the config non-empty")
	require.Len(t, cfg.PreToolUse, 1)
	matcher := cfg.PreToolUse[0]
	assert.Equal(t, "*", matcher.Matcher)
	require.Len(t, matcher.Hooks, 1)
	assert.Equal(t, hooks.HookTypeBuiltin, matcher.Hooks[0].Type)
	assert.Equal(t, RedactSecrets, matcher.Hooks[0].Command)
}
