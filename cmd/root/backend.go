package root

import (
	"context"
	"log/slog"
	"sync"

	"github.com/docker/docker-agent/pkg/config"
	"github.com/docker/docker-agent/pkg/runtime"
	"github.com/docker/docker-agent/pkg/session"
)

// backend creates a runtime and a session, plus a cleanup closure that
// callers must invoke when they're done. Both --remote and the local code
// path will eventually be expressed as backends so the rest of runOrExec
// stops branching on f.remoteAddress.
type backend interface {
	CreateRuntimeAndSession(ctx context.Context) (runtime.Runtime, *session.Session, func(), error)
}

// localBackend builds the in-process runtime and session.
type localBackend struct {
	flags       *runExecFlags
	agentSource config.Source
}

func (b *localBackend) CreateRuntimeAndSession(ctx context.Context) (runtime.Runtime, *session.Session, func(), error) {
	loadResult, err := b.flags.loadAgentFrom(ctx, b.agentSource)
	if err != nil {
		return nil, nil, nil, err
	}

	rt, sess, err := b.flags.createLocalRuntimeAndSession(ctx, loadResult)
	if err != nil {
		stopToolSets(loadResult.Team)
		return nil, nil, nil, err
	}

	var once sync.Once
	cleanup := func() {
		once.Do(func() {
			stopToolSets(loadResult.Team)
			if err := rt.Close(); err != nil {
				slog.ErrorContext(ctx, "Failed to close runtime", "error", err)
			}
		})
	}
	return rt, sess, cleanup, nil
}
