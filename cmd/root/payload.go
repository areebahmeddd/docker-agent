package root

import (
	"github.com/docker/docker-agent/pkg/config"
	"github.com/docker/docker-agent/pkg/permissions"
)

// LoadTeamRequest is the typed input to a backend's team load. It carries
// everything the team loader needs: the resolved agent source plus the
// runtime config and the per-flag knobs the user supplied.
//
// Today only LocalBackend consumes it. The intent is that a future
// RemoteBackend marshals the same struct over the wire so the server runs
// teamloader.LoadWithConfig with identical inputs.
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

// loadTeamRequest builds a LoadTeamRequest from the current flags.
func (f *runExecFlags) loadTeamRequest(agentSource config.Source) LoadTeamRequest {
	return LoadTeamRequest{
		Source:         agentSource,
		ModelOverrides: f.modelOverrides,
		PromptFiles:    f.promptFiles,
		RunConfig:      &f.runConfig,
	}
}

// createSessionRequest builds a CreateSessionRequest from the current
// flags and the supplied working directory.
func (f *runExecFlags) createSessionRequest(workingDir string) CreateSessionRequest {
	return CreateSessionRequest{
		AgentName:         f.agentName,
		ToolsApproved:     f.autoApprove,
		HideToolResults:   f.hideToolResults,
		SessionDB:         f.sessionDB,
		ResumeSessionID:   f.sessionID,
		SnapshotsEnabled:  f.snapshotsEnabled,
		GlobalPermissions: f.globalPermissions,
		WorkingDir:        workingDir,
	}
}
