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
//
// JSON tags reflect the wire contract being designed. Fields that don't
// yet have a wire-friendly representation (the Source interface, the
// RuntimeConfig with its embedded sync.Mutex and environment.Provider)
// are tagged json:"-" until they get one. Round-trip tests live in
// payload_test.go.
type LoadTeamRequest struct {
	Source         config.Source         `json:"-"`
	ModelOverrides []string              `json:"model_overrides,omitempty"`
	PromptFiles    []string              `json:"prompt_files,omitempty"`
	RunConfig      *config.RuntimeConfig `json:"-"`
}

// CreateSessionRequest is the typed input to a backend's session creation.
// It carries the agent selection and every CLI-flag-driven session knob.
//
// The same forward-compatibility argument as LoadTeamRequest applies: this
// is the payload a remote backend will eventually send over the wire.
//
// GlobalPermissions is currently json:"-" (the permissions.Checker is an
// opaque struct without a wire form yet); it'll get a serializable
// representation in a follow-up.
type CreateSessionRequest struct {
	AgentName         string               `json:"agent_name,omitempty"`
	ToolsApproved     bool                 `json:"tools_approved,omitempty"`
	HideToolResults   bool                 `json:"hide_tool_results,omitempty"`
	SessionDB         string               `json:"session_db,omitempty"`
	ResumeSessionID   string               `json:"resume_session_id,omitempty"`
	SnapshotsEnabled  bool                 `json:"snapshots_enabled,omitempty"`
	GlobalPermissions *permissions.Checker `json:"-"`
	WorkingDir        string               `json:"working_dir,omitempty"`
}
