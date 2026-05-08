package root

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"sync"

	"github.com/docker/docker-agent/pkg/config"
	"github.com/docker/docker-agent/pkg/runtime"
	"github.com/docker/docker-agent/pkg/session"
	"github.com/docker/docker-agent/pkg/tui"
)

// backend creates a runtime and a session, plus a cleanup closure that
// callers must invoke when they're done. Both --remote and the local code
// path are expressed as backends so runOrExec stops branching on
// f.remoteAddress.
type backend interface {
	CreateRuntimeAndSession(ctx context.Context) (runtime.Runtime, *session.Session, func(), error)
	Spawner(rt runtime.Runtime) tui.SessionSpawner
}

// selectBackend picks the backend implied by the current flags.
func (f *runExecFlags) selectBackend(agentFileName string) (backend, error) {
	if f.remoteAddress != "" {
		return &remoteBackend{flags: f, agentFileName: agentFileName}, nil
	}
	agentSource, err := config.Resolve(agentFileName, f.runConfig.EnvProvider())
	if err != nil {
		return nil, err
	}
	return &localBackend{flags: f, agentSource: agentSource}, nil
}

// localBackend builds the in-process runtime and session.
type localBackend struct {
	flags       *runExecFlags
	agentSource config.Source
}

func (b *localBackend) CreateRuntimeAndSession(ctx context.Context) (runtime.Runtime, *session.Session, func(), error) {
	loadResult, err := b.flags.loadAgentFrom(ctx, b.flags.loadTeamRequest(b.agentSource))
	if err != nil {
		return nil, nil, nil, err
	}

	wd, _ := os.Getwd()
	rt, sess, err := b.flags.createLocalRuntimeAndSession(ctx, loadResult, b.flags.createSessionRequest(wd))
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

func (b *localBackend) Spawner(rt runtime.Runtime) tui.SessionSpawner {
	return b.flags.createSessionSpawner(b.agentSource, rt.SessionStore())
}

// remoteBackend talks to a docker-agent server.
type remoteBackend struct {
	flags         *runExecFlags
	agentFileName string
}

func (b *remoteBackend) CreateRuntimeAndSession(ctx context.Context) (runtime.Runtime, *session.Session, func(), error) {
	client, err := runtime.NewClient(b.flags.remoteAddress)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to create remote client: %w", err)
	}

	sessTemplate := session.New(
		session.WithToolsApproved(b.flags.autoApprove),
	)

	sess, err := client.CreateSession(ctx, sessTemplate)
	if err != nil {
		return nil, nil, nil, err
	}

	rt, err := runtime.NewRemoteRuntime(client,
		runtime.WithRemoteCurrentAgent(b.flags.agentName),
		runtime.WithRemoteAgentFilename(b.agentFileName),
	)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to create remote runtime: %w", err)
	}

	slog.DebugContext(ctx, "Using remote runtime", "address", b.flags.remoteAddress, "agent", b.flags.agentName)

	cleanup := func() {
		if err := rt.Close(); err != nil {
			slog.ErrorContext(ctx, "Failed to close remote runtime", "error", err)
		}
	}
	return rt, sess, cleanup, nil
}

func (b *remoteBackend) Spawner(runtime.Runtime) tui.SessionSpawner {
	return nil
}
