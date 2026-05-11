package runtime

import (
	"context"

	"github.com/docker/docker-agent/pkg/api"
	"github.com/docker/docker-agent/pkg/config/latest"
	"github.com/docker/docker-agent/pkg/session"
	"github.com/docker/docker-agent/pkg/tools"
)

// RemoteClient is the interface that both HTTP and Connect-RPC clients implement
// for communicating with a remote docker agent server. It provides methods for
// creating sessions, running agents, and streaming events.
type RemoteClient interface {
	// GetAgent retrieves an agent configuration by ID
	GetAgent(ctx context.Context, id string) (*latest.Config, error)

	// CreateSession creates a new session
	CreateSession(ctx context.Context, sessTemplate *session.Session) (*session.Session, error)

	// ResumeSession resumes a paused session with optional rejection reason or tool name
	ResumeSession(ctx context.Context, id, confirmation, reason, toolName string) error

	// ResumeElicitation sends an elicitation response
	ResumeElicitation(ctx context.Context, sessionID string, action tools.ElicitationAction, content map[string]any) error

	// RunAgent executes an agent and returns a channel of streaming events
	RunAgent(ctx context.Context, sessionID, agent string, messages []api.Message) (<-chan Event, error)

	// RunAgentWithAgentName executes an agent with a specific agent name
	RunAgentWithAgentName(ctx context.Context, sessionID, agent, agentName string, messages []api.Message) (<-chan Event, error)

	// SteerSession injects user messages into a running session mid-turn
	SteerSession(ctx context.Context, sessionID string, messages []api.Message) error

	// FollowUpSession queues messages for end-of-turn processing
	FollowUpSession(ctx context.Context, sessionID string, messages []api.Message) error

	// UpdateSessionTitle updates the title of a session
	UpdateSessionTitle(ctx context.Context, sessionID, title string) error

	// GetAgentToolCount returns the number of tools available for an agent
	GetAgentToolCount(ctx context.Context, agentFilename, agentName string) (int, error)

	// StreamSessionEvents streams runtime events for a session as they occur
	StreamSessionEvents(ctx context.Context, sessionID string) (<-chan Event, error)
}

var _ RemoteClient = (*Client)(nil)
