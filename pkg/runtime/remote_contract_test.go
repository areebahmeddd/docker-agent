package runtime

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/docker/docker-agent/pkg/api"
	"github.com/docker/docker-agent/pkg/config/latest"
	"github.com/docker/docker-agent/pkg/session"
	"github.com/docker/docker-agent/pkg/tools"
)

// stubRemoteClient is a RemoteClient that returns enough state for the
// non-streaming Runtime contract to exercise RemoteRuntime end-to-end.
//
// Methods used only by streaming paths (RunAgent, RunAgentWithAgentName,
// CreateSession) panic so an accidental wiring against them is loud
// rather than silent.
type stubRemoteClient struct {
	cfg *latest.Config
}

func (s *stubRemoteClient) GetAgent(context.Context, string) (*latest.Config, error) {
	return s.cfg, nil
}

func (s *stubRemoteClient) CreateSession(context.Context, *session.Session) (*session.Session, error) {
	panic("CreateSession not exercised by the contract test")
}

func (s *stubRemoteClient) ResumeSession(context.Context, string, string, string, string) error {
	return nil
}

func (s *stubRemoteClient) ResumeElicitation(context.Context, string, tools.ElicitationAction, map[string]any) error {
	return nil
}

func (s *stubRemoteClient) RunAgent(context.Context, string, string, []api.Message) (<-chan Event, error) {
	panic("RunAgent not exercised by the contract test")
}

func (s *stubRemoteClient) RunAgentWithAgentName(context.Context, string, string, string, []api.Message) (<-chan Event, error) {
	panic("RunAgentWithAgentName not exercised by the contract test")
}

func (s *stubRemoteClient) SteerSession(context.Context, string, []api.Message) error {
	return nil
}

func (s *stubRemoteClient) FollowUpSession(context.Context, string, []api.Message) error {
	return nil
}

func (s *stubRemoteClient) UpdateSessionTitle(context.Context, string, string) error {
	return nil
}

func (s *stubRemoteClient) GetAgentToolCount(context.Context, string, string) (int, error) {
	return 0, nil
}

// TestRemoteRuntime_Contract runs the same surface contract LocalRuntime
// passes against a RemoteRuntime backed by a stub client. Any silent
// no-op on RemoteRuntime that the contract considers a failure surfaces
// here as a red test rather than a runtime user complaint.
func TestRemoteRuntime_Contract(t *testing.T) {
	runRuntimeContract(t, func(t *testing.T) Runtime {
		t.Helper()
		client := &stubRemoteClient{
			cfg: &latest.Config{
				Agents: latest.Agents{
					{Name: "test", Description: "test agent"},
				},
			},
		}
		rt, err := NewRemoteRuntime(client)
		require.NoError(t, err)

		// Seed a session ID so Steer / FollowUp / Resume / ResumeElicitation
		// reach the (stubbed) client instead of returning "no active session".
		rt.sessionID = "test-session"
		return rt
	})
}
