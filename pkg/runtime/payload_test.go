package runtime

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadTeamRequest_RoundTrip(t *testing.T) {
	t.Parallel()

	in := LoadTeamRequest{
		ModelOverrides: []string{"openai/gpt-4o", "anthropic/claude-3-5-sonnet-20240620"},
		PromptFiles:    []string{"prompts/role.md", "prompts/tone.md"},
	}

	data, err := json.Marshal(in)
	require.NoError(t, err)

	var out LoadTeamRequest
	require.NoError(t, json.Unmarshal(data, &out))

	assert.Equal(t, in.ModelOverrides, out.ModelOverrides)
	assert.Equal(t, in.PromptFiles, out.PromptFiles)
}

func TestLoadTeamRequest_OmitsEmptyFields(t *testing.T) {
	t.Parallel()

	data, err := json.Marshal(LoadTeamRequest{})
	require.NoError(t, err)
	assert.JSONEq(t, `{}`, string(data))
}

func TestLoadTeamRequest_NonSerializableFieldsExcluded(t *testing.T) {
	t.Parallel()

	// Source and RunConfig are intentionally json:"-" because they don't
	// have a wire form yet. This test pins that contract so a future
	// edit that adds a tag without designing a wire shape gets caught.
	data, err := json.Marshal(LoadTeamRequest{
		ModelOverrides: []string{"x"},
	})
	require.NoError(t, err)
	assert.NotContains(t, string(data), "source")
	assert.NotContains(t, string(data), "run_config")
}

func TestCreateSessionRequest_RoundTrip(t *testing.T) {
	t.Parallel()

	in := CreateSessionRequest{
		AgentName:        "main",
		ToolsApproved:    true,
		HideToolResults:  true,
		SessionDB:        "/tmp/sessions.db",
		ResumeSessionID:  "01HF8...",
		SnapshotsEnabled: true,
		WorkingDir:       "/tmp/work",
	}

	data, err := json.Marshal(in)
	require.NoError(t, err)

	var out CreateSessionRequest
	require.NoError(t, json.Unmarshal(data, &out))

	assert.Equal(t, in, out)
}

func TestCreateSessionRequest_OmitsEmptyFields(t *testing.T) {
	t.Parallel()

	data, err := json.Marshal(CreateSessionRequest{})
	require.NoError(t, err)
	assert.JSONEq(t, `{}`, string(data))
}

func TestCreateSessionRequest_NonSerializableFieldsExcluded(t *testing.T) {
	t.Parallel()

	// GlobalPermissions has no wire form yet.
	data, err := json.Marshal(CreateSessionRequest{AgentName: "x"})
	require.NoError(t, err)
	assert.NotContains(t, string(data), "global_permissions")
}
