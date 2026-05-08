package runtime

// Pauser is implemented by runtimes that can pause/resume their loop at
// iteration boundaries.
type Pauser interface {
	TogglePause() bool
}

// PauserOf returns the runtime as a Pauser, or nil if it doesn't support
// pausing.
func PauserOf(r Runtime) Pauser {
	p, _ := r.(Pauser)
	return p
}

// ModelSwitcherOf returns the runtime as a ModelSwitcher, or nil if it
// doesn't support model switching.
func ModelSwitcherOf(r Runtime) ModelSwitcher {
	ms, _ := r.(ModelSwitcher)
	return ms
}

// ToolsChangeSubscriberOf returns the runtime as a ToolsChangeSubscriber,
// or nil if it doesn't emit tool-change notifications.
func ToolsChangeSubscriberOf(r Runtime) ToolsChangeSubscriber {
	tcs, _ := r.(ToolsChangeSubscriber)
	return tcs
}
