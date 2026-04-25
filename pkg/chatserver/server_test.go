package chatserver

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/docker/docker-agent/pkg/chat"
	"github.com/docker/docker-agent/pkg/session"
)

func TestBuildSession_RequiresUserMessage(t *testing.T) {
	tests := []struct {
		name     string
		messages []ChatCompletionMessage
		wantNil  bool
	}{
		{
			name:    "empty list",
			wantNil: true,
		},
		{
			name: "only system messages",
			messages: []ChatCompletionMessage{
				{Role: "system", Content: "be helpful"},
			},
			wantNil: true,
		},
		{
			name: "blank user message is ignored",
			messages: []ChatCompletionMessage{
				{Role: "user", Content: "   "},
			},
			wantNil: true,
		},
		{
			name: "valid user message",
			messages: []ChatCompletionMessage{
				{Role: "user", Content: "hello"},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sess := buildSession(tc.messages)
			if tc.wantNil {
				assert.Nil(t, sess)
				return
			}
			require.NotNil(t, sess)
			assert.True(t, sess.ToolsApproved)
			assert.True(t, sess.NonInteractive)
		})
	}
}

func TestBuildSession_PreservesHistory(t *testing.T) {
	sess := buildSession([]ChatCompletionMessage{
		{Role: "system", Content: "you are a docker agent"},
		{Role: "user", Content: "hello"},
		{Role: "assistant", Content: "hi there"},
		{Role: "user", Content: "how are you?"},
	})
	require.NotNil(t, sess)

	// GetAllMessages omits system messages.
	all := sess.GetAllMessages()
	require.Len(t, all, 3)

	roles := make([]chat.MessageRole, len(all))
	for i, m := range all {
		roles[i] = m.Message.Role
	}
	assert.Equal(t, []chat.MessageRole{
		chat.MessageRoleUser,
		chat.MessageRoleAssistant,
		chat.MessageRoleUser,
	}, roles)

	assert.Equal(t, "how are you?", sess.GetLastUserMessageContent())
	assert.Equal(t, "hi there", sess.GetLastAssistantMessageContent())
}

func TestBuildSession_PreservesToolMessage(t *testing.T) {
	sess := buildSession([]ChatCompletionMessage{
		{Role: "user", Content: "compute 2+2"},
		{Role: "assistant", Content: ""}, // dropped: empty content
		{Role: "tool", Content: "4", ToolCallID: "call_1"},
	})
	require.NotNil(t, sess)

	all := sess.GetAllMessages()
	require.Len(t, all, 2)

	last := all[len(all)-1].Message
	assert.Equal(t, chat.MessageRoleTool, last.Role)
	assert.Equal(t, "4", last.Content)
	assert.Equal(t, "call_1", last.ToolCallID)
}

func TestBuildSession_UnknownRoleTreatedAsUser(t *testing.T) {
	sess := buildSession([]ChatCompletionMessage{
		{Role: "developer", Content: "do this"},
	})
	require.NotNil(t, sess)

	all := sess.GetAllMessages()
	require.Len(t, all, 1)
	assert.Equal(t, chat.MessageRoleUser, all[0].Message.Role)
	assert.Equal(t, "do this", all[0].Message.Content)
}

func TestSessionUsage_OmitsZero(t *testing.T) {
	sess := session.New()
	assert.Nil(t, sessionUsage(sess))

	sess.InputTokens = 5
	sess.OutputTokens = 7
	usage := sessionUsage(sess)
	require.NotNil(t, usage)
	assert.Equal(t, int64(5), usage.PromptTokens)
	assert.Equal(t, int64(7), usage.CompletionTokens)
	assert.Equal(t, int64(12), usage.TotalTokens)
}

func TestAgentPolicy_Pick(t *testing.T) {
	p := agentPolicy{exposed: []string{"root", "reviewer"}, fallback: "root"}

	assert.Equal(t, "reviewer", p.pick("reviewer"))
	assert.Equal(t, "root", p.pick("root"))
	assert.Equal(t, "root", p.pick(""), "empty model falls back")
	assert.Equal(t, "root", p.pick("gpt-4"), "unknown model falls back")
}

func TestErrTypeFor(t *testing.T) {
	assert.Equal(t, "invalid_request_error", errTypeFor(400))
	assert.Equal(t, "invalid_request_error", errTypeFor(404))
	assert.Equal(t, "internal_error", errTypeFor(500))
	assert.Equal(t, "internal_error", errTypeFor(502))
}
