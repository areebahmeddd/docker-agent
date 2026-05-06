package runtime

import (
	"context"
	"os"

	"github.com/docker/docker-agent/pkg/session"
)

// WithSnapshots configures whether snapshot hooks are auto-injected for every agent.
func WithSnapshots(enabled bool) Opt {
	return func(r *LocalRuntime) {
		r.snapshotsEnabled = enabled
	}
}

// UndoLastSnapshot restores files recorded for the latest completed snapshot hook checkpoint.
func (r *LocalRuntime) UndoLastSnapshot(ctx context.Context, sess *session.Session) (files int, ok bool, err error) {
	if r == nil || sess == nil {
		return 0, false, nil
	}
	cwd := sess.WorkingDir
	if cwd == "" {
		cwd = r.workingDir
	}
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	return r.builtinsState.UndoLastSnapshot(ctx, sess.ID, cwd)
}
