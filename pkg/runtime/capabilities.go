package runtime

// ToolsChangeSubscriberOf returns the runtime as a ToolsChangeSubscriber,
// or nil if it doesn't emit tool-change notifications.
func ToolsChangeSubscriberOf(r Runtime) ToolsChangeSubscriber {
	tcs, _ := r.(ToolsChangeSubscriber)
	return tcs
}
