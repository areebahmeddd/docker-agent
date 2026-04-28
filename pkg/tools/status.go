package tools

import (
	"github.com/docker/docker-agent/pkg/tools/lifecycle"
)

// ToolsetStatus is a snapshot of a single toolset's lifecycle, suitable for
// status surfaces (TUI /toolsets, JSON status endpoints, logs).
//
// The fields are intentionally flat and self-describing so they can be
// rendered without the renderer needing to import lifecycle.
type ToolsetStatus struct {
	// Name is the toolset name as configured in the agent YAML (or a
	// derived label when the YAML has no name).
	Name string
	// Description is the user-visible Describer.Describe() output, never
	// containing secrets. Empty when the toolset does not implement
	// Describer.
	Description string
	// State is the lifecycle state. For toolsets that don't implement
	// Statable, the runtime sets it to StateReady when the toolset has a
	// usable tool list and StateStopped otherwise — matching what the
	// user actually observes.
	State lifecycle.State
	// LastError is the most recent failure recorded by the supervisor, or
	// nil. Toolsets that don't implement Statable always report nil.
	LastError error
	// RestartCount is the number of supervisor restarts since the last
	// successful Ready transition. Zero for toolsets without a supervisor.
	RestartCount int
}
