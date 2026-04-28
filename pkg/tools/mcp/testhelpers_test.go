package mcp

// newTestToolset constructs a Toolset wired to the given mcpClient. It is
// for in-package tests only; callers do NOT call Start, so the supervisor
// starts in StateStopped. Helpers below let tests skip the supervisor
// where appropriate.
func newTestToolset(name, logID string, client mcpClient) *Toolset {
	ts := &Toolset{
		name:      name,
		mcpClient: client,
		logID:     logID,
	}
	ts.supervisor = newSupervisor(ts)
	return ts
}

// markStartedForTesting forces the supervisor into StateReady without going
// through Connect. Tests that exercise per-request code paths (callTool,
// Tools, ListPrompts) but do not want to drive a full lifecycle use this.
func (ts *Toolset) markStartedForTesting() {
	ts.supervisor.MarkReadyForTesting()
}
