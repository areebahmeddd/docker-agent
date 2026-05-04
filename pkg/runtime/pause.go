package runtime

import "context"

// TogglePause toggles whether runStreamLoop pauses at iteration boundaries.
// Returns true if now paused. The pause takes effect as soon as the in-flight
// LLM request and its tool calls complete.
//
// Internally, pauseCh is non-nil and open while paused; closing it on resume
// wakes every goroutine waiting in waitIfPaused.
func (r *LocalRuntime) TogglePause() bool {
	r.pauseMu.Lock()
	defer r.pauseMu.Unlock()
	if r.pauseCh != nil {
		close(r.pauseCh)
		r.pauseCh = nil
		return false
	}
	r.pauseCh = make(chan struct{})
	return true
}

// waitIfPaused blocks until the runtime is resumed or ctx is cancelled.
func (r *LocalRuntime) waitIfPaused(ctx context.Context) error {
	r.pauseMu.Lock()
	ch := r.pauseCh
	r.pauseMu.Unlock()
	if ch == nil {
		return nil
	}
	select {
	case <-ch:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
