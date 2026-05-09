package runtime

import (
	"github.com/docker/docker-agent/pkg/config"
	"github.com/docker/docker-agent/pkg/permissions"
)

// LoadTeamRequest is the typed input to a backend's team load. It carries
// everything the team loader needs: the resolved agent source plus the
// runtime config and the per-flag knobs the user supplied.
//
// LocalBackend consumes this struct directly. The same struct is the
// payload a future RemoteBackend marshals over the wire so the server
// runs teamloader.LoadWithConfig with identical inputs.
type LoadTeamRequest struct {
	Source         config.Source
	ModelOverrides []string
	PromptFiles    []string
	RunConfig      *config.RuntimeConfig
}

// CreateSessionRequest is the typed input to a backend's session creation.
// It carries the agent selection and every CLI-flag-driven session knob.
//
// The same forward-compatibility argument as LoadTeamRequest applies: this
// is the payload a remote backend will eventually send over the wire.
type CreateSessionRequest struct {
	AgentName         string
	ToolsApproved     bool
	HideToolResults   bool
	SessionDB         string
	ResumeSessionID   string
	SnapshotsEnabled  bool
	GlobalPermissions *permissions.Checker
	WorkingDir        string
}
