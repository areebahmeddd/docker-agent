package root

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/docker/docker-agent/pkg/cli"
	"github.com/docker/docker-agent/pkg/runtime"
	"github.com/docker/docker-agent/pkg/server"
	"github.com/docker/docker-agent/pkg/session"
)

// startAttachedServer exposes the in-process runtime over HTTP so external
// processes can drive the running TUI (steer, followup, resume, ...). The
// listener is closed when ctx is cancelled. No-op when --listen is empty.
func (f *runExecFlags) startAttachedServer(ctx context.Context, out *cli.Printer, rt runtime.Runtime, sess *session.Session) error {
	if f.listenAddr == "" {
		return nil
	}

	sm := server.NewSessionManager(ctx, nil, rt.SessionStore(), 0, &f.runConfig)
	sm.AttachRuntime(sess.ID, rt, sess)

	ln, err := server.Listen(ctx, f.listenAddr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", f.listenAddr, err)
	}
	context.AfterFunc(ctx, func() { _ = ln.Close() })

	out.Println("Control plane listening on", ln.Addr().String())
	warnIfNotLoopback(out, ln.Addr())

	srv := server.NewWithManager(sm)
	go func() {
		if err := srv.Serve(ctx, ln); err != nil {
			slog.ErrorContext(ctx, "Control plane server stopped", "error", err)
		}
	}()

	return nil
}
